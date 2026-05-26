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
)

type WikiCompactResult struct {
	PreviousHead    string
	NewHead         string
	CompactedBefore time.Time
	Pages           int
	CommitsRemoved  int
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
	if s.testWikiCompactRefUpdateFailure != nil {
		if err := s.testWikiCompactRefUpdateFailure(repoFullName, commitSHA); err != nil {
			return err
		}
	}
	if s.Git == nil {
		return errors.New("git store unavailable")
	}
	full := wikiRepoFullName(repoFullName)
	return s.Git.UpdateRefSafe(ctx, full, "refs/heads/"+wikiDefaultBranch, commitSHA, true)
}

func (s *Service) repairWikiCatalogFromGit(ctx context.Context, repoFullName string) error {
	_, err := s.MigrateWiki(ctx, repoFullName, WikiMigrationOptions{})
	return err
}
