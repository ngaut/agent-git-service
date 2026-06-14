package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/gitstore"
)

const (
	wikiCompactCommitName  = "gh-server"
	wikiCompactCommitEmail = "gh-server@localhost"
	wikiCompactRefPrefix   = "refs/heads/compacted-"
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

func (s *Service) createWikiCompactCommitObject(ctx context.Context, repoFullName string, committedAt time.Time, livePages []db.WikiPage) (string, error) {
	if s.Git == nil {
		return "", errors.New("git store unavailable")
	}
	if err := s.ensureWikiRepo(ctx, repoFullName); err != nil {
		return "", err
	}
	full := wikiRepoFullName(repoFullName)
	entries := make([]gitstore.CreateTreeEntryInput, 0, len(livePages))
	for _, page := range livePages {
		body, err := s.wikiPageBody(ctx, page)
		if err != nil {
			return "", err
		}
		bodyCopy := string(body)
		entries = append(entries, gitstore.CreateTreeEntryInput{
			Path:    wikiSlugToPath(page.Slug),
			Mode:    "100644",
			Type:    "blob",
			Content: &bodyCopy,
		})
	}
	tree, err := s.Git.CreateTreeObject(ctx, full, gitstore.CreateTreeOptions{Entries: entries})
	if err != nil {
		return "", err
	}
	commit, err := s.Git.CreateCommitObject(ctx, full, gitstore.CreateCommitOptions{
		Message: fmt.Sprintf("Compact wiki history at %s", committedAt.Format(time.RFC3339)),
		TreeSHA: tree.SHA,
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

func wikiCompactProjectionRef(committedAt time.Time) string {
	return wikiCompactRefPrefix + committedAt.UTC().Format("20060102-150405")
}

func (s *Service) updateWikiCompactRefLocked(ctx context.Context, repoFullName, ref, commitSHA string) error {
	if s.testWikiCompactRefUpdateFailure != nil {
		if err := s.testWikiCompactRefUpdateFailure(repoFullName, commitSHA); err != nil {
			return err
		}
	}
	if s.Git == nil {
		return errors.New("git store unavailable")
	}
	full := wikiRepoFullName(repoFullName)
	if _, err := s.Git.RepairRefLock(ctx, full, ref, wikiRefLockStaleAfter, false); err != nil {
		if errors.Is(err, gitstore.ErrRefLockActive) {
			return fmt.Errorf("%w: wiki ref lock for %s is still active", ErrConflict, ref)
		}
		return err
	}
	if _, err := s.Git.LookupRef(ctx, full, ref); err != nil {
		if errors.Is(err, gitstore.ErrRefNotFound) {
			return s.Git.CreateRef(ctx, full, ref, commitSHA)
		}
		return err
	}
	return s.Git.UpdateRefSafe(ctx, full, ref, commitSHA, true)
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
