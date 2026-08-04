package service

import (
	"context"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

func TestWikiProjectionSnapshotMatchesGit(t *testing.T) {
	ctx := context.Background()
	git, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("New git store: %v", err)
	}
	repo := db.Repository{FullName: "owner/projection-snapshot"}
	wikiFullName := wikiRepoFullName(repo.FullName)
	if err := git.Init(ctx, wikiFullName, wikiDefaultBranch, false); err != nil {
		t.Fatalf("init wiki repository: %v", err)
	}
	svc := &Service{}

	matches, err := svc.wikiProjectionSnapshotMatchesGit(
		ctx,
		git,
		repo,
		wikicatalog.HeadProjectionState{},
	)
	if err != nil || !matches {
		t.Fatalf("empty catalog and Git match = %v, err = %v, want true", matches, err)
	}
	matches, err = svc.wikiProjectionSnapshotMatchesGit(
		ctx,
		git,
		repo,
		wikicatalog.HeadProjectionState{GitRepairObligationExists: true},
	)
	if err != nil || matches {
		t.Fatalf("empty catalog with repair obligation match = %v, err = %v, want false", matches, err)
	}
	matches, err = svc.wikiProjectionSnapshotMatchesGit(
		ctx,
		git,
		repo,
		wikicatalog.HeadProjectionState{
			Exists:             true,
			ChangesetID:        1,
			CommitSHA:          "1111111111111111111111111111111111111111",
			Source:             wikicatalog.SourceREST,
			SynthFormatVersion: synthProjectionMaterialized,
		},
	)
	if err != nil || matches {
		t.Fatalf("catalog head without Git ref match = %v, err = %v, want false", matches, err)
	}

	commitSHA, err := git.CommitFiles(ctx, wikiFullName, wikiDefaultBranch, "seed", []gitstore.FileMutation{{
		Path:    "home.md",
		Content: []byte("# Home\n"),
	}})
	if err != nil {
		t.Fatalf("seed wiki commit: %v", err)
	}

	tests := []struct {
		name  string
		state wikicatalog.HeadProjectionState
		want  bool
	}{
		{
			name: "matching materialized head",
			state: wikicatalog.HeadProjectionState{
				Exists:             true,
				ChangesetID:        1,
				CommitSHA:          commitSHA,
				Source:             wikicatalog.SourceREST,
				SynthFormatVersion: synthProjectionMaterialized,
			},
			want: true,
		},
		{
			name: "matching head with repair obligation",
			state: wikicatalog.HeadProjectionState{
				Exists:                    true,
				ChangesetID:               1,
				CommitSHA:                 commitSHA,
				Source:                    wikicatalog.SourceREST,
				SynthFormatVersion:        synthProjectionMaterialized,
				GitRepairObligationExists: true,
			},
			want: false,
		},
		{
			name:  "git ahead of empty catalog",
			state: wikicatalog.HeadProjectionState{},
			want:  false,
		},
		{
			name: "pending current projection",
			state: wikicatalog.HeadProjectionState{
				Exists:             true,
				ChangesetID:        1,
				CommitSHA:          commitSHA,
				Source:             wikicatalog.SourceREST,
				SynthFormatVersion: synthProjectionPending,
			},
			want: false,
		},
		{
			name: "older pending projection",
			state: wikicatalog.HeadProjectionState{
				Exists:                 true,
				ChangesetID:            2,
				CommitSHA:              commitSHA,
				Source:                 wikicatalog.SourceREST,
				SynthFormatVersion:     synthProjectionMaterialized,
				PendingProjectionCount: 1,
			},
			want: false,
		},
		{
			name: "mismatched heads",
			state: wikicatalog.HeadProjectionState{
				Exists:             true,
				ChangesetID:        1,
				CommitSHA:          "1111111111111111111111111111111111111111",
				Source:             wikicatalog.SourceREST,
				SynthFormatVersion: synthProjectionMaterialized,
			},
			want: false,
		},
		{
			name: "materialized compaction",
			state: wikicatalog.HeadProjectionState{
				Exists:             true,
				ChangesetID:        1,
				CommitSHA:          "2222222222222222222222222222222222222222",
				Source:             wikicatalog.SourceCompact,
				SynthFormatVersion: synthProjectionMaterialized,
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, matchErr := svc.wikiProjectionSnapshotMatchesGit(ctx, git, repo, tc.state)
			if matchErr != nil {
				t.Fatalf("wikiProjectionSnapshotMatchesGit: %v", matchErr)
			}
			if got != tc.want {
				t.Fatalf("match = %v, want %v", got, tc.want)
			}
		})
	}
}
