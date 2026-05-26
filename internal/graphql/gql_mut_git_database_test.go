package graphql_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

func TestGraphQL_CreateBlobAndCreateTree(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	repo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    h.User.Login,
		Name:          "gql-git-db",
		DefaultBranch: "main",
		AutoInit:      true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	blobQuery := `
		mutation($input: CreateGitBlobInput!) {
			createBlob(input: $input) {
				blob {
					oid
					byteSize
					isBinary
				}
			}
		}
	`
	blobData := []byte{0x00, 0x01, 0xff, 0x7f}
	repoNodeID := fmt.Sprintf("Repository_%d", repo.ID)
	blobRes := h.DoGraphQL(t, blobQuery, map[string]any{
		"input": map[string]any{
			"repositoryId": repoNodeID,
			"content":      base64.StdEncoding.EncodeToString(blobData),
			"encoding":     "BASE64",
		},
	})

	blob := blobRes["createBlob"].(map[string]any)["blob"].(map[string]any)
	blobOID, _ := blob["oid"].(string)
	if len(blobOID) != 40 {
		t.Fatalf("blob oid: got %v", blob["oid"])
	}
	if blob["byteSize"] != float64(len(blobData)) {
		t.Fatalf("blob byteSize: got %v want %d", blob["byteSize"], len(blobData))
	}
	if blob["isBinary"] != true {
		t.Fatalf("blob isBinary: got %v want true", blob["isBinary"])
	}

	treeQuery := `
		mutation($input: CreateGitTreeInput!) {
			createTree(input: $input) {
				tree {
					oid
					entries {
						path
						mode
						type
						oid
					}
				}
			}
		}
	`
	treeRes := h.DoGraphQL(t, treeQuery, map[string]any{
		"input": map[string]any{
			"repositoryId": repoNodeID,
			"entries": []map[string]any{
				{
					"path":    "data.bin",
					"mode":    "100644",
					"type":    "BLOB",
					"content": "tree-inline-content",
				},
			},
		},
	})

	tree := treeRes["createTree"].(map[string]any)["tree"].(map[string]any)
	treeOID, _ := tree["oid"].(string)
	if len(treeOID) != 40 {
		t.Fatalf("tree oid: got %v", tree["oid"])
	}
	entries := tree["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("tree entries: got %d want 1", len(entries))
	}
	entry := entries[0].(map[string]any)
	if entry["path"] != "data.bin" {
		t.Fatalf("entry path: got %v", entry["path"])
	}
	if entry["type"] != "BLOB" {
		t.Fatalf("entry type: got %v", entry["type"])
	}

	headSHA, err := h.Svc.Git.HeadSHA(ctx, repo.FullName, "main")
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/gql-git-db/git/commits", map[string]any{
		"message": "commit graph tree",
		"tree":    treeOID,
		"parents": []string{headSHA},
	})
	if w.Code != 201 {
		t.Fatalf("create commit status: got %d body=%s", w.Code, w.Body.String())
	}
}

func TestGraphQL_CreateTreeRejectsMutuallyExclusiveObjectIDs(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	repo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    h.User.Login,
		Name:          "gql-git-db-invalid",
		DefaultBranch: "main",
		AutoInit:      true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	repoNodeID := fmt.Sprintf("Repository_%d", repo.ID)

	query := `
		mutation($input: CreateGitTreeInput!) {
			createTree(input: $input) {
				tree { oid }
			}
		}
	`

	res := doRawGql(t, h.Mux, query, map[string]any{
		"input": map[string]any{
			"repositoryId": repoNodeID,
			"entries": []map[string]any{
				{
					"path":    "file.txt",
					"mode":    "100644",
					"type":    "BLOB",
					"sha":     "1111111111111111111111111111111111111111",
					"oid":     "2222222222222222222222222222222222222222",
					"content": "hello",
				},
			},
		},
	})

	errs, ok := res["errors"].([]any)
	if !ok || len(errs) == 0 {
		t.Fatalf("expected graphql errors, got %#v", res)
	}
	msg := errs[0].(map[string]any)["message"]
	if msg != "provide either sha or oid, not both" {
		t.Fatalf("error message: got %v", msg)
	}
}
