package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/gitstore"

	"gorm.io/gorm"
)

// MergePR merges a pull request and updates DB state.
// strategy: "merge", "squash", or "rebase".
func (s *Service) MergePR(ctx context.Context, repoFullName string, number int, strategy, commitTitle string) (db.PullRequest, error) {
	// Enforce authentication before any state access to avoid leaking
	// PR state to unauthenticated callers.
	currentUser, err := s.GetCurrentUser(ctx)
	if err != nil {
		var pr db.PullRequest
		return pr, fmt.Errorf("authentication required for merge: %w", err)
	}

	pr, err := s.loadPRForMerge(ctx, repoFullName, number)
	if err != nil {
		return pr, err
	}
	if pr.Merged {
		return pr, fmt.Errorf("pr already merged")
	}
	if pr.State != db.StateOpen {
		return pr, fmt.Errorf("pr is not open (state: %s)", pr.State)
	}

	mergeMethod := strings.ToLower(strategy)
	if mergeMethod == "" {
		mergeMethod = "merge"
	}

	if err := s.enforceMergePolicy(ctx, currentUser, &pr); err != nil {
		return pr, err
	}

	if err := s.mergePRRecordWithUser(ctx, currentUser, &pr, mergeMethod, commitTitle); err != nil {
		return pr, err
	}
	return pr, nil
}

// MergePRByID merges a pull request identified by its internal DB ID.
// It performs the git merge/rebase, then updates the PR record.
// If commitMsg is empty, a default merge message is used.
func (s *Service) MergePRByID(ctx context.Context, prID uint, mergeMethod, commitMsg string) error {
	// Enforce authentication before any DB/state access to avoid leaking
	// PR existence or state to unauthenticated callers.
	currentUser, err := s.GetCurrentUser(ctx)
	if err != nil {
		return fmt.Errorf("authentication required for merge: %w", err)
	}

	pr, err := s.loadPRForMergeByID(ctx, prID)
	if err != nil {
		return err
	}
	if pr.Merged {
		return fmt.Errorf("pr already merged")
	}
	if pr.State != db.StateOpen {
		return fmt.Errorf("pr is not open (state: %s)", pr.State)
	}
	if err := s.enforceMergePolicy(ctx, currentUser, &pr); err != nil {
		return err
	}
	return s.mergePRRecordWithUser(ctx, currentUser, &pr, mergeMethod, commitMsg)
}

// MergePRRecord performs the git merge and updates the PR record.
// Shared between MergePRByID (GQL) and MergePR (REST).
// If commitMsg is empty, a default merge message is used.
func (s *Service) MergePRRecord(ctx context.Context, pr *db.PullRequest, mergeMethod, commitMsg string) error {
	currentUser, err := s.GetCurrentUser(ctx)
	if err != nil {
		return fmt.Errorf("authentication required for merge: %w", err)
	}
	if err := s.enforceMergePolicy(ctx, currentUser, pr); err != nil {
		return err
	}
	return s.mergePRRecordWithUser(ctx, currentUser, pr, mergeMethod, commitMsg)
}

func (s *Service) loadPRForMerge(ctx context.Context, repoFullName string, number int) (db.PullRequest, error) {
	repo, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return db.PullRequest{}, err
	}

	var pr db.PullRequest
	if err := s.DBForCtx(ctx).
		Select("id", "number", "repository_id", "merged", "state", "head_ref", "head_sha", "base_ref").
		First(&pr, "repository_id = ? AND number = ?", repo.ID, number).Error; err != nil {
		return pr, wrapErrf(err, "pull request #%d", number)
	}
	pr.Repository = repo
	return pr, nil
}

func (s *Service) loadPRForMergeByID(ctx context.Context, prID uint) (db.PullRequest, error) {
	var pr db.PullRequest
	if err := s.DBForCtx(ctx).
		Select("id", "number", "repository_id", "merged", "state", "head_ref", "head_sha", "base_ref").
		Preload("Repository", func(q *gorm.DB) *gorm.DB {
			return q.Select("id", "full_name")
		}).
		First(&pr, prID).Error; err != nil {
		return pr, wrapErrf(err, "pull request id %d", prID)
	}
	return pr, nil
}

func (s *Service) mergePRRecordWithUser(ctx context.Context, currentUser db.User, pr *db.PullRequest, mergeMethod, commitMsg string) error {
	return s.mergePRRecordWithActor(ctx, currentUser, pr, mergeMethod, commitMsg)
}

func (s *Service) mergePRRecordWithActor(ctx context.Context, actor db.User, pr *db.PullRequest, mergeMethod, commitMsg string) error {
	if s.Git == nil || pr.Repository.FullName == "" {
		// No git store available — just mark merged in DB.
		now := time.Now()
		if err := s.DBForCtx(ctx).Model(pr).Updates(map[string]any{
			"merged": true, "merged_at": &now,
			"state": db.StateClosed, "closed_at": &now,
			"merged_by_login":              actor.Login,
			"auto_merge":                   false,
			"auto_merge_method":            "",
			"auto_merge_commit_headline":   "",
			"auto_merge_commit_body":       "",
			"auto_merge_author_email":      "",
			"auto_merge_expected_head_sha": "",
			"auto_merge_enabled_by_login":  "",
		}).Error; err != nil {
			return err
		}
		pr.Merged = true
		pr.MergedAt = &now
		pr.State = db.StateClosed
		pr.ClosedAt = &now
		pr.MergedByLogin = actor.Login
		pr.AutoMerge = false
		pr.AutoMergeMethod = ""
		pr.AutoMergeCommitHeadline = ""
		pr.AutoMergeCommitBody = ""
		pr.AutoMergeAuthorEmail = ""
		pr.AutoMergeExpectedHeadSHA = ""
		pr.AutoMergeEnabledByLogin = ""
		return nil
	}
	if commitMsg == "" {
		commitMsg = fmt.Sprintf("Merge pull request #%d", pr.Number)
	}
	var sha string
	var mergeErr error
	switch mergeMethod {
	case "rebase":
		sha, mergeErr = s.Git.Rebase(ctx, gitstore.RebaseOptions{
			FullName:   pr.Repository.FullName,
			BaseBranch: pr.BaseRef,
			HeadBranch: pr.HeadRef,
			Committer:  actor.Login,
			Email:      actor.Email,
		})
	case "squash":
		sha, mergeErr = s.Git.SquashMerge(ctx, gitstore.SquashMergeOptions{
			FullName:      pr.Repository.FullName,
			BaseBranch:    pr.BaseRef,
			HeadBranch:    pr.HeadRef,
			Committer:     actor.Login,
			Email:         actor.Email,
			SquashMessage: commitMsg,
		})
	default:
		sha, mergeErr = s.Git.Merge(ctx, gitstore.MergeOptions{
			FullName:     pr.Repository.FullName,
			BaseBranch:   pr.BaseRef,
			HeadBranch:   pr.HeadRef,
			Committer:    actor.Login,
			Email:        actor.Email,
			MergeMessage: commitMsg,
		})
	}
	if mergeErr != nil {
		if isMergeConflict(mergeErr) {
			return fmt.Errorf("%w: merge conflict: %v", ErrConflict, mergeErr)
		}
		return fmt.Errorf("MergePRRecord: git %s: %w", mergeMethod, mergeErr)
	}
	now := time.Now()
	if err := s.DBForCtx(ctx).Model(pr).Updates(map[string]any{
		"merged": true, "merged_at": &now,
		"state": db.StateClosed, "closed_at": &now,
		"merge_commit_sha":             sha,
		"merged_by_login":              actor.Login,
		"auto_merge":                   false,
		"auto_merge_method":            "",
		"auto_merge_commit_headline":   "",
		"auto_merge_commit_body":       "",
		"auto_merge_author_email":      "",
		"auto_merge_expected_head_sha": "",
		"auto_merge_enabled_by_login":  "",
	}).Error; err != nil {
		return err
	}
	pr.Merged = true
	pr.MergedAt = &now
	pr.State = db.StateClosed
	pr.ClosedAt = &now
	pr.MergeCommitSHA = sha
	pr.MergedByLogin = actor.Login
	pr.AutoMerge = false
	pr.AutoMergeMethod = ""
	pr.AutoMergeCommitHeadline = ""
	pr.AutoMergeCommitBody = ""
	pr.AutoMergeAuthorEmail = ""
	pr.AutoMergeExpectedHeadSHA = ""
	pr.AutoMergeEnabledByLogin = ""
	return nil
}

func isMergeConflict(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "conflict")
}

// PushHeadSHA updates the HeadSHA stored in the PR after a real git push.
func (s *Service) PushHeadSHA(ctx context.Context, repoFullName string, number int) error {
	pr, err := s.GetPR(ctx, repoFullName, number)
	if err != nil {
		return err
	}
	sha, err := s.Git.HeadSHA(ctx, repoFullName, pr.HeadRef)
	if err != nil {
		return nil // non-fatal
	}
	if err := s.DBForCtx(ctx).Model(&db.PullRequest{}).Where("id = ?", pr.ID).Update("head_sha", sha).Error; err != nil {
		return err
	}
	// Update commit messages and filenames for search
	return s.UpdatePRCommitData(ctx, repoFullName, number)
}

// ListPRCommits returns the real commits of a PR.
func (s *Service) ListPRCommits(ctx context.Context, repoFullName string, number int) ([]map[string]any, error) {
	pr, err := s.GetPR(ctx, repoFullName, number)
	if err != nil {
		return nil, err
	}
	return s.ListPRCommitsFromLoaded(ctx, pr)
}

// ListPRCommitsFromLoaded returns commits using an already-loaded PR,
// avoiding redundant GetPR + GetRepo re-queries.
func (s *Service) ListPRCommitsFromLoaded(ctx context.Context, pr db.PullRequest) ([]map[string]any, error) {
	logOut, err := s.Git.PRCommitsLog(ctx, pr.Repository.FullName, pr.BaseSHA, pr.HeadSHA)
	if err != nil {
		return []map[string]any{}, nil // default to empty if git fails
	}

	lines := strings.Split(strings.TrimSpace(logOut), "\n")
	var commits []map[string]any
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 5)
		if len(parts) < 5 {
			continue
		}
		sha, name, email, date, msg := parts[0], parts[1], parts[2], parts[3], parts[4]
		user := map[string]any{"name": name, "email": email, "date": date}
		commits = append(commits, map[string]any{
			"sha": sha,
			"commit": map[string]any{
				"author":    user,
				"committer": user,
				"message":   msg,
				"tree":      map[string]any{"sha": sha},
				"url":       "",
			},
			"author":    map[string]any{"login": name},
			"committer": map[string]any{"login": name},
			"parents":   []any{},
		})
	}
	if commits == nil {
		commits = []map[string]any{}
	}
	return commits, nil
}

// ListPRFiles returns the real files changed in a PR with line-level stats.
func (s *Service) ListPRFiles(ctx context.Context, repoFullName string, number int) ([]map[string]any, error) {
	pr, err := s.GetPR(ctx, repoFullName, number)
	if err != nil {
		return nil, err
	}
	return s.DiffFiles(ctx, repoFullName, pr.BaseSHA, pr.HeadSHA)
}

// DiffFiles returns file-level diff stats between two SHAs.
func (s *Service) DiffFiles(ctx context.Context, repoFullName, baseSHA, headSHA string) ([]map[string]any, error) {
	if baseSHA == "" || headSHA == "" {
		return []map[string]any{}, nil
	}

	// Get file-level additions/deletions via --numstat
	numstatOut, err := s.Git.DiffNumStat(ctx, repoFullName, baseSHA, headSHA)
	numstatMap := map[string][2]int{} // filename → [additions, deletions]
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(numstatOut), "\n") {
			if line == "" {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				var add, del int
				fmt.Sscanf(parts[0], "%d", &add)
				fmt.Sscanf(parts[1], "%d", &del)
				numstatMap[parts[2]] = [2]int{add, del}
			}
		}
	}

	// Get file statuses via --name-status
	nameStatusOut, err := s.Git.DiffNameStatus(ctx, repoFullName, baseSHA, headSHA)
	if err != nil {
		return []map[string]any{}, nil
	}

	lines := strings.Split(strings.TrimSpace(nameStatusOut), "\n")
	var files []map[string]any
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			status := parts[0]
			filename := parts[len(parts)-1]
			var statusText string
			switch status {
			case "A":
				statusText = "added"
			case "D":
				statusText = "removed"
			case "M":
				statusText = "modified"
			default:
				statusText = "changed"
			}
			stats := numstatMap[filename]
			files = append(files, map[string]any{
				"sha":       "",
				"filename":  filename,
				"status":    statusText,
				"additions": stats[0],
				"deletions": stats[1],
				"changes":   stats[0] + stats[1],
			})
		}
	}
	if files == nil {
		files = []map[string]any{}
	}
	return files, nil
}

// PRDiffStats returns total additions, deletions, and changed file count for a PR.
// Uses DiffNumStats (one git subprocess) instead of Compare (four) since callers
// — notably the ListPRs hot path — only consume file-level stats.
func (s *Service) PRDiffStats(ctx context.Context, repoFullName, baseSHA, headSHA string) (additions, deletions, changedFiles int) {
	if baseSHA == "" || headSHA == "" || s.Git == nil {
		return 0, 0, 0
	}
	files, err := s.Git.DiffNumStats(ctx, repoFullName, baseSHA, headSHA)
	if err != nil {
		return 0, 0, 0
	}
	for _, f := range files {
		additions += f.Additions
		deletions += f.Deletions
	}
	return additions, deletions, len(files)
}
