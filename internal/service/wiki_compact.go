package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gh-server/internal/gitstore"
)

const (
	wikiCompactCommitName  = "gh-server"
	wikiCompactCommitEmail = "gh-server@localhost"
	wikiRefLockStaleAfter  = 5 * time.Minute
)

type WikiCompactResult struct {
	PreviousHead    string
	NewHead         string
	CompactedBefore time.Time
	Pages           int
	CommitsRemoved  int
}

type WikiRefLockRepairResult struct {
	Ref        string
	LockPath   string
	Present    bool
	Cleared    bool
	Force      bool
	AgeSeconds int64
}

func (s *Service) createWikiCompactCommitObject(ctx context.Context, repoFullName string, committedAt time.Time) (string, error) {
	if s.Git == nil {
		return "", errors.New("git store unavailable")
	}
	if err := s.ensureWikiRepo(ctx, repoFullName); err != nil {
		return "", err
	}
	full := wikiRepoFullName(repoFullName)
	headSHA, err := s.Git.HeadSHA(ctx, full, wikiDefaultBranch)
	if err != nil {
		return "", err
	}
	headCommit, err := s.Git.GetGitCommitObject(ctx, full, headSHA)
	if err != nil {
		return "", err
	}
	commit, err := s.Git.CreateCommitObject(ctx, full, gitstore.CreateCommitOptions{
		Message: fmt.Sprintf("Compact wiki history at %s", committedAt.Format(time.RFC3339)),
		TreeSHA: headCommit.TreeSHA,
		Author: gitstore.GitSignature{
			Name:  wikiCompactCommitName,
			Email: wikiCompactCommitEmail,
			Date:  committedAt.Format(time.RFC3339),
		},
		Committer: gitstore.GitSignature{
			Name:  wikiCompactCommitName,
			Email: wikiCompactCommitEmail,
			Date:  committedAt.Format(time.RFC3339),
		},
	})
	if err != nil {
		return "", err
	}
	return commit.SHA, nil
}

func (s *Service) updateWikiCompactRef(ctx context.Context, repoFullName, commitSHA string) error {
	if s.Git == nil {
		return errors.New("git store unavailable")
	}
	full := wikiRepoFullName(repoFullName)
	return s.Git.WithRepoLock(ctx, full, func() error {
		return s.updateWikiCompactRefLocked(ctx, repoFullName, commitSHA)
	})
}

func (s *Service) updateWikiCompactRefLocked(ctx context.Context, repoFullName, commitSHA string) error {
	if s.testWikiCompactRefUpdateFailure != nil {
		if err := s.testWikiCompactRefUpdateFailure(repoFullName, commitSHA); err != nil {
			return err
		}
	}
	if s.Git == nil {
		return errors.New("git store unavailable")
	}
	full := wikiRepoFullName(repoFullName)
	ref := "refs/heads/" + wikiDefaultBranch
	if _, err := s.Git.RepairRefLock(ctx, full, ref, wikiRefLockStaleAfter, false); err != nil {
		if errors.Is(err, gitstore.ErrRefLockActive) {
			return fmt.Errorf("%w: wiki ref lock for %s is still active", ErrConflict, ref)
		}
		return err
	}
	return s.Git.UpdateRefSafe(ctx, full, ref, commitSHA, true)
}

func (s *Service) repairWikiCatalogFromGit(ctx context.Context, repoFullName string) error {
	_, err := s.MigrateWiki(ctx, repoFullName, WikiMigrationOptions{})
	return err
}

func (s *Service) RepairWikiRefLocks(ctx context.Context, repoFullName string, force bool) (WikiRefLockRepairResult, error) {
	if s.Git == nil {
		return WikiRefLockRepairResult{}, errors.New("git store unavailable")
	}
	if err := s.ensureWikiRepo(ctx, repoFullName); err != nil {
		return WikiRefLockRepairResult{}, err
	}
	full := wikiRepoFullName(repoFullName)
	ref := "refs/heads/" + wikiDefaultBranch
	var result gitstore.RefLockRepairResult
	err := s.Git.WithRepoLock(ctx, full, func() error {
		var repairErr error
		result, repairErr = s.Git.RepairRefLock(ctx, full, ref, wikiRefLockStaleAfter, force)
		if repairErr != nil && errors.Is(repairErr, gitstore.ErrRefLockActive) {
			return fmt.Errorf("%w: wiki ref lock for %s is still active", ErrConflict, ref)
		}
		return repairErr
	})
	if err != nil {
		return WikiRefLockRepairResult{}, err
	}
	return WikiRefLockRepairResult(result), nil
}
