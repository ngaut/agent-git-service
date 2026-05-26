package gitstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ngaut/agent-git-service/internal/gitstore"
)

func TestStore_CreateTreeObjectDeleteSHA(t *testing.T) {
	store, cleanup := gitstore.NewTestStore(t)
	defer cleanup()

	ctx := context.Background()
	repoName := "user/repo-tree-delete"
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if _, err := store.WriteFile(ctx, repoName, "main", "docs/guide.txt", "add docs", []byte("docs\n")); err != nil {
		t.Fatalf("WriteFile docs failed: %v", err)
	}

	headSHA, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA failed: %v", err)
	}
	commit, err := store.GetGitCommitObject(ctx, repoName, headSHA)
	if err != nil {
		t.Fatalf("GetGitCommitObject failed: %v", err)
	}

	cases := []struct {
		name         string
		deletePath   string
		deleteMode   string
		deleteType   string
		wantRemoved  string
		wantRetained string
	}{
		{
			name:         "blob leaf",
			deletePath:   "README.md",
			deleteMode:   "100644",
			deleteType:   "blob",
			wantRemoved:  "README.md",
			wantRetained: "docs",
		},
		{
			name:         "subtree",
			deletePath:   "docs",
			deleteMode:   "040000",
			deleteType:   "tree",
			wantRemoved:  "docs",
			wantRetained: "README.md",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tree, err := store.CreateTreeObject(ctx, repoName, gitstore.CreateTreeOptions{
				BaseTree: commit.TreeSHA,
				Entries: []gitstore.CreateTreeEntryInput{
					{Path: tc.deletePath, Mode: tc.deleteMode, Type: tc.deleteType, DeleteSHA: true},
				},
			})
			if err != nil {
				t.Fatalf("CreateTreeObject delete failed: %v", err)
			}

			var removedPresent, retainedPresent bool
			for _, entry := range tree.Entries {
				switch entry.Path {
				case tc.wantRemoved:
					removedPresent = true
				case tc.wantRetained:
					retainedPresent = true
				}
			}
			if removedPresent {
				t.Fatalf("deleted path %s still present: %#v", tc.wantRemoved, tree.Entries)
			}
			if !retainedPresent {
				t.Fatalf("delete removed unrelated entries: %#v", tree.Entries)
			}
		})
	}
}

func TestStore_CreateTreeObjectDeleteSHARequiresBaseTree(t *testing.T) {
	store, cleanup := gitstore.NewTestStore(t)
	defer cleanup()

	ctx := context.Background()
	repoName := "user/repo-tree-delete-no-base"
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	_, err := store.CreateTreeObject(ctx, repoName, gitstore.CreateTreeOptions{
		Entries: []gitstore.CreateTreeEntryInput{
			{Path: "README.md", Mode: "100644", Type: "blob", DeleteSHA: true},
		},
	})
	if err == nil {
		t.Fatal("expected error for delete without base_tree")
	}
	if !errors.Is(err, gitstore.ErrInvalidTreeEntry) {
		t.Fatalf("expected ErrInvalidTreeEntry, got %v", err)
	}
}
