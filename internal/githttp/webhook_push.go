package githttp

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/service"
)

const zeroGitSHA = "0000000000000000000000000000000000000000"

type pushRefChange struct {
	Ref     string
	Before  string
	After   string
	Created bool
	Deleted bool
	Forced  bool
	Exclude []string
}

func (h *Handler) handlePostPushWebhooks(ctx context.Context, repoFullName, repoPath string, beforeRefs map[string]string) error {
	repo, err := h.Svc.GetRepo(ctx, repoFullName)
	if err != nil {
		return err
	}
	afterRefs, err := snapshotRefs(ctx, repoPath)
	if err != nil {
		return err
	}
	changes := diffPushedRefs(ctx, repoPath, beforeRefs, afterRefs)
	if len(changes) == 0 {
		return nil
	}

	if err := h.Svc.UpdateRepositoryPushedAt(ctx, repo.ID, time.Now()); err != nil {
		return err
	}
	repo, err = h.Svc.GetRepo(ctx, repoFullName)
	if err != nil {
		return err
	}

	for _, change := range changes {
		payload := h.buildPushWebhookPayload(ctx, repoPath, repo, change)
		if err := h.Svc.DispatchWebhookEvent(ctx, repo.ID, "push", "", payload); err != nil {
			return err
		}
		if !strings.HasPrefix(change.Ref, "refs/heads/") || change.Deleted {
			continue
		}
		branch := strings.TrimPrefix(change.Ref, "refs/heads/")
		prs, err := h.Svc.ListOpenPRsByHead(ctx, repo.ID, branch)
		if err != nil {
			return err
		}
		for _, pr := range prs {
			updatedPR, err := h.Svc.SyncPRHeadAfterPush(ctx, pr.ID, repo.FullName)
			if err != nil {
				return err
			}
			if err := h.Svc.DispatchWebhookEvent(ctx, updatedPR.RepositoryID, "pull_request", "synchronize", map[string]any{
				"action":       "synchronize",
				"number":       updatedPR.Number,
				"pull_request": transform.PR(updatedPR, nil, transform.AuthorAssociationChecks{}, nil),
				"repository":   transform.Repo(updatedPR.Repository),
				"sender":       pushSenderPayload(ctx),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *Handler) buildPushWebhookPayload(ctx context.Context, repoPath string, repo db.Repository, change pushRefChange) map[string]any {
	commits, headCommit := pushCommits(ctx, repoPath, repo.FullName, h.Svc.HTMLBaseURL(), change)
	var compareURL string
	if change.Before != zeroGitSHA && change.After != zeroGitSHA {
		compareURL = fmt.Sprintf("%s/%s/compare/%s...%s", h.Svc.HTMLBaseURL(), repo.FullName, change.Before, change.After)
	}
	return map[string]any{
		"ref":         change.Ref,
		"before":      change.Before,
		"after":       change.After,
		"created":     change.Created,
		"deleted":     change.Deleted,
		"forced":      change.Forced,
		"base_ref":    nil,
		"compare":     compareURL,
		"commits":     commits,
		"head_commit": headCommit,
		"repository":  transform.Repo(repo),
		"pusher":      pushPusherPayload(ctx),
		"sender":      pushSenderPayload(ctx),
	}
}

func pushSenderPayload(ctx context.Context) any {
	user, ok := service.UserFromContext(ctx)
	if !ok {
		return nil
	}
	return transform.User(user)
}

func pushPusherPayload(ctx context.Context) map[string]any {
	user, ok := service.UserFromContext(ctx)
	if !ok {
		return map[string]any{"name": "", "email": ""}
	}
	return map[string]any{
		"name":  user.Login,
		"email": user.Email,
	}
}

func snapshotRefs(ctx context.Context, repoPath string) (map[string]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "for-each-ref", "--format=%(refname) %(objectname)")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("snapshot refs: %w", err)
	}
	refs := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		refs[parts[0]] = parts[1]
	}
	return refs, nil
}

func diffPushedRefs(ctx context.Context, repoPath string, before, after map[string]string) []pushRefChange {
	exclude := collectReachableTips(before)
	seen := make(map[string]struct{}, len(before)+len(after))
	var changes []pushRefChange
	for ref, beforeSHA := range before {
		seen[ref] = struct{}{}
		afterSHA := after[ref]
		change, ok := buildPushRefChange(ctx, repoPath, ref, beforeSHA, afterSHA, exclude)
		if ok {
			changes = append(changes, change)
		}
	}
	for ref, afterSHA := range after {
		if _, ok := seen[ref]; ok {
			continue
		}
		change, ok := buildPushRefChange(ctx, repoPath, ref, "", afterSHA, exclude)
		if ok {
			changes = append(changes, change)
		}
	}
	return changes
}

func collectReachableTips(refs map[string]string) []string {
	seen := make(map[string]struct{}, len(refs))
	tips := make([]string, 0, len(refs))
	for _, sha := range refs {
		if sha == "" || sha == zeroGitSHA {
			continue
		}
		if _, ok := seen[sha]; ok {
			continue
		}
		seen[sha] = struct{}{}
		tips = append(tips, sha)
	}
	return tips
}

func buildPushRefChange(ctx context.Context, repoPath, ref, beforeSHA, afterSHA string, exclude []string) (pushRefChange, bool) {
	if !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/tags/") {
		return pushRefChange{}, false
	}
	if beforeSHA == afterSHA {
		return pushRefChange{}, false
	}
	change := pushRefChange{
		Ref:    ref,
		Before: beforeSHA,
		After:  afterSHA,
	}
	if change.Before == "" {
		change.Before = zeroGitSHA
		change.Created = true
	}
	if change.After == "" {
		change.After = zeroGitSHA
		change.Deleted = true
	}
	if !change.Created && !change.Deleted {
		change.Forced = !gitIsAncestor(ctx, repoPath, change.Before, change.After)
	}
	if change.Created {
		change.Exclude = exclude
	}
	return change, true
}

func gitIsAncestor(ctx context.Context, repoPath, beforeSHA, afterSHA string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "merge-base", "--is-ancestor", beforeSHA, afterSHA)
	return cmd.Run() == nil
}

func pushCommits(ctx context.Context, repoPath, repoFullName, htmlBaseURL string, change pushRefChange) ([]map[string]any, any) {
	if change.Deleted || change.After == zeroGitSHA {
		return []map[string]any{}, nil
	}

	args := []string{"-C", repoPath, "rev-list", "--reverse", "--max-count=20"}
	if change.Before != zeroGitSHA {
		args = append(args, change.Before+".."+change.After)
	} else {
		args = append(args, change.After)
		if len(change.Exclude) > 0 {
			args = append(args, "--not")
			args = append(args, change.Exclude...)
		}
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.Output()
	if err != nil {
		return []map[string]any{}, nil
	}

	var commits []map[string]any
	for _, sha := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		sha = strings.TrimSpace(sha)
		if sha == "" {
			continue
		}
		commit, ok := pushCommitDetails(ctx, repoPath, repoFullName, htmlBaseURL, sha)
		if !ok {
			return []map[string]any{}, nil
		}
		commits = append(commits, commit)
	}
	if len(commits) == 0 {
		return []map[string]any{}, nil
	}
	return commits, commits[len(commits)-1]
}

func pushCommitDetails(ctx context.Context, repoPath, repoFullName, htmlBaseURL, sha string) (map[string]any, bool) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "show", "--format=%H%x00%T%x00%an%x00%ae%x00%cn%x00%ce%x00%aI%x00%s", "--name-status", "--find-renames", "--find-copies", "--no-ext-diff", "--no-color", sha)
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}

	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) == 0 {
		return nil, false
	}
	parts := strings.Split(lines[0], "\x00")
	if len(parts) != 8 {
		return nil, false
	}

	added := make([]string, 0)
	removed := make([]string, 0)
	modified := make([]string, 0)
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		statusParts := strings.Split(line, "\t")
		if len(statusParts) < 2 {
			continue
		}
		switch statusParts[0][0] {
		case 'A', 'C':
			added = append(added, statusParts[len(statusParts)-1])
		case 'D':
			removed = append(removed, statusParts[len(statusParts)-1])
		case 'R':
			if len(statusParts) >= 3 {
				removed = append(removed, statusParts[1])
				added = append(added, statusParts[2])
			}
		default:
			modified = append(modified, statusParts[len(statusParts)-1])
		}
	}

	return map[string]any{
		"id":        parts[0],
		"tree_id":   parts[1],
		"message":   parts[7],
		"timestamp": parts[6],
		"url":       fmt.Sprintf("%s/%s/commit/%s", htmlBaseURL, repoFullName, parts[0]),
		"distinct":  true,
		"author": map[string]any{
			"name":  parts[2],
			"email": parts[3],
		},
		"committer": map[string]any{
			"name":  parts[4],
			"email": parts[5],
		},
		"added":    added,
		"removed":  removed,
		"modified": modified,
	}, true
}
