package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gh-server/internal/db"
)

type SetPRAutoMergeInput struct {
	Enabled         bool
	MergeMethod     string
	CommitHeadline  string
	CommitBody      string
	AuthorEmail     string
	ExpectedHeadSHA string
}

type branchProtectionRequiredStatusChecks struct {
	Strict   bool                         `json:"strict"`
	Contexts []string                     `json:"contexts"`
	Checks   []branchProtectionCheckEntry `json:"checks"`
}

type branchProtectionCheckEntry struct {
	Context string `json:"context"`
	Name    string `json:"name"`
}

type branchProtectionRequiredReviews struct {
	RequiredApprovingReviewCount int                              `json:"required_approving_review_count"`
	BypassPullRequestAllowances  branchProtectionBypassAllowances `json:"bypass_pull_request_allowances"`
}

type branchProtectionBypassAllowances struct {
	Users []string `json:"users"`
	Teams []string `json:"teams"`
	Apps  []string `json:"apps"`
}

type mergeStatusState struct {
	Passed    bool
	Completed bool
	UpdatedAt time.Time
}

func (s *Service) SetPRAutoMerge(ctx context.Context, prID uint, in SetPRAutoMergeInput) (db.PullRequest, error) {
	currentUser, err := s.GetCurrentUser(ctx)
	if err != nil {
		return db.PullRequest{}, fmt.Errorf("authentication required for auto-merge: %w", err)
	}

	pr, err := s.GetPRByID(ctx, prID)
	if err != nil {
		return db.PullRequest{}, err
	}
	if pr.Merged || pr.State != db.StateOpen {
		return db.PullRequest{}, fmt.Errorf("%w: pull request is not open", ErrInvalidState)
	}

	canMerge, err := s.CanCreatePR(ctx, pr.RepositoryID, currentUser.ID)
	if err != nil {
		return db.PullRequest{}, err
	}
	if !canMerge {
		return db.PullRequest{}, fmt.Errorf("%w: auto-merge requires repository write access", ErrForbidden)
	}

	updates := map[string]any{
		"auto_merge":                   in.Enabled,
		"auto_merge_method":            "",
		"auto_merge_commit_headline":   "",
		"auto_merge_commit_body":       "",
		"auto_merge_author_email":      "",
		"auto_merge_expected_head_sha": "",
		"auto_merge_enabled_by_login":  "",
	}

	if in.Enabled {
		if !pr.Repository.AllowAutoMerge {
			return db.PullRequest{}, fmt.Errorf("%w: auto-merge is not allowed for this repository", ErrInvalidState)
		}
		if expected := strings.TrimSpace(in.ExpectedHeadSHA); expected != "" && !strings.EqualFold(expected, pr.HeadSHA) {
			return db.PullRequest{}, fmt.Errorf("%w: expected head oid does not match the current pull request head", ErrInvalidState)
		}

		mergeMethod := strings.ToUpper(strings.TrimSpace(in.MergeMethod))
		if mergeMethod == "" {
			mergeMethod = "MERGE"
		}
		updates["auto_merge_method"] = mergeMethod
		updates["auto_merge_commit_headline"] = strings.TrimSpace(in.CommitHeadline)
		updates["auto_merge_commit_body"] = strings.TrimSpace(in.CommitBody)
		updates["auto_merge_author_email"] = strings.TrimSpace(in.AuthorEmail)
		updates["auto_merge_expected_head_sha"] = strings.TrimSpace(in.ExpectedHeadSHA)
		updates["auto_merge_enabled_by_login"] = currentUser.Login
	}

	if err := s.UpdatePRFields(ctx, prID, updates); err != nil {
		return db.PullRequest{}, err
	}
	return s.GetPRByID(ctx, prID)
}

func (s *Service) enforceMergePolicy(ctx context.Context, currentUser db.User, pr *db.PullRequest) error {
	if pr == nil {
		return fmt.Errorf("%w: pull request is required", ErrValidation)
	}

	canMerge, err := s.CanCreatePR(ctx, pr.RepositoryID, currentUser.ID)
	if err != nil {
		return err
	}
	if !canMerge {
		return fmt.Errorf("%w: merge requires repository write access", ErrForbidden)
	}

	bp, err := s.GetBranchProtection(ctx, pr.RepositoryID, pr.BaseRef)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	return s.enforceBranchProtectionForMerge(ctx, currentUser, *pr, *bp)
}

func (s *Service) enforceBranchProtectionForMerge(ctx context.Context, currentUser db.User, pr db.PullRequest, bp db.BranchProtection) error {
	requiredReviews, err := decodeBranchProtectionRequiredReviews(bp.RequiredPullRequestJSON)
	if err != nil {
		return fmt.Errorf("%w: invalid required pull request reviews configuration", ErrInvalidState)
	}
	if !bp.EnforceAdmins && currentUser.SiteAdmin {
		return nil
	}

	bypassReviews := isBranchProtectionBypassUser(currentUser.Login, requiredReviews.BypassPullRequestAllowances.Users)
	if requiredReviews.RequiredApprovingReviewCount > 0 && !bypassReviews {
		approvals, changesRequested, err := s.currentPRReviewState(ctx, pr.ID)
		if err != nil {
			return err
		}
		if changesRequested {
			return fmt.Errorf("%w: branch protection blocks merge while changes are requested", ErrInvalidState)
		}
		if approvals < requiredReviews.RequiredApprovingReviewCount {
			return fmt.Errorf("%w: branch protection requires %d approving review(s)", ErrInvalidState, requiredReviews.RequiredApprovingReviewCount)
		}
	}

	requiredChecks, err := decodeBranchProtectionRequiredStatusChecks(bp.RequiredStatusChecksJSON)
	if err != nil {
		return fmt.Errorf("%w: invalid required status checks configuration", ErrInvalidState)
	}
	if requiredChecks.Strict {
		return fmt.Errorf("%w: strict required status checks are not supported", ErrInvalidState)
	}
	if err := s.enforceRequiredStatusChecks(ctx, pr, requiredChecks); err != nil {
		return err
	}
	return nil
}

func decodeBranchProtectionRequiredReviews(raw string) (branchProtectionRequiredReviews, error) {
	var parsed branchProtectionRequiredReviews
	if strings.TrimSpace(raw) == "" {
		return parsed, nil
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return parsed, err
	}
	return parsed, nil
}

func decodeBranchProtectionRequiredStatusChecks(raw string) (branchProtectionRequiredStatusChecks, error) {
	var parsed branchProtectionRequiredStatusChecks
	if strings.TrimSpace(raw) == "" {
		return parsed, nil
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return parsed, err
	}
	return parsed, nil
}

func isBranchProtectionBypassUser(login string, users []string) bool {
	for _, candidate := range users {
		if strings.EqualFold(strings.TrimSpace(candidate), login) {
			return true
		}
	}
	return false
}

func (s *Service) currentPRReviewState(ctx context.Context, prID uint) (approvals int, changesRequested bool, err error) {
	var reviews []db.PullRequestReview
	err = s.DBForCtx(ctx).
		Where("pull_request_id = ?", prID).
		Order("created_at asc").
		Find(&reviews).Error
	if err != nil {
		return 0, false, err
	}

	latestByAuthor := make(map[string]db.PullRequestReview)
	for _, review := range reviews {
		if review.AuthorLogin == "" {
			continue
		}
		latestByAuthor[review.AuthorLogin] = review
	}
	for _, review := range latestByAuthor {
		switch review.State {
		case db.ReviewApproved:
			approvals++
		case db.ReviewChangesRequested:
			changesRequested = true
		}
	}
	return approvals, changesRequested, nil
}

func (s *Service) enforceRequiredStatusChecks(ctx context.Context, pr db.PullRequest, required branchProtectionRequiredStatusChecks) error {
	contexts := collectRequiredStatusCheckContexts(required)
	if len(contexts) == 0 {
		return nil
	}

	statuses, err := s.latestStatusChecksForPR(ctx, pr)
	if err != nil {
		return err
	}
	for _, contextName := range contexts {
		state, ok := statuses[contextName]
		if !ok || !state.Completed || !state.Passed {
			return fmt.Errorf("%w: required status checks have not passed", ErrInvalidState)
		}
	}
	return nil
}

func collectRequiredStatusCheckContexts(required branchProtectionRequiredStatusChecks) []string {
	seen := make(map[string]struct{})
	var contexts []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		contexts = append(contexts, name)
	}
	for _, name := range required.Contexts {
		add(name)
	}
	for _, check := range required.Checks {
		if check.Context != "" {
			add(check.Context)
			continue
		}
		add(check.Name)
	}
	return contexts
}

func (s *Service) latestStatusChecksForPR(ctx context.Context, pr db.PullRequest) (map[string]mergeStatusState, error) {
	states := make(map[string]mergeStatusState)

	runs, err := s.ListWorkflowRunsBySHA(ctx, pr.RepositoryID, pr.HeadSHA)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		name := strings.TrimSpace(run.Name)
		if name == "" {
			continue
		}
		state := mergeStatusState{
			Passed:    run.Status == db.RunCompleted && run.Conclusion == db.ConclusionSuccess,
			Completed: run.Status == db.RunCompleted,
			UpdatedAt: run.UpdatedAt,
		}
		recordLatestStatus(states, name, state)
	}

	commitStatuses, err := s.ListCommitStatuses(ctx, pr.RepositoryID, pr.HeadSHA)
	if err != nil {
		return nil, err
	}
	for _, status := range commitStatuses {
		name := strings.TrimSpace(status.Context)
		if name == "" {
			continue
		}
		state := mergeStatusState{
			Passed:    status.State == "success",
			Completed: status.State != "pending",
			UpdatedAt: status.UpdatedAt,
		}
		recordLatestStatus(states, name, state)
	}
	return states, nil
}

func recordLatestStatus(states map[string]mergeStatusState, name string, candidate mergeStatusState) {
	if existing, ok := states[name]; ok && !candidate.UpdatedAt.After(existing.UpdatedAt) {
		return
	}
	states[name] = candidate
}

func (s *Service) ReevaluateAutoMergeForSHA(ctx context.Context, repoID uint, sha string) {
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return
	}

	var prs []db.PullRequest
	if err := preloadPRFull(s.DBForCtx(ctx)).
		Where("repository_id = ? AND head_sha = ? AND auto_merge = ? AND merged = false AND state = ?", repoID, sha, true, db.StateOpen).
		Find(&prs).Error; err != nil {
		slog.WarnContext(ctx, "auto-merge candidate lookup failed", "repo_id", repoID, "head_sha", sha, "error", err)
		return
	}
	for i := range prs {
		if err := s.tryAutoMergePR(ctx, &prs[i]); err != nil &&
			!errors.Is(err, ErrInvalidState) &&
			!errors.Is(err, ErrForbidden) &&
			!errors.Is(err, ErrConflict) {
			slog.WarnContext(ctx, "auto-merge evaluation failed", "pr_id", prs[i].ID, "pr_number", prs[i].Number, "error", err)
		}
	}
}

func (s *Service) tryAutoMergePR(ctx context.Context, pr *db.PullRequest) error {
	if pr == nil || !pr.AutoMerge || pr.Merged || pr.State != db.StateOpen {
		return nil
	}
	if !pr.Repository.AllowAutoMerge {
		return fmt.Errorf("%w: auto-merge is not allowed for this repository", ErrInvalidState)
	}
	if expected := strings.TrimSpace(pr.AutoMergeExpectedHeadSHA); expected != "" && !strings.EqualFold(expected, pr.HeadSHA) {
		return fmt.Errorf("%w: expected head oid does not match the current pull request head", ErrInvalidState)
	}

	actor, err := s.autoMergeActor(ctx, *pr)
	if err != nil {
		return err
	}
	if err := s.enforceBranchProtectionAndAutoMergePolicy(ctx, actor, *pr); err != nil {
		return err
	}

	commitMsg := strings.TrimSpace(pr.AutoMergeCommitHeadline)
	if body := strings.TrimSpace(string(pr.AutoMergeCommitBody)); body != "" {
		if commitMsg != "" {
			commitMsg += "\n\n" + body
		} else {
			commitMsg = body
		}
	}
	return s.mergePRRecordWithActor(ctx, actor, pr, strings.ToLower(pr.AutoMergeMethod), commitMsg)
}

func (s *Service) enforceBranchProtectionAndAutoMergePolicy(ctx context.Context, actor db.User, pr db.PullRequest) error {
	if err := s.enforceMergePolicy(ctx, actor, &pr); err != nil {
		return err
	}
	return nil
}

func (s *Service) autoMergeActor(ctx context.Context, pr db.PullRequest) (db.User, error) {
	login := strings.TrimSpace(pr.AutoMergeEnabledByLogin)
	if login == "" {
		login = pr.Author.Login
	}
	if login == "" {
		return db.User{}, fmt.Errorf("%w: auto-merge actor is not available", ErrInvalidState)
	}
	actor, err := s.GetUser(ctx, login)
	if err != nil {
		return db.User{}, err
	}
	if email := strings.TrimSpace(pr.AutoMergeAuthorEmail); email != "" {
		actor.Email = email
	}
	return actor, nil
}
