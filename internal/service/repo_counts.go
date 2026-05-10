package service

import (
	"context"
	"log/slog"

	"gh-server/internal/db"
)

// StarCountBatch returns the number of stars keyed by repository ID. One SQL
// round-trip regardless of how many repo IDs are requested; missing repos
// (and repos with zero stars) are absent from the map, which callers should
// treat as zero.
//
// Use this for list handlers. For a single repo, prefer LoadRepoAggregates,
// which folds star / fork / open-issue counts into one SQL round-trip and
// participates in the per-request cache.
func (s *Service) StarCountBatch(ctx context.Context, repoIDs []uint) map[uint]int {
	return groupCountByRepo(ctx, s, &db.Star{}, repoIDs, "", nil)
}

// ForkCountBatch returns fork counts for many repos in a single round-trip.
// Forks are repositories whose parent_id points back at the target repo, so
// the grouping column differs from the repository_id shared by StarCountBatch
// and CountIssuesByRepoIDBatch.
func (s *Service) ForkCountBatch(ctx context.Context, repoIDs []uint) map[uint]int {
	if len(repoIDs) == 0 {
		return nil
	}
	type row struct {
		ParentID uint
		Count    int
	}
	var rows []row
	if err := s.DBForCtx(ctx).
		Model(&db.Repository{}).
		Select("parent_id, COUNT(*) as count").
		Where("parent_id IN ?", repoIDs).
		Group("parent_id").
		Scan(&rows).Error; err != nil {
		slog.ErrorContext(ctx, "ForkCountBatch", "error", err)
		return nil
	}
	out := make(map[uint]int, len(rows))
	for _, r := range rows {
		out[r.ParentID] = r.Count
	}
	return out
}

// CountIssuesByRepoIDBatch returns open-issue counts for many repos in a
// single round-trip, matching the single-repo CountIssuesByRepoID's filter
// (state = open) exactly.
func (s *Service) CountIssuesByRepoIDBatch(ctx context.Context, repoIDs []uint) map[uint]int {
	return groupCountByRepo(ctx, s, &db.Issue{}, repoIDs, "state = ?", []any{db.StateOpen})
}

// groupCountByRepo is the shared implementation behind batch counters that
// group by repository_id. extraWhere and extraArgs are passed to GORM's
// parameterized Where, not string-concatenated into the SQL — the one caller
// that filters by state gets proper placeholder substitution.
func groupCountByRepo(ctx context.Context, s *Service, model any, repoIDs []uint, extraWhere string, extraArgs []any) map[uint]int {
	if len(repoIDs) == 0 {
		return nil
	}
	type row struct {
		RepositoryID uint
		Count        int
	}
	var rows []row
	q := s.DBForCtx(ctx).
		Model(model).
		Select("repository_id, COUNT(*) as count").
		Where("repository_id IN ?", repoIDs)
	if extraWhere != "" {
		q = q.Where(extraWhere, extraArgs...)
	}
	if err := q.Group("repository_id").Scan(&rows).Error; err != nil {
		slog.ErrorContext(ctx, "groupCountByRepo", "error", err)
		return nil
	}
	out := make(map[uint]int, len(rows))
	for _, r := range rows {
		out[r.RepositoryID] = r.Count
	}
	return out
}
