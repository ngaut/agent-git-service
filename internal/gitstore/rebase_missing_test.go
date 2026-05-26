package gitstore_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/gitstore"
)

func TestStore_RebaseMissingBranchReturnsErrorAndDoesNotAdvanceBase(t *testing.T) {
	cases := []struct {
		name    string
		base    string
		head    string
		errHint string
	}{
		{name: "missing-head", base: "main", head: "no-such-head", errHint: "checkout"},
		{name: "missing-base", base: "no-such-base", head: "feature", errHint: "rebase"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := gitstore.NewTestStore(t)
			defer cleanup()

			ctx := context.Background()
			repoName := "user/rebase-" + tc.name

			if err := store.Init(ctx, repoName, "main", true); err != nil {
				t.Fatalf("Init failed: %v", err)
			}
			if _, err := store.WriteFile(ctx, repoName, "main", "main.txt", "base commit", []byte("base\n")); err != nil {
				t.Fatalf("WriteFile main failed: %v", err)
			}
			if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
				t.Fatalf("CreateBranch failed: %v", err)
			}
			if _, err := store.WriteFile(ctx, repoName, "feature", "feature.txt", "feature commit", []byte("feature\n")); err != nil {
				t.Fatalf("WriteFile feature failed: %v", err)
			}

			mainBefore, err := store.HeadSHA(ctx, repoName, "main")
			if err != nil {
				t.Fatalf("HeadSHA main before rebase failed: %v", err)
			}
			featureBefore, err := store.HeadSHA(ctx, repoName, "feature")
			if err != nil {
				t.Fatalf("HeadSHA feature before rebase failed: %v", err)
			}

			_, rebaseErr := store.Rebase(ctx, gitstore.RebaseOptions{
				FullName:   repoName,
				BaseBranch: tc.base,
				HeadBranch: tc.head,
				Committer:  "Test Bot",
				Email:      "bot@example.com",
			})
			if rebaseErr == nil {
				t.Fatal("expected rebase error for missing branch, got nil")
			}
			if !strings.Contains(strings.ToLower(rebaseErr.Error()), tc.errHint) {
				t.Fatalf("expected error to mention %q, got: %v", tc.errHint, rebaseErr)
			}

			mainAfter, err := store.HeadSHA(ctx, repoName, "main")
			if err != nil {
				t.Fatalf("HeadSHA main after rebase failed: %v", err)
			}
			if mainAfter != mainBefore {
				t.Fatalf("expected main HEAD to remain %s after failed rebase, got %s", mainBefore, mainAfter)
			}

			if tc.head == "feature" {
				featureAfter, err := store.HeadSHA(ctx, repoName, "feature")
				if err != nil {
					t.Fatalf("HeadSHA feature after rebase failed: %v", err)
				}
				if featureAfter != featureBefore {
					t.Fatalf("expected feature HEAD to remain %s after failed rebase, got %s", featureBefore, featureAfter)
				}
			}
		})
	}
}
