package rest_test

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/openpgp"
	"golang.org/x/crypto/openpgp/armor"

	"gh-server/internal/db"
	"gh-server/internal/service"
	"gh-server/internal/testharness"
)

func createGitRepo(t *testing.T, h *testharness.Harness, name, defaultBranch string, autoInit bool) string {
	t.Helper()
	ctx := context.Background()
	repo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    h.User.Login,
		Name:          name,
		DefaultBranch: defaultBranch,
		AutoInit:      autoInit,
	})
	if err != nil {
		t.Fatalf("create repo %s: %v", name, err)
	}
	return repo.FullName
}

func assertContentFileShape(t *testing.T, item map[string]any, name, path, ref string, includesContent bool) {
	t.Helper()
	if item["type"] != "file" {
		t.Fatalf("content type: got %v, want file", item["type"])
	}
	if item["name"] != name {
		t.Fatalf("content name: got %v, want %s", item["name"], name)
	}
	if item["path"] != path {
		t.Fatalf("content path: got %v, want %s", item["path"], path)
	}
	sha := requireContentStringField(t, item, "sha")
	requireContentURLContains(t, item, "url", "/api/v3/repos/testuser/", "/contents/"+path+"?ref="+ref)
	requireContentURLContains(t, item, "git_url", "/api/v3/repos/testuser/", "/git/blobs/"+sha)
	requireContentURLContains(t, item, "html_url", "/testuser/", "/blob/"+ref+"/"+path)
	requireContentURLContains(t, item, "download_url", "/testuser/", "/raw/"+ref+"/"+path)
	assertContentLinks(t, item)
	if includesContent {
		if item["encoding"] != "base64" {
			t.Fatalf("content encoding: got %v, want base64", item["encoding"])
		}
		requireContentStringField(t, item, "content")
	} else if _, ok := item["content"]; ok {
		t.Fatalf("content field should be omitted from metadata response")
	}
}

func assertContentDirShape(t *testing.T, item map[string]any, name, path, ref string) {
	t.Helper()
	if item["type"] != "dir" {
		t.Fatalf("content type: got %v, want dir", item["type"])
	}
	if item["name"] != name {
		t.Fatalf("content name: got %v, want %s", item["name"], name)
	}
	if item["path"] != path {
		t.Fatalf("content path: got %v, want %s", item["path"], path)
	}
	sha := requireContentStringField(t, item, "sha")
	requireContentURLContains(t, item, "url", "/api/v3/repos/testuser/", "/contents/"+path+"?ref="+ref)
	requireContentURLContains(t, item, "git_url", "/api/v3/repos/testuser/", "/git/trees/"+sha)
	requireContentURLContains(t, item, "html_url", "/testuser/", "/tree/"+ref+"/"+path)
	if item["download_url"] != nil {
		t.Fatalf("directory download_url: got %v, want nil", item["download_url"])
	}
	assertContentLinks(t, item)
}

func assertContentLinks(t *testing.T, item map[string]any) {
	t.Helper()
	links, ok := item["_links"].(map[string]any)
	if !ok {
		t.Fatalf("_links: expected object, got %T", item["_links"])
	}
	if links["self"] != item["url"] {
		t.Fatalf("_links.self: got %v, want %v", links["self"], item["url"])
	}
	if links["git"] != item["git_url"] {
		t.Fatalf("_links.git: got %v, want %v", links["git"], item["git_url"])
	}
	if links["html"] != item["html_url"] {
		t.Fatalf("_links.html: got %v, want %v", links["html"], item["html_url"])
	}
}

func requireContentStringField(t *testing.T, item map[string]any, field string) string {
	t.Helper()
	value, ok := item[field].(string)
	if !ok || value == "" {
		t.Fatalf("%s: got %v, want non-empty string", field, item[field])
	}
	return value
}

func requireContentURLContains(t *testing.T, item map[string]any, field string, parts ...string) {
	t.Helper()
	value := requireContentStringField(t, item, field)
	for _, part := range parts {
		if !strings.Contains(value, part) {
			t.Fatalf("%s: got %q, want to contain %q", field, value, part)
		}
	}
}

func createOrgGitRepo(t *testing.T, h *testharness.Harness, ownerLogin, name, defaultBranch string, autoInit bool) string {
	t.Helper()
	ctx := context.Background()
	repo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    ownerLogin,
		Name:          name,
		DefaultBranch: defaultBranch,
		AutoInit:      autoInit,
	})
	if err != nil {
		t.Fatalf("create org repo %s/%s: %v", ownerLogin, name, err)
	}
	return repo.FullName
}

func buildSignedCommitPayload(t *testing.T, treeSHA string, parents []string, authorName, authorEmail, authorDate, committerName, committerEmail, committerDate, message string) string {
	t.Helper()

	formatIdentity := func(name, email, date string) string {
		parsed, err := time.Parse(time.RFC3339, date)
		if err != nil {
			t.Fatalf("parse commit date %q: %v", date, err)
		}
		return name + " <" + email + "> " + strconv.FormatInt(parsed.Unix(), 10) + " " + parsed.Format("-0700")
	}

	var b strings.Builder
	b.WriteString("tree ")
	b.WriteString(treeSHA)
	b.WriteByte('\n')
	for _, parent := range parents {
		b.WriteString("parent ")
		b.WriteString(parent)
		b.WriteByte('\n')
	}
	b.WriteString("author ")
	b.WriteString(formatIdentity(authorName, authorEmail, authorDate))
	b.WriteByte('\n')
	b.WriteString("committer ")
	b.WriteString(formatIdentity(committerName, committerEmail, committerDate))
	b.WriteString("\n\n")
	b.WriteString(message)
	return b.String()
}

func createTestGPGIdentity(t *testing.T, name, email string) (*openpgp.Entity, string) {
	t.Helper()

	entity, err := openpgp.NewEntity(name, "", email, nil)
	if err != nil {
		t.Fatalf("new gpg entity: %v", err)
	}

	return entity, armoredPublicKeyFromEntity(t, entity)
}

func armoredPublicKeyFromEntity(t *testing.T, entity *openpgp.Entity) string {
	t.Helper()

	var buf bytes.Buffer
	w, err := armor.Encode(&buf, "PGP PUBLIC KEY BLOCK", nil)
	if err != nil {
		t.Fatalf("armor encode public key: %v", err)
	}
	if err := entity.Serialize(w); err != nil {
		t.Fatalf("serialize public key: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close armored public key: %v", err)
	}

	return buf.String()
}

func resignEntitySelfSignatures(t *testing.T, entity *openpgp.Entity) {
	t.Helper()

	for _, identity := range entity.Identities {
		if identity == nil || identity.UserId == nil || identity.SelfSignature == nil {
			continue
		}
		if err := identity.SelfSignature.SignUserId(identity.UserId.Id, entity.PrimaryKey, entity.PrivateKey, nil); err != nil {
			t.Fatalf("re-sign identity self-signature: %v", err)
		}
	}
	for i := range entity.Subkeys {
		if entity.Subkeys[i].PublicKey == nil || entity.Subkeys[i].Sig == nil {
			continue
		}
		if err := entity.Subkeys[i].Sig.SignKey(entity.Subkeys[i].PublicKey, entity.PrivateKey, nil); err != nil {
			t.Fatalf("re-sign subkey binding: %v", err)
		}
	}
}

func signCommitPayload(t *testing.T, entity *openpgp.Entity, payload string) string {
	t.Helper()

	var sig bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&sig, entity, strings.NewReader(payload), nil); err != nil {
		t.Fatalf("armored detach sign: %v", err)
	}
	return sig.String()
}

func TestGitHandlers(t *testing.T) {
	t.Run("ContentInvalidPath", func(t *testing.T) {
		h := testharness.New(t)
		createGitRepo(t, h, "git-invalid-path", "main", true)

		badPath := "/api/v3/repos/testuser/git-invalid-path/contents/.."
		w := h.DoREST(t, "GET", badPath, nil)
		assertStatusCode(t, w, 404)

		w = h.DoRESTJSON(t, "PUT", badPath, map[string]any{
			"message": "nope",
			"content": base64.StdEncoding.EncodeToString([]byte("data")),
		})
		assertStatusCode(t, w, 422)
		resp := testharness.DecodeJSON(t, w)
		if resp["message"] != "invalid path" {
			t.Errorf("PUT invalid path message: got %v, want %q", resp["message"], "invalid path")
		}

		w = h.DoRESTJSON(t, "DELETE", badPath, map[string]any{
			"message": "nope",
		})
		assertStatusCode(t, w, 422)
		resp = testharness.DecodeJSON(t, w)
		if resp["message"] != "invalid path" {
			t.Errorf("DELETE invalid path message: got %v, want %q", resp["message"], "invalid path")
		}
	})

	t.Run("ContentBase64Error", func(t *testing.T) {
		h := testharness.New(t)
		createGitRepo(t, h, "git-base64-error", "main", true)

		path := "/api/v3/repos/testuser/git-base64-error/contents/file.txt"
		w := h.DoRESTJSON(t, "PUT", path, map[string]any{
			"message": "add file",
			"content": "not-base64!!",
		})
		assertStatusCode(t, w, 422)
		resp := testharness.DecodeJSON(t, w)
		if resp["message"] != "content must be base64-encoded" {
			t.Errorf("base64 error message: got %v, want %q", resp["message"], "content must be base64-encoded")
		}
	})

	t.Run("ContentBranchFallback", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()
		fullName := createGitRepo(t, h, "git-branch-fallback", "trunk", true)

		initialSHA, err := h.Svc.Git.HeadSHA(ctx, fullName, "trunk")
		if err != nil {
			t.Fatalf("head trunk before put: %v", err)
		}

		content := "hello trunk"
		path := "/api/v3/repos/testuser/git-branch-fallback/contents/notes.txt"
		w := h.DoRESTJSON(t, "PUT", path, map[string]any{
			"message": "add notes",
			"content": base64.StdEncoding.EncodeToString([]byte(content)),
		})
		assertStatusCode(t, w, 201)

		updatedSHA, err := h.Svc.Git.HeadSHA(ctx, fullName, "trunk")
		if err != nil {
			t.Fatalf("head trunk after put: %v", err)
		}
		if updatedSHA == initialSHA {
			t.Fatalf("expected trunk head to change after PUT")
		}
		if _, err := h.Svc.Git.HeadSHA(ctx, fullName, "main"); err == nil {
			t.Fatalf("expected main branch to be absent when default is trunk")
		}

		w = h.DoREST(t, "GET", path, nil)
		assertStatusCode(t, w, 200)
		body := testharness.DecodeJSON(t, w)
		if body["name"] != "notes.txt" {
			t.Errorf("content name: got %v, want %q", body["name"], "notes.txt")
		}
		if body["encoding"] != "base64" {
			t.Errorf("encoding: got %v, want %q", body["encoding"], "base64")
		}
		if body["content"] != base64.StdEncoding.EncodeToString([]byte(content)) {
			t.Errorf("content mismatch: got %v", body["content"])
		}
		assertContentFileShape(t, body, "notes.txt", "notes.txt", "trunk", true)
		blobSHA, _ := body["sha"].(string)
		if blobSHA == "" {
			t.Fatalf("content sha: got empty")
		}

		w = h.DoRESTJSON(t, "DELETE", path, map[string]any{
			"message": "delete notes",
			"sha":     blobSHA,
		})
		assertStatusCode(t, w, 200)

		afterDeleteSHA, err := h.Svc.Git.HeadSHA(ctx, fullName, "trunk")
		if err != nil {
			t.Fatalf("head trunk after delete: %v", err)
		}
		if afterDeleteSHA == updatedSHA {
			t.Fatalf("expected trunk head to change after DELETE")
		}

		w = h.DoREST(t, "GET", path, nil)
		assertStatusCode(t, w, 404)
	})

	t.Run("ContentsSHAValidation", func(t *testing.T) {
		h := testharness.New(t)
		createGitRepo(t, h, "git-content-sha", "main", true)

		path := "/api/v3/repos/testuser/git-content-sha/contents/file.txt"
		w := h.DoRESTJSON(t, "PUT", path, map[string]any{
			"message": "create file",
			"content": base64.StdEncoding.EncodeToString([]byte("one")),
		})
		assertStatusCode(t, w, 201)
		createBody := testharness.DecodeJSON(t, w)
		createdContent, ok := createBody["content"].(map[string]any)
		if !ok {
			t.Fatalf("create content: expected object, got %T", createBody["content"])
		}
		assertContentFileShape(t, createdContent, "file.txt", "file.txt", "main", false)
		if size, _ := createdContent["size"].(float64); size != 3 {
			t.Fatalf("created content size: got %v, want 3", createdContent["size"])
		}

		w = h.DoREST(t, "GET", path, nil)
		assertStatusCode(t, w, 200)
		body := testharness.DecodeJSON(t, w)
		assertContentFileShape(t, body, "file.txt", "file.txt", "main", true)
		sha, _ := body["sha"].(string)
		if sha == "" {
			t.Fatalf("content sha: got empty")
		}

		w = h.DoRESTJSON(t, "PUT", path, map[string]any{
			"message": "missing sha update",
			"content": base64.StdEncoding.EncodeToString([]byte("two")),
		})
		assertStatusCode(t, w, 422)

		w = h.DoRESTJSON(t, "PUT", path, map[string]any{
			"message": "wrong sha update",
			"content": base64.StdEncoding.EncodeToString([]byte("two")),
			"sha":     "0000000000000000000000000000000000000000",
		})
		assertStatusCode(t, w, 409)

		w = h.DoRESTJSON(t, "PUT", path, map[string]any{
			"message": "correct sha update",
			"content": base64.StdEncoding.EncodeToString([]byte("two")),
			"sha":     sha,
		})
		assertStatusCode(t, w, 200)
		w = h.DoREST(t, "GET", path, nil)
		assertStatusCode(t, w, 200)
		body = testharness.DecodeJSON(t, w)
		updatedSHA, _ := body["sha"].(string)
		if updatedSHA == "" || updatedSHA == sha {
			t.Fatalf("updated content sha: got %q, previous %q", updatedSHA, sha)
		}

		w = h.DoRESTJSON(t, "DELETE", path, map[string]any{
			"message": "missing sha delete",
		})
		assertStatusCode(t, w, 422)

		w = h.DoRESTJSON(t, "DELETE", path, map[string]any{
			"message": "wrong sha delete",
			"sha":     sha,
		})
		assertStatusCode(t, w, 409)

		w = h.DoRESTJSON(t, "DELETE", path, map[string]any{
			"message": "correct sha delete",
			"sha":     updatedSHA,
		})
		assertStatusCode(t, w, 200)
	})

	t.Run("ReadmeResolutionOrder", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()
		fullName := createGitRepo(t, h, "git-readme-order", "main", true)

		if _, err := h.Svc.Git.DeleteFileFromRepo(ctx, fullName, "main", "README.md", "remove seeded readme"); err != nil {
			t.Fatalf("remove README.md: %v", err)
		}
		if _, err := h.Svc.Git.WriteFile(ctx, fullName, "main", "README", "add readme", []byte("primary readme")); err != nil {
			t.Fatalf("write README: %v", err)
		}
		if _, err := h.Svc.Git.WriteFile(ctx, fullName, "main", "readme.md", "add readme.md", []byte("secondary readme")); err != nil {
			t.Fatalf("write readme.md: %v", err)
		}

		w := h.DoREST(t, "GET", "/api/v3/repos/testuser/git-readme-order/readme", nil)
		assertStatusCode(t, w, 200)
		body := testharness.DecodeJSON(t, w)
		if body["name"] != "README" {
			t.Errorf("readme name: got %v, want %q", body["name"], "README")
		}
		if body["content"] != base64.StdEncoding.EncodeToString([]byte("primary readme")) {
			t.Errorf("readme content mismatch: got %v", body["content"])
		}
	})

	t.Run("GitRefs", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()
		fullName := createGitRepo(t, h, "git-refs", "main", true)

		headSHA, err := h.Svc.Git.HeadSHA(ctx, fullName, "main")
		if err != nil {
			t.Fatalf("head main: %v", err)
		}

		w := h.DoREST(t, "GET", "/api/v3/repos/testuser/git-refs/git/refs/heads/main", nil)
		assertStatusCode(t, w, 200)
		body := testharness.DecodeJSON(t, w)
		if body["ref"] != "refs/heads/main" {
			t.Errorf("ref: got %v, want %q", body["ref"], "refs/heads/main")
		}
		obj, ok := body["object"].(map[string]any)
		if !ok {
			t.Fatalf("object: expected map, got %T", body["object"])
		}
		if obj["sha"] != headSHA {
			t.Errorf("sha: got %v, want %q", obj["sha"], headSHA)
		}

		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-refs/git/refs/heads/missing", nil)
		assertStatusCode(t, w, 404)

		w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/git-refs/git/refs/heads/main", map[string]any{
			"sha": headSHA,
		})
		assertStatusCode(t, w, 200)

		w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/git-refs/git/refs/heads/main", map[string]any{
			"sha": "deadbeef",
		})
		assertStatusCode(t, w, 422)

		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-refs/git/refs", map[string]any{
			"ref": "refs/heads/feature",
			"sha": headSHA,
		})
		assertStatusCode(t, w, 201)

		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-refs/git/refs", map[string]any{
			"ref": "refs/heads/bad",
			"sha": "deadbeef",
		})
		assertStatusCode(t, w, 422)

		w = h.DoREST(t, "DELETE", "/api/v3/repos/testuser/git-refs/git/refs/heads/feature", nil)
		assertStatusCode(t, w, 204)

		w = h.DoREST(t, "DELETE", "/api/v3/repos/testuser/git-missing/git/refs/heads/main", nil)
		assertStatusCode(t, w, 404)
	})

	t.Run("GitRefsCustomNamespace", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()
		fullName := createGitRepo(t, h, "git-refs-custom", "main", true)

		headSHA, err := h.Svc.Git.HeadSHA(ctx, fullName, "main")
		if err != nil {
			t.Fatalf("head main: %v", err)
		}

		w := h.DoRESTJSON(t, "POST", "/api/v3/repos/"+fullName+"/git/refs", map[string]any{
			"ref": "refs/locks/issue-42",
			"sha": headSHA,
		})
		assertStatusCode(t, w, 201)

		w = h.DoREST(t, "GET", "/api/v3/repos/"+fullName+"/git/refs/locks/issue-42", nil)
		assertStatusCode(t, w, 200)
		body := testharness.DecodeJSON(t, w)
		if body["ref"] != "refs/locks/issue-42" {
			t.Errorf("ref: got %v, want %q", body["ref"], "refs/locks/issue-42")
		}
		obj, ok := body["object"].(map[string]any)
		if !ok {
			t.Fatalf("object: expected map, got %T", body["object"])
		}
		if obj["sha"] != headSHA {
			t.Errorf("sha: got %v, want %q", obj["sha"], headSHA)
		}

		w = h.DoREST(t, "GET", "/api/v3/repos/"+fullName+"/git/ref/locks/issue-42", nil)
		assertStatusCode(t, w, 200)

		if err := h.Svc.Git.CreateBranch(ctx, fullName, "feature", "main"); err != nil {
			t.Fatalf("create feature branch: %v", err)
		}
		featureSHA, err := h.Svc.Git.WriteFile(ctx, fullName, "feature", "feature.txt", "add feature", []byte("feature\n"))
		if err != nil {
			t.Fatalf("write feature commit: %v", err)
		}

		w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/"+fullName+"/git/refs/locks/issue-42", map[string]any{
			"sha":   featureSHA,
			"force": false,
		})
		assertStatusCode(t, w, 200)
		body = testharness.DecodeJSON(t, w)
		if body["ref"] != "refs/locks/issue-42" {
			t.Errorf("patched ref: got %v, want %q", body["ref"], "refs/locks/issue-42")
		}
		obj, ok = body["object"].(map[string]any)
		if !ok {
			t.Fatalf("patched object: expected map, got %T", body["object"])
		}
		if obj["sha"] != featureSHA {
			t.Errorf("patched sha: got %v, want %q", obj["sha"], featureSHA)
		}

		w = h.DoREST(t, "GET", "/api/v3/repos/"+fullName+"/git/refs/locks/issue-42", nil)
		assertStatusCode(t, w, 200)
		body = testharness.DecodeJSON(t, w)
		obj, ok = body["object"].(map[string]any)
		if !ok {
			t.Fatalf("re-read object: expected map, got %T", body["object"])
		}
		if obj["sha"] != featureSHA {
			t.Errorf("re-read sha: got %v, want %q", obj["sha"], featureSHA)
		}

		// Rewinding from featureSHA back to headSHA (its ancestor) is a
		// non-fast-forward update and now requires force=true per the
		// fix for issue #1291.
		w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/"+fullName+"/git/ref/locks/issue-42", map[string]any{
			"sha":   headSHA,
			"force": true,
		})
		assertStatusCode(t, w, 200)

		w = h.DoREST(t, "GET", "/api/v3/repos/"+fullName+"/git/refs/locks/nope", nil)
		assertStatusCode(t, w, 404)

		w = h.DoREST(t, "GET", "/api/v3/repos/"+fullName+"/git/matching-refs/locks", nil)
		assertStatusCode(t, w, 200)
		arr := testharness.DecodeJSONArray(t, w)
		if len(arr) != 1 {
			t.Fatalf("matching-refs/locks: got %d entries, want 1", len(arr))
		}
		if arr[0]["ref"] != "refs/locks/issue-42" {
			t.Errorf("matching-refs ref: got %v, want %q", arr[0]["ref"], "refs/locks/issue-42")
		}

		w = h.DoREST(t, "GET", "/api/v3/repos/"+fullName+"/git/matching-refs", nil)
		assertStatusCode(t, w, 200)
		all := testharness.DecodeJSONArray(t, w)
		seen := map[string]bool{}
		for _, item := range all {
			if ref, _ := item["ref"].(string); ref != "" {
				seen[ref] = true
			}
		}
		if !seen["refs/heads/main"] || !seen["refs/locks/issue-42"] {
			t.Errorf("empty-prefix matching-refs missing expected refs; got %v", seen)
		}

		w = h.DoREST(t, "DELETE", "/api/v3/repos/"+fullName+"/git/refs/locks/issue-42", nil)
		assertStatusCode(t, w, 204)

		w = h.DoREST(t, "GET", "/api/v3/repos/"+fullName+"/git/refs/locks/issue-42", nil)
		assertStatusCode(t, w, 404)
	})

	t.Run("CreateGitRefDuplicate", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()
		fullName := createGitRepo(t, h, "git-refs-dup", "main", true)

		headSHA, err := h.Svc.Git.HeadSHA(ctx, fullName, "main")
		if err != nil {
			t.Fatalf("head main: %v", err)
		}

		path := "/api/v3/repos/" + fullName + "/git/refs"
		body := map[string]any{"ref": "refs/locks/issue-42", "sha": headSHA}

		w := h.DoRESTJSON(t, "POST", path, body)
		assertStatusCode(t, w, 201)

		w = h.DoRESTJSON(t, "POST", path, body)
		assertStatusCode(t, w, 422)
		resp := testharness.DecodeJSON(t, w)
		if msg, _ := resp["message"].(string); !strings.Contains(msg, "Reference already exists") {
			t.Errorf("duplicate ref message: got %v, want contains %q", resp["message"], "Reference already exists")
		}
	})

	t.Run("GitDatabaseCommitAndTreeEndpoints", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()
		fullName := createGitRepo(t, h, "git-database", "main", true)

		headSHA, err := h.Svc.Git.HeadSHA(ctx, fullName, "main")
		if err != nil {
			t.Fatalf("head main: %v", err)
		}

		w := h.DoREST(t, "GET", "/api/v3/repos/testuser/git-database/git/commits/"+headSHA, nil)
		assertStatusCode(t, w, 200)
		commitBody := testharness.DecodeJSON(t, w)
		if commitBody["sha"] != headSHA {
			t.Fatalf("git commit sha: got %v, want %q", commitBody["sha"], headSHA)
		}
		if commitBody["message"] != "Initial commit" {
			t.Fatalf("git commit message: got %v, want %q", commitBody["message"], "Initial commit")
		}
		parents, ok := commitBody["parents"].([]any)
		if !ok {
			t.Fatalf("git commit parents: expected array, got %T", commitBody["parents"])
		}
		if len(parents) != 0 {
			t.Fatalf("git commit parents: expected root commit, got %d parents", len(parents))
		}
		author, ok := commitBody["author"].(map[string]any)
		if !ok {
			t.Fatalf("git commit author: expected map, got %T", commitBody["author"])
		}
		if author["name"] != "gh-server" {
			t.Fatalf("git commit author name: got %v, want %q", author["name"], "gh-server")
		}
		if nodeID, ok := commitBody["node_id"].(string); !ok || nodeID == "" {
			t.Fatalf("git commit node_id: got %v", commitBody["node_id"])
		}
		treeObj, ok := commitBody["tree"].(map[string]any)
		if !ok {
			t.Fatalf("git commit tree: expected map, got %T", commitBody["tree"])
		}
		treeSHA, ok := treeObj["sha"].(string)
		if !ok || treeSHA == "" {
			t.Fatalf("git commit tree sha: got %v", treeObj["sha"])
		}
		if treeSHA == headSHA {
			t.Fatalf("git commit tree sha should not reuse commit sha: %s", treeSHA)
		}

		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-database/git/trees/"+treeSHA, nil)
		assertStatusCode(t, w, 200)
		treeBody := testharness.DecodeJSON(t, w)
		if treeBody["sha"] != treeSHA {
			t.Fatalf("git tree sha: got %v, want %q", treeBody["sha"], treeSHA)
		}
		if treeBody["truncated"] != false {
			t.Fatalf("git tree truncated: got %v, want false", treeBody["truncated"])
		}
		entries, ok := treeBody["tree"].([]any)
		if !ok {
			t.Fatalf("git tree entries: expected array, got %T", treeBody["tree"])
		}
		if len(entries) != 1 {
			t.Fatalf("git tree entries: expected 1 entry, got %d", len(entries))
		}
		entry, ok := entries[0].(map[string]any)
		if !ok {
			t.Fatalf("git tree entry: expected map, got %T", entries[0])
		}
		if entry["path"] != "README.md" {
			t.Fatalf("git tree path: got %v, want %q", entry["path"], "README.md")
		}
		if entry["type"] != "blob" {
			t.Fatalf("git tree type: got %v, want %q", entry["type"], "blob")
		}

		if _, err := h.Svc.Git.WriteFile(ctx, fullName, "main", "nested/child.txt", "add nested child", []byte("nested")); err != nil {
			t.Fatalf("write nested child: %v", err)
		}
		nestedHeadSHA, err := h.Svc.Git.HeadSHA(ctx, fullName, "main")
		if err != nil {
			t.Fatalf("head main after nested child: %v", err)
		}

		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-database/git/trees/"+nestedHeadSHA+"?recursive=0", nil)
		assertStatusCode(t, w, 200)
		recursiveByPresence := testharness.DecodeJSON(t, w)
		if recursiveByPresence["sha"] != nestedHeadSHA {
			t.Fatalf("recursive-by-presence tree sha: got %v, want %q", recursiveByPresence["sha"], nestedHeadSHA)
		}
		recursiveEntries, ok := recursiveByPresence["tree"].([]any)
		if !ok {
			t.Fatalf("recursive-by-presence tree entries: expected array, got %T", recursiveByPresence["tree"])
		}
		foundNestedTree := false
		foundNestedChild := false
		for _, raw := range recursiveEntries {
			item, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("recursive-by-presence tree entry: expected map, got %T", raw)
			}
			if item["path"] == "nested" && item["type"] == "tree" {
				foundNestedTree = true
			}
			if item["path"] == "nested/child.txt" {
				foundNestedChild = true
			}
		}
		if !foundNestedTree {
			t.Fatalf("recursive-by-presence tree should include nested tree entry, got %#v", recursiveEntries)
		}
		if !foundNestedChild {
			t.Fatalf("recursive-by-presence tree should include nested/child.txt, got %#v", recursiveEntries)
		}
		nestedCommit, err := h.Svc.Git.GetGitCommitObject(ctx, fullName, nestedHeadSHA)
		if err != nil {
			t.Fatalf("get nested commit: %v", err)
		}
		nestedTreeSHA := nestedCommit.TreeSHA

		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-database/git/commits", map[string]any{
			"message": "rest-created commit",
			"tree":    nestedTreeSHA,
			"parents": []string{nestedHeadSHA},
		})
		assertStatusCode(t, w, 201)
		created := testharness.DecodeJSON(t, w)
		createdSHA, ok := created["sha"].(string)
		if !ok || len(createdSHA) != 40 {
			t.Fatalf("created git commit sha: got %v", created["sha"])
		}
		if createdSHA == headSHA {
			t.Fatalf("created git commit sha should differ from head: %s", createdSHA)
		}
		createdTree, ok := created["tree"].(map[string]any)
		if !ok {
			t.Fatalf("created git commit tree: expected map, got %T", created["tree"])
		}
		if createdTree["sha"] != nestedTreeSHA {
			t.Fatalf("created git commit tree sha: got %v, want %q", createdTree["sha"], nestedTreeSHA)
		}
		createdParents, ok := created["parents"].([]any)
		if !ok || len(createdParents) != 1 {
			t.Fatalf("created git commit parents: got %v", created["parents"])
		}
		parentObj, ok := createdParents[0].(map[string]any)
		if !ok {
			t.Fatalf("created git commit parent: expected map, got %T", createdParents[0])
		}
		if parentObj["sha"] != nestedHeadSHA {
			t.Fatalf("created git commit parent sha: got %v, want %q", parentObj["sha"], nestedHeadSHA)
		}
		createdAuthor, ok := created["author"].(map[string]any)
		if !ok {
			t.Fatalf("created git commit author: expected map, got %T", created["author"])
		}
		if createdAuthor["name"] != h.User.Name {
			t.Fatalf("created git commit author name: got %v, want %q", createdAuthor["name"], h.User.Name)
		}
		if nodeID, ok := created["node_id"].(string); !ok || nodeID == "" {
			t.Fatalf("created git commit node_id: got %v", created["node_id"])
		}

		updatedHeadSHA, err := h.Svc.Git.HeadSHA(ctx, fullName, "main")
		if err != nil {
			t.Fatalf("head main after create: %v", err)
		}
		if updatedHeadSHA != nestedHeadSHA {
			t.Fatalf("git commit create should not update HEAD: got %s, want %s", updatedHeadSHA, nestedHeadSHA)
		}

		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-database/git/commits/"+createdSHA, nil)
		assertStatusCode(t, w, 200)
		fetched := testharness.DecodeJSON(t, w)
		if fetched["message"] != "rest-created commit" {
			t.Fatalf("fetched created git commit message: got %v, want %q", fetched["message"], "rest-created commit")
		}

		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-database/git/trees/"+createdSHA+"?recursive=1", nil)
		assertStatusCode(t, w, 200)
		recursiveTree := testharness.DecodeJSON(t, w)
		if recursiveTree["sha"] != createdSHA {
			t.Fatalf("recursive tree sha from commit: got %v, want %q", recursiveTree["sha"], createdSHA)
		}
		treeItems, ok := recursiveTree["tree"].([]any)
		if !ok {
			t.Fatalf("recursive tree items from commit: expected array, got %T", recursiveTree["tree"])
		}
		foundNestedTree = false
		foundNestedChild = false
		for _, raw := range treeItems {
			item, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("recursive tree item from commit: expected map, got %T", raw)
			}
			if item["path"] == "nested" && item["type"] == "tree" {
				foundNestedTree = true
			}
			if item["path"] == "nested/child.txt" {
				foundNestedChild = true
			}
		}
		if !foundNestedTree || !foundNestedChild {
			t.Fatalf("recursive tree from commit missing expected entries: %#v", treeItems)
		}
	})

	t.Run("GitDatabaseCreateBlobAndTree_Issue1292", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()
		fullName := createGitRepo(t, h, "git-blob-tree", "main", true)

		// 1. POST /git/blobs (utf-8 default).
		w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/blobs", map[string]any{
			"content": "hello world",
		})
		assertStatusCode(t, w, 201)
		blob := testharness.DecodeJSON(t, w)
		blobSHA, ok := blob["sha"].(string)
		if !ok || len(blobSHA) != 40 {
			t.Fatalf("create blob sha: got %v", blob["sha"])
		}
		blobURL, ok := blob["url"].(string)
		if !ok || !strings.HasSuffix(blobURL, "/git/blobs/"+blobSHA) {
			t.Fatalf("create blob url: got %v", blob["url"])
		}

		// 2. POST /git/blobs (base64), and verify byte-exact round-trip
		// against the canonical git blob hash: sha1("blob <n>\0<bytes>").
		binary := []byte{0x00, 0x01, 0xff, 0xfe, 0x7f}
		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/blobs", map[string]any{
			"content":  base64.StdEncoding.EncodeToString(binary),
			"encoding": "base64",
		})
		assertStatusCode(t, w, 201)
		binaryBlob := testharness.DecodeJSON(t, w)
		binarySHA, _ := binaryBlob["sha"].(string)
		hasher := sha1.New()
		hasher.Write([]byte(fmt.Sprintf("blob %d\x00", len(binary))))
		hasher.Write(binary)
		wantSHA := hex.EncodeToString(hasher.Sum(nil))
		if binarySHA != wantSHA {
			t.Fatalf("base64 blob sha: got %s, want %s (byte-exact round-trip failed)", binarySHA, wantSHA)
		}

		// 2b. GET /git/blobs/{sha} round-trips the bytes verbatim.
		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-blob-tree/git/blobs/"+binarySHA, nil)
		assertStatusCode(t, w, 200)
		got := testharness.DecodeJSON(t, w)
		if got["sha"] != binarySHA {
			t.Fatalf("get blob sha: got %v, want %s", got["sha"], binarySHA)
		}
		if got["encoding"] != "base64" {
			t.Fatalf("get blob encoding: got %v, want base64", got["encoding"])
		}
		// JSON unmarshal: numbers decode to float64.
		if size, _ := got["size"].(float64); int(size) != len(binary) {
			t.Fatalf("get blob size: got %v, want %d", got["size"], len(binary))
		}
		decodedContent, decErr := base64.StdEncoding.DecodeString(got["content"].(string))
		if decErr != nil {
			t.Fatalf("get blob content not valid base64: %v", decErr)
		}
		if !bytes.Equal(decodedContent, binary) {
			t.Fatalf("get blob bytes: got %v, want %v", decodedContent, binary)
		}
		if nodeID, _ := got["node_id"].(string); nodeID == "" {
			t.Fatalf("get blob node_id: got empty")
		}

		// 2c. GET /git/blobs/{sha} on a non-blob (use the initial commit's tree
		// SHA) returns 404. Establishes that the endpoint validates object type.
		initHead, err := h.Svc.Git.HeadSHA(ctx, fullName, "main")
		if err != nil {
			t.Fatalf("head main for non-blob check: %v", err)
		}
		initCommitObj, err := h.Svc.Git.GetGitCommitObject(ctx, fullName, initHead)
		if err != nil {
			t.Fatalf("get initial commit for non-blob check: %v", err)
		}
		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-blob-tree/git/blobs/"+initCommitObj.TreeSHA, nil)
		assertStatusCode(t, w, 404)

		// 2d. GET /git/blobs/{sha} on an unknown SHA returns 404.
		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-blob-tree/git/blobs/0000000000000000000000000000000000000000", nil)
		assertStatusCode(t, w, 404)

		// 3. POST /git/blobs with bad encoding -> 422.
		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/blobs", map[string]any{
			"content":  "hi",
			"encoding": "latin-1",
		})
		assertStatusCode(t, w, 422)

		// 4. POST /git/blobs with invalid base64 -> 422.
		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/blobs", map[string]any{
			"content":  "not!!!base64",
			"encoding": "base64",
		})
		assertStatusCode(t, w, 422)

		// 5. POST /git/trees from two blobs with no base_tree.
		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/trees", map[string]any{
			"tree": []map[string]any{
				{"path": "flat.txt", "mode": "100644", "type": "blob", "sha": blobSHA},
				{"path": "dir/binary.bin", "mode": "100644", "type": "blob", "sha": binarySHA},
			},
		})
		assertStatusCode(t, w, 201)
		treeResp := testharness.DecodeJSON(t, w)
		newTreeSHA, ok := treeResp["sha"].(string)
		if !ok || len(newTreeSHA) != 40 {
			t.Fatalf("create tree sha: got %v", treeResp["sha"])
		}
		treeURL, ok := treeResp["url"].(string)
		if !ok || !strings.HasSuffix(treeURL, "/git/trees/"+newTreeSHA) {
			t.Fatalf("create tree url: got %v", treeResp["url"])
		}
		// recursive readback should find both entries (the "dir" entry plus dir/binary.bin and flat.txt).
		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-blob-tree/git/trees/"+newTreeSHA+"?recursive=1", nil)
		assertStatusCode(t, w, 200)
		recursive := testharness.DecodeJSON(t, w)
		recursiveItems, _ := recursive["tree"].([]any)
		var foundFlat, foundNested bool
		for _, raw := range recursiveItems {
			item, _ := raw.(map[string]any)
			switch item["path"] {
			case "flat.txt":
				foundFlat = true
			case "dir/binary.bin":
				foundNested = true
			}
		}
		if !foundFlat || !foundNested {
			t.Fatalf("recursive tree missing expected entries: %#v", recursiveItems)
		}

		// 6. POST /git/trees with base_tree (the initial commit's tree) plus an extra entry.
		headSHA, err := h.Svc.Git.HeadSHA(ctx, fullName, "main")
		if err != nil {
			t.Fatalf("head main: %v", err)
		}
		initialCommit, err := h.Svc.Git.GetGitCommitObject(ctx, fullName, headSHA)
		if err != nil {
			t.Fatalf("get initial commit: %v", err)
		}
		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/trees", map[string]any{
			"base_tree": initialCommit.TreeSHA,
			"tree": []map[string]any{
				{"path": "added.txt", "mode": "100644", "type": "blob", "sha": blobSHA},
			},
		})
		assertStatusCode(t, w, 201)
		baseTreeResp := testharness.DecodeJSON(t, w)
		baseTreeSHA, _ := baseTreeResp["sha"].(string)
		if len(baseTreeSHA) != 40 {
			t.Fatalf("base_tree result sha: got %v", baseTreeResp["sha"])
		}
		// Should contain both the original README.md and the new added.txt.
		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-blob-tree/git/trees/"+baseTreeSHA, nil)
		assertStatusCode(t, w, 200)
		merged := testharness.DecodeJSON(t, w)
		mergedItems, _ := merged["tree"].([]any)
		var hasReadme, hasAdded bool
		for _, raw := range mergedItems {
			item, _ := raw.(map[string]any)
			switch item["path"] {
			case "README.md":
				hasReadme = true
			case "added.txt":
				hasAdded = true
			}
		}
		if !hasReadme || !hasAdded {
			t.Fatalf("base_tree merge missing entries: %#v", mergedItems)
		}

		// 7. POST /git/trees with sha:null deletes an entry from base_tree.
		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/trees", map[string]any{
			"base_tree": initialCommit.TreeSHA,
			"tree": []map[string]any{
				{"path": "README.md", "mode": "100644", "type": "blob", "sha": nil},
			},
		})
		assertStatusCode(t, w, 201)
		deletedTreeResp := testharness.DecodeJSON(t, w)
		deletedTreeSHA, _ := deletedTreeResp["sha"].(string)
		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-blob-tree/git/trees/"+deletedTreeSHA, nil)
		assertStatusCode(t, w, 200)
		deletedTree := testharness.DecodeJSON(t, w)
		deletedItems, _ := deletedTree["tree"].([]any)
		for _, raw := range deletedItems {
			item, _ := raw.(map[string]any)
			if item["path"] == "README.md" {
				t.Fatalf("sha:null delete kept README.md: %#v", deletedItems)
			}
		}

		// 8. POST /git/trees with sha:null deletes a subtree from base_tree.
		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/trees", map[string]any{
			"base_tree": initialCommit.TreeSHA,
			"tree": []map[string]any{
				{"path": "docs", "mode": "040000", "type": "tree", "sha": nil},
			},
		})
		assertStatusCode(t, w, 201)
		deletedDirTreeResp := testharness.DecodeJSON(t, w)
		deletedDirTreeSHA, _ := deletedDirTreeResp["sha"].(string)
		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-blob-tree/git/trees/"+deletedDirTreeSHA+"?recursive=1", nil)
		assertStatusCode(t, w, 200)
		deletedDirTree := testharness.DecodeJSON(t, w)
		deletedDirItems, _ := deletedDirTree["tree"].([]any)
		var hasDeletedDocs, hasRetainedReadme bool
		for _, raw := range deletedDirItems {
			item, _ := raw.(map[string]any)
			switch item["path"] {
			case "docs", "docs/guide.txt":
				hasDeletedDocs = true
			case "README.md":
				hasRetainedReadme = true
			}
		}
		if hasDeletedDocs {
			t.Fatalf("sha:null delete kept docs subtree: %#v", deletedDirItems)
		}
		if !hasRetainedReadme {
			t.Fatalf("sha:null subtree delete removed unrelated entries: %#v", deletedDirItems)
		}

		// 9. POST /git/trees with inline content (no sha).
		inline := "inline-bytes"
		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/trees", map[string]any{
			"tree": []map[string]any{
				{"path": "inline.txt", "mode": "100644", "type": "blob", "content": inline},
			},
		})
		assertStatusCode(t, w, 201)
		inlineTree := testharness.DecodeJSON(t, w)
		inlineEntries, _ := inlineTree["tree"].([]any)
		if len(inlineEntries) != 1 {
			t.Fatalf("inline tree entries: got %d, want 1", len(inlineEntries))
		}
		inlineEntry, _ := inlineEntries[0].(map[string]any)
		// The blob sha should be reproducible: same content via POST /git/blobs.
		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/blobs", map[string]any{
			"content": inline,
		})
		assertStatusCode(t, w, 201)
		expected := testharness.DecodeJSON(t, w)
		if inlineEntry["sha"] != expected["sha"] {
			t.Fatalf("inline blob sha: tree saw %v, separate blob hash %v", inlineEntry["sha"], expected["sha"])
		}

		// 9. Validation: empty tree -> 422.
		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/trees", map[string]any{
			"tree": []map[string]any{},
		})
		assertStatusCode(t, w, 422)

		// 10. Validation: invalid mode -> 422.
		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/trees", map[string]any{
			"tree": []map[string]any{
				{"path": "x.txt", "mode": "200644", "type": "blob", "sha": blobSHA},
			},
		})
		assertStatusCode(t, w, 422)

		// 11. Validation: type/mode mismatch -> 422.
		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/trees", map[string]any{
			"tree": []map[string]any{
				{"path": "x.txt", "mode": "100644", "type": "tree", "sha": blobSHA},
			},
		})
		assertStatusCode(t, w, 422)

		// 12. Validation: content on non-blob -> 422.
		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/trees", map[string]any{
			"tree": []map[string]any{
				{"path": "x", "mode": "040000", "type": "tree", "content": "nope"},
			},
		})
		assertStatusCode(t, w, 422)

		// 13. Validation: bad path -> 422 (covers / prefix, leading -, ..).
		for _, badPath := range []string{"/abs/path", "-h", "--version", "../escape", "trail/"} {
			w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/trees", map[string]any{
				"tree": []map[string]any{
					{"path": badPath, "mode": "100644", "type": "blob", "sha": blobSHA},
				},
			})
			if w.Code != 422 {
				t.Fatalf("bad path %q: got status %d, want 422", badPath, w.Code)
			}
		}

		// 14. Validation: base_tree not found -> 422.
		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/trees", map[string]any{
			"base_tree": "0000000000000000000000000000000000000000",
			"tree": []map[string]any{
				{"path": "x.txt", "mode": "100644", "type": "blob", "sha": blobSHA},
			},
		})
		assertStatusCode(t, w, 422)

		// 15. Validation: sha:null with content -> 422.
		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/trees", map[string]any{
			"base_tree": initialCommit.TreeSHA,
			"tree": []map[string]any{
				{"path": "README.md", "mode": "100644", "type": "blob", "sha": nil, "content": "nope"},
			},
		})
		assertStatusCode(t, w, 422)

		// 16. Validation: sha:null without base_tree -> 422.
		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/trees", map[string]any{
			"tree": []map[string]any{
				{"path": "README.md", "mode": "100644", "type": "blob", "sha": nil},
			},
		})
		assertStatusCode(t, w, 422)

		// 17. End-to-end: blob -> tree -> commit -> ref using only REST.
		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/commits", map[string]any{
			"message": "rest-only commit via blob+tree",
			"tree":    newTreeSHA,
			"parents": []string{headSHA},
		})
		assertStatusCode(t, w, 201)
		commitResp := testharness.DecodeJSON(t, w)
		commitSHA, _ := commitResp["sha"].(string)
		if len(commitSHA) != 40 {
			t.Fatalf("rest-only commit sha: got %v", commitResp["sha"])
		}
		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/refs", map[string]any{
			"ref": "refs/heads/rest-only",
			"sha": commitSHA,
		})
		assertStatusCode(t, w, 201)

		// 15. POST/GET /git/tags creates and reads annotated tag objects without creating a ref.
		w = h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-blob-tree/git/tags", map[string]any{
			"tag":     "v1.2.3",
			"message": "release v1.2.3",
			"object":  headSHA,
			"type":    "commit",
			"tagger": map[string]any{
				"name":  "Test User",
				"email": "test@example.com",
				"date":  "2026-04-25T12:00:00Z",
			},
		})
		assertStatusCode(t, w, 201)
		tagResp := testharness.DecodeJSON(t, w)
		tagSHA, _ := tagResp["sha"].(string)
		if len(tagSHA) != 40 {
			t.Fatalf("tag object sha: got %v", tagResp["sha"])
		}
		if tagResp["tag"] != "v1.2.3" || tagResp["message"] != "release v1.2.3" {
			t.Fatalf("tag response mismatch: %#v", tagResp)
		}
		tagObj, _ := tagResp["object"].(map[string]any)
		if tagObj["sha"] != headSHA || tagObj["type"] != "commit" {
			t.Fatalf("tag object target mismatch: %#v", tagObj)
		}
		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-blob-tree/git/tags/"+tagSHA, nil)
		assertStatusCode(t, w, 200)
		gotTag := testharness.DecodeJSON(t, w)
		if gotTag["sha"] != tagSHA {
			t.Fatalf("get tag sha: got %v, want %s", gotTag["sha"], tagSHA)
		}
		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-blob-tree/git/tags/"+headSHA, nil)
		assertStatusCode(t, w, 404)
	})

	t.Run("GitDatabaseWriteHandlersEmitAuditEvents_Issue1305", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()
		owner := h.User
		ownerToken := h.Token
		org, err := h.Svc.EnsureOrg(service.ContextWithUser(ctx, owner), "git-audit-org")
		if err != nil {
			t.Fatalf("EnsureOrg: %v", err)
		}
		fullName := createOrgGitRepo(t, h, org.Login, "git-audit-repo", "main", true)

		headSHA, err := h.Svc.Git.HeadSHA(ctx, fullName, "main")
		if err != nil {
			t.Fatalf("head main: %v", err)
		}
		headCommit, err := h.Svc.Git.GetGitCommitObject(ctx, fullName, headSHA)
		if err != nil {
			t.Fatalf("get head commit: %v", err)
		}

		w := h.DoRESTJSONWithToken(t, "POST", "/api/v3/repos/git-audit-org/git-audit-repo/git/blobs", ownerToken, map[string]any{
			"content": "audit bytes",
		})
		assertStatusCode(t, w, 201)
		blobSHA := testharness.DecodeJSON(t, w)["sha"].(string)

		w = h.DoRESTJSONWithToken(t, "POST", "/api/v3/repos/git-audit-org/git-audit-repo/git/trees", ownerToken, map[string]any{
			"base_tree": headCommit.TreeSHA,
			"tree":      []map[string]any{{"path": "audit.txt", "mode": "100644", "type": "blob", "sha": blobSHA}},
		})
		assertStatusCode(t, w, 201)
		treeSHA := testharness.DecodeJSON(t, w)["sha"].(string)

		w = h.DoRESTJSONWithToken(t, "POST", "/api/v3/repos/git-audit-org/git-audit-repo/git/commits", ownerToken, map[string]any{
			"message": "audit commit",
			"tree":    treeSHA,
			"parents": []string{headSHA},
		})
		assertStatusCode(t, w, 201)
		commitSHA := testharness.DecodeJSON(t, w)["sha"].(string)

		w = h.DoRESTJSONWithToken(t, "POST", "/api/v3/repos/git-audit-org/git-audit-repo/git/refs", ownerToken, map[string]any{
			"ref": "refs/heads/audit-branch",
			"sha": commitSHA,
		})
		assertStatusCode(t, w, 201)

		w = h.DoRESTJSONWithToken(t, "PATCH", "/api/v3/repos/git-audit-org/git-audit-repo/git/refs/heads/audit-branch", ownerToken, map[string]any{
			"sha":   headSHA,
			"force": true,
		})
		assertStatusCode(t, w, 200)

		w = h.DoRESTWithToken(t, "DELETE", "/api/v3/repos/git-audit-org/git-audit-repo/git/refs/heads/audit-branch", ownerToken)
		assertStatusCode(t, w, 204)

		w = h.DoRESTWithToken(t, "GET", "/api/v3/orgs/git-audit-org/audit-log", ownerToken)
		assertStatusCode(t, w, 200)
		rows := testharness.DecodeJSONArray(t, w)
		actions := map[string]map[string]any{}
		for _, row := range rows {
			action, _ := row["action"].(string)
			if strings.HasPrefix(action, "git.") {
				actions[action] = row
			}
		}
		for _, action := range []string{
			service.AuditActionGitBlobCreate,
			service.AuditActionGitTreeCreate,
			service.AuditActionGitCommitCreate,
			service.AuditActionGitRefCreate,
			service.AuditActionGitRefUpdate,
			service.AuditActionGitRefDelete,
		} {
			row, ok := actions[action]
			if !ok {
				t.Fatalf("missing audit action %s in rows=%v", action, rows)
			}
			if row["repo"] != fullName {
				t.Fatalf("audit repo for %s: got %v, want %s", action, row["repo"], fullName)
			}
			if row["actor"] != owner.Login {
				t.Fatalf("audit actor for %s: got %v, want %s", action, row["actor"], owner.Login)
			}
			if row["org"] != org.Login {
				t.Fatalf("audit org for %s: got %v, want %s", action, row["org"], org.Login)
			}
			if details, _ := row["details"].(string); details == "" {
				t.Fatalf("audit details for %s should not be empty: row=%v", action, row)
			}
		}
	})

	t.Run("GitDatabaseCommitSignatureVerification", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()
		fullName := createGitRepo(t, h, "git-signed-commit", "main", true)

		headSHA, err := h.Svc.Git.HeadSHA(ctx, fullName, "main")
		if err != nil {
			t.Fatalf("head main: %v", err)
		}
		headCommit, err := h.Svc.Git.GetGitCommitObject(ctx, fullName, headSHA)
		if err != nil {
			t.Fatalf("get head commit: %v", err)
		}

		entity, armoredKey := createTestGPGIdentity(t, "Signed User", "signed@example.com")
		if err := h.DB.Model(&db.User{}).Where("id = ?", h.User.ID).Updates(map[string]any{
			"email": "signed@example.com",
			"name":  "Signed User",
		}).Error; err != nil {
			t.Fatalf("update user email/name: %v", err)
		}
		h.User.Email = "signed@example.com"
		h.User.Name = "Signed User"
		if _, err := h.Svc.CreateGPGKey(ctx, h.User.ID, armoredKey); err != nil {
			t.Fatalf("create gpg key: %v", err)
		}

		authorDate := "2026-04-24T10:00:00+08:00"
		message := "signed via rest api"
		payload := buildSignedCommitPayload(t, headCommit.TreeSHA, []string{headSHA}, "Signed User", "signed@example.com", authorDate, "Signed User", "signed@example.com", authorDate, message)
		signature := signCommitPayload(t, entity, payload)

		w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/git-signed-commit/git/commits", map[string]any{
			"message": message,
			"tree":    headCommit.TreeSHA,
			"parents": []string{headSHA},
			"author": map[string]any{
				"name":  "Signed User",
				"email": "signed@example.com",
				"date":  authorDate,
			},
			"committer": map[string]any{
				"name":  "Signed User",
				"email": "signed@example.com",
				"date":  authorDate,
			},
			"signature": signature,
		})
		assertStatusCode(t, w, 201)
		body := testharness.DecodeJSON(t, w)
		verification, ok := body["verification"].(map[string]any)
		if !ok {
			t.Fatalf("verification: expected map, got %T", body["verification"])
		}
		if verification["verified"] != true {
			t.Fatalf("verification.verified: got %v, want true", verification["verified"])
		}
		if verification["reason"] != "valid" {
			t.Fatalf("verification.reason: got %v, want %q", verification["reason"], "valid")
		}
		expectedSignature := signature
		if !strings.HasSuffix(expectedSignature, "\n") {
			expectedSignature += "\n"
		}
		if verification["signature"] != expectedSignature {
			t.Fatalf("verification.signature mismatch:\n got: %q\nwant: %q", verification["signature"], expectedSignature)
		}
		if verification["payload"] != payload {
			t.Fatalf("verification.payload mismatch")
		}
		if verifiedAt, ok := verification["verified_at"].(string); !ok || verifiedAt == "" {
			t.Fatalf("verification.verified_at: got %v", verification["verified_at"])
		}

		commitSHA, ok := body["sha"].(string)
		if !ok || commitSHA == "" {
			t.Fatalf("commit sha: got %v", body["sha"])
		}
		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-signed-commit/git/commits/"+commitSHA, nil)
		assertStatusCode(t, w, 200)
		body = testharness.DecodeJSON(t, w)
		verification, ok = body["verification"].(map[string]any)
		if !ok {
			t.Fatalf("verification on get: expected map, got %T", body["verification"])
		}
		if verification["verified"] != true || verification["reason"] != "valid" {
			t.Fatalf("verification on get: got %#v", verification)
		}
	})

	t.Run("GitDatabaseCommitSignatureVerificationReasons", func(t *testing.T) {
		cases := []struct {
			name       string
			repoName   string
			wantReason string
			mutateKey  func(t *testing.T, entity *openpgp.Entity)
		}{
			{
				name:       "ExpiredKey",
				repoName:   "git-signed-expired-key",
				wantReason: "expired_key",
				mutateKey: func(t *testing.T, entity *openpgp.Entity) {
					t.Helper()
					lifetime := uint32(3600)
					expiredAt := time.Now().Add(-48 * time.Hour).UTC()
					for _, identity := range entity.Identities {
						if identity == nil || identity.SelfSignature == nil {
							continue
						}
						identity.SelfSignature.CreationTime = expiredAt
						identity.SelfSignature.KeyLifetimeSecs = &lifetime
						identity.SelfSignature.FlagsValid = true
						identity.SelfSignature.FlagSign = true
					}
				},
			},
			{
				name:       "NotSigningKey",
				repoName:   "git-signed-not-signing-key",
				wantReason: "not_signing_key",
				mutateKey: func(t *testing.T, entity *openpgp.Entity) {
					t.Helper()
					for _, identity := range entity.Identities {
						if identity == nil || identity.SelfSignature == nil {
							continue
						}
						identity.SelfSignature.FlagsValid = true
						identity.SelfSignature.FlagCertify = true
						identity.SelfSignature.FlagSign = false
					}
				},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				h := testharness.New(t)
				ctx := context.Background()
				fullName := createGitRepo(t, h, tc.repoName, "main", true)

				headSHA, err := h.Svc.Git.HeadSHA(ctx, fullName, "main")
				if err != nil {
					t.Fatalf("head main: %v", err)
				}
				headCommit, err := h.Svc.Git.GetGitCommitObject(ctx, fullName, headSHA)
				if err != nil {
					t.Fatalf("get head commit: %v", err)
				}

				entity, _ := createTestGPGIdentity(t, "Signed User", "signed@example.com")
				if err := h.DB.Model(&db.User{}).Where("id = ?", h.User.ID).Updates(map[string]any{
					"email": "signed@example.com",
					"name":  "Signed User",
				}).Error; err != nil {
					t.Fatalf("update user email/name: %v", err)
				}
				h.User.Email = "signed@example.com"
				h.User.Name = "Signed User"

				authorDate := "2026-04-24T10:00:00+08:00"
				message := "signed via rest api"
				payload := buildSignedCommitPayload(t, headCommit.TreeSHA, []string{headSHA}, "Signed User", "signed@example.com", authorDate, "Signed User", "signed@example.com", authorDate, message)
				signature := signCommitPayload(t, entity, payload)

				tc.mutateKey(t, entity)
				resignEntitySelfSignatures(t, entity)

				if _, err := h.Svc.CreateGPGKey(ctx, h.User.ID, armoredPublicKeyFromEntity(t, entity)); err != nil {
					t.Fatalf("create gpg key: %v", err)
				}

				w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/"+tc.repoName+"/git/commits", map[string]any{
					"message": message,
					"tree":    headCommit.TreeSHA,
					"parents": []string{headSHA},
					"author": map[string]any{
						"name":  "Signed User",
						"email": "signed@example.com",
						"date":  authorDate,
					},
					"committer": map[string]any{
						"name":  "Signed User",
						"email": "signed@example.com",
						"date":  authorDate,
					},
					"signature": signature,
				})
				assertStatusCode(t, w, 201)
				body := testharness.DecodeJSON(t, w)
				verification, ok := body["verification"].(map[string]any)
				if !ok {
					t.Fatalf("verification: expected map, got %T", body["verification"])
				}
				if verification["verified"] != false {
					t.Fatalf("verification.verified: got %v, want false", verification["verified"])
				}
				if verification["reason"] != tc.wantReason {
					t.Fatalf("verification.reason: got %v, want %q", verification["reason"], tc.wantReason)
				}
				expectedSignature := signature
				if !strings.HasSuffix(expectedSignature, "\n") {
					expectedSignature += "\n"
				}
				if verification["signature"] != expectedSignature {
					t.Fatalf("verification.signature mismatch:\n got: %q\nwant: %q", verification["signature"], expectedSignature)
				}
				if verification["payload"] != payload {
					t.Fatalf("verification.payload mismatch")
				}
				if verification["verified_at"] != nil {
					t.Fatalf("verification.verified_at: got %v, want nil", verification["verified_at"])
				}

				commitSHA, ok := body["sha"].(string)
				if !ok || commitSHA == "" {
					t.Fatalf("commit sha: got %v", body["sha"])
				}
				w = h.DoREST(t, "GET", "/api/v3/repos/testuser/"+tc.repoName+"/git/commits/"+commitSHA, nil)
				assertStatusCode(t, w, 200)
				body = testharness.DecodeJSON(t, w)
				verification, ok = body["verification"].(map[string]any)
				if !ok {
					t.Fatalf("verification on get: expected map, got %T", body["verification"])
				}
				if verification["verified"] != false || verification["reason"] != tc.wantReason {
					t.Fatalf("verification on get: got %#v, want reason %q", verification, tc.wantReason)
				}
			})
		}
	})
	t.Run("ContentsDirectoryListing", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()
		fullName := createGitRepo(t, h, "git-contents-dir", "main", true)

		// Remove the auto-initialized README to start clean
		if _, err := h.Svc.Git.DeleteFileFromRepo(ctx, fullName, "main", "README.md", "remove seeded readme"); err != nil {
			t.Fatalf("remove README.md: %v", err)
		}

		// Create a directory structure:
		// src/
		//   main.go
		//   utils/
		//     helper.go
		// docs/
		//   README.md

		// Create src/main.go
		w := h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/git-contents-dir/contents/src/main.go", map[string]any{
			"message": "add main.go",
			"content": base64.StdEncoding.EncodeToString([]byte("package main")),
		})
		assertStatusCode(t, w, 201)

		// Create src/utils/helper.go
		w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/git-contents-dir/contents/src/utils/helper.go", map[string]any{
			"message": "add helper.go",
			"content": base64.StdEncoding.EncodeToString([]byte("package utils")),
		})
		assertStatusCode(t, w, 201)

		// Create docs/README.md
		w = h.DoRESTJSON(t, "PUT", "/api/v3/repos/testuser/git-contents-dir/contents/docs/README.md", map[string]any{
			"message": "add docs readme",
			"content": base64.StdEncoding.EncodeToString([]byte("# Documentation")),
		})
		assertStatusCode(t, w, 201)

		// Test listing root directory - should return array
		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-contents-dir/contents", nil)
		assertStatusCode(t, w, 200)
		rootArr := testharness.DecodeJSONArray(t, w)
		if len(rootArr) != 2 {
			t.Fatalf("root contents: expected 2 entries (src, docs), got %d", len(rootArr))
		}

		// Find src and docs in the array
		var srcEntry, docsEntry map[string]any
		for _, item := range rootArr {
			name, _ := item["name"].(string)
			if name == "src" {
				srcEntry = item
			} else if name == "docs" {
				docsEntry = item
			}
		}

		if srcEntry == nil {
			t.Fatal("root contents: missing src entry")
		}
		if docsEntry == nil {
			t.Fatal("root contents: missing docs entry")
		}

		// Verify src is a directory
		if srcEntry["type"] != "dir" {
			t.Errorf("src type: got %v, want %q", srcEntry["type"], "dir")
		}
		if srcEntry["name"] != "src" {
			t.Errorf("src name: got %v, want %q", srcEntry["name"], "src")
		}
		if srcEntry["path"] != "src" {
			t.Errorf("src path: got %v, want %q", srcEntry["path"], "src")
		}
		assertContentDirShape(t, srcEntry, "src", "src", "main")

		// Verify docs is a directory
		if docsEntry["type"] != "dir" {
			t.Errorf("docs type: got %v, want %q", docsEntry["type"], "dir")
		}

		// Test listing src directory - should return array with main.go and utils
		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-contents-dir/contents/src", nil)
		assertStatusCode(t, w, 200)
		srcArr := testharness.DecodeJSONArray(t, w)
		if len(srcArr) != 2 {
			t.Fatalf("src contents: expected 2 entries (main.go, utils), got %d", len(srcArr))
		}

		// Find main.go and utils in src
		var mainGoEntry, utilsEntry map[string]any
		for _, item := range srcArr {
			name, _ := item["name"].(string)
			if name == "main.go" {
				mainGoEntry = item
			} else if name == "utils" {
				utilsEntry = item
			}
		}

		if mainGoEntry == nil {
			t.Fatal("src contents: missing main.go entry")
		}
		if utilsEntry == nil {
			t.Fatal("src contents: missing utils entry")
		}

		// Verify main.go is a file
		if mainGoEntry["type"] != "file" {
			t.Errorf("main.go type: got %v, want %q", mainGoEntry["type"], "file")
		}
		if mainGoEntry["name"] != "main.go" {
			t.Errorf("main.go name: got %v, want %q", mainGoEntry["name"], "main.go")
		}
		if mainGoEntry["path"] != "src/main.go" {
			t.Errorf("main.go path: got %v, want %q", mainGoEntry["path"], "src/main.go")
		}
		assertContentFileShape(t, mainGoEntry, "main.go", "src/main.go", "main", false)
		// File should have size > 0
		size, ok := mainGoEntry["size"].(float64)
		if !ok || size == 0 {
			t.Errorf("main.go size: expected positive number, got %v", mainGoEntry["size"])
		}
		// File should have sha
		if mainGoEntry["sha"] == nil || mainGoEntry["sha"] == "" {
			t.Errorf("main.go sha: expected non-empty sha")
		}

		// Verify utils is a directory
		if utilsEntry["type"] != "dir" {
			t.Errorf("utils type: got %v, want %q", utilsEntry["type"], "dir")
		}
		assertContentDirShape(t, utilsEntry, "utils", "src/utils", "main")

		// Test listing src/utils directory - should return array with helper.go
		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-contents-dir/contents/src/utils", nil)
		assertStatusCode(t, w, 200)
		utilsArr := testharness.DecodeJSONArray(t, w)
		if len(utilsArr) != 1 {
			t.Fatalf("utils contents: expected 1 entry (helper.go), got %d", len(utilsArr))
		}

		helperEntry := utilsArr[0]
		if helperEntry["type"] != "file" {
			t.Errorf("helper.go type: got %v, want %q", helperEntry["type"], "file")
		}
		if helperEntry["name"] != "helper.go" {
			t.Errorf("helper.go name: got %v, want %q", helperEntry["name"], "helper.go")
		}
		if helperEntry["path"] != "src/utils/helper.go" {
			t.Errorf("helper.go path: got %v, want %q", helperEntry["path"], "src/utils/helper.go")
		}

		// Test that file requests still work (backward compatibility)
		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-contents-dir/contents/src/main.go", nil)
		assertStatusCode(t, w, 200)
		fileResp := testharness.DecodeJSON(t, w)
		if fileResp["type"] != "file" {
			t.Errorf("file response type: got %v, want %q", fileResp["type"], "file")
		}
		if fileResp["encoding"] != "base64" {
			t.Errorf("file response encoding: got %v, want %q", fileResp["encoding"], "base64")
		}
		if fileResp["content"] == nil {
			t.Errorf("file response content: expected base64 content")
		}
		assertContentFileShape(t, fileResp, "main.go", "src/main.go", "main", true)
	})

	t.Run("GetCommitDiffHeader", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()
		fullName := createGitRepo(t, h, "git-commit-diff", "main", true)

		// Create a commit with a file change
		content := "initial content"
		path := "/api/v3/repos/testuser/git-commit-diff/contents/test.txt"
		w := h.DoRESTJSON(t, "PUT", path, map[string]any{
			"message": "add test file",
			"content": base64.StdEncoding.EncodeToString([]byte(content)),
		})
		assertStatusCode(t, w, 201)

		// Get the new commit SHA
		commits, err := h.Svc.Git.ListCommits(ctx, fullName, 1, nil)
		if err != nil {
			t.Fatalf("list commits: %v", err)
		}
		if len(commits) == 0 {
			t.Fatalf("no commits found")
		}
		commitSHA := commits[0].SHA

		// Test with application/vnd.github.diff header
		req := httptest.NewRequest("GET", "/api/v3/repos/testuser/git-commit-diff/commits/"+commitSHA, nil)
		req.Header.Set("Authorization", "token "+h.Token)
		req.Header.Set("Accept", "application/vnd.github.diff")
		w = httptest.NewRecorder()
		h.Mux.ServeHTTP(w, req)
		assertStatusCode(t, w, 200)

		contentType := w.Header().Get("Content-Type")
		if !strings.Contains(contentType, "text/plain") {
			t.Errorf("Content-Type: got %q, want text/plain", contentType)
		}

		body := w.Body.String()
		// Diff should contain the file change
		if !strings.Contains(body, "test.txt") {
			t.Errorf("diff body should contain test.txt, got: %s", body)
		}
	})

	t.Run("GetCommitDiffHeaderV3", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()
		fullName := createGitRepo(t, h, "git-commit-diff-v3", "main", true)

		// Create a commit with a file change
		content := "initial content"
		path := "/api/v3/repos/testuser/git-commit-diff-v3/contents/test.txt"
		w := h.DoRESTJSON(t, "PUT", path, map[string]any{
			"message": "add test file",
			"content": base64.StdEncoding.EncodeToString([]byte(content)),
		})
		assertStatusCode(t, w, 201)

		// Get the new commit SHA
		commits, err := h.Svc.Git.ListCommits(ctx, fullName, 1, nil)
		if err != nil {
			t.Fatalf("list commits: %v", err)
		}
		if len(commits) == 0 {
			t.Fatalf("no commits found")
		}
		commitSHA := commits[0].SHA

		// Test with application/vnd.github.v3.diff header (GitHub API v3 format)
		req := httptest.NewRequest("GET", "/api/v3/repos/testuser/git-commit-diff-v3/commits/"+commitSHA, nil)
		req.Header.Set("Authorization", "token "+h.Token)
		req.Header.Set("Accept", "application/vnd.github.v3.diff")
		w = httptest.NewRecorder()
		h.Mux.ServeHTTP(w, req)
		assertStatusCode(t, w, 200)

		contentType := w.Header().Get("Content-Type")
		if !strings.Contains(contentType, "text/plain") {
			t.Errorf("Content-Type: got %q, want text/plain", contentType)
		}

		body := w.Body.String()
		// Diff should contain the file change
		if !strings.Contains(body, "test.txt") {
			t.Errorf("diff body should contain test.txt, got: %s", body)
		}
	})

	t.Run("GetCommitNormalJSON", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()
		fullName := createGitRepo(t, h, "git-commit-json", "main", true)

		// Get the initial commit
		commits, err := h.Svc.Git.ListCommits(ctx, fullName, 1, nil)
		if err != nil {
			t.Fatalf("list commits: %v", err)
		}
		if len(commits) == 0 {
			t.Fatalf("no commits found")
		}
		commitSHA := commits[0].SHA

		// Request commit without diff header (normal JSON response)
		w := h.DoREST(t, "GET", "/api/v3/repos/testuser/git-commit-json/commits/"+commitSHA, nil)
		assertStatusCode(t, w, 200)

		contentType := w.Header().Get("Content-Type")
		if !strings.Contains(contentType, "application/json") {
			t.Errorf("Content-Type: got %q, want application/json", contentType)
		}

		body := testharness.DecodeJSON(t, w)
		if body["sha"] != commitSHA {
			t.Errorf("sha: got %v, want %q", body["sha"], commitSHA)
		}
	})

	t.Run("CompareCommits", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()
		fullName := createGitRepo(t, h, "git-compare", "main", true)

		// Create a feature branch with a commit
		headSHA, err := h.Svc.Git.HeadSHA(ctx, fullName, "main")
		if err != nil {
			t.Fatalf("head main: %v", err)
		}

		// Create feature branch
		if err := h.Svc.Git.UpdateRef(ctx, fullName, "refs/heads/feature", headSHA); err != nil {
			t.Fatalf("create feature branch: %v", err)
		}

		// Add a commit to feature branch
		if _, err := h.Svc.Git.WriteFile(ctx, fullName, "feature", "feature.txt", "add feature file", []byte("feature content")); err != nil {
			t.Fatalf("write feature file: %v", err)
		}

		// Test compare endpoint: main...feature
		w := h.DoREST(t, "GET", "/api/v3/repos/testuser/git-compare/compare/main...feature", nil)
		assertStatusCode(t, w, 200)
		body := testharness.DecodeJSON(t, w)

		// Check response structure
		if body["status"] == nil {
			t.Errorf("compare response missing 'status' field")
		}
		if body["ahead_by"] == nil {
			t.Errorf("compare response missing 'ahead_by' field")
		}
		if body["behind_by"] == nil {
			t.Errorf("compare response missing 'behind_by' field")
		}
		if body["total_commits"] == nil {
			t.Errorf("compare response missing 'total_commits' field")
		}
		if body["commits"] == nil {
			t.Errorf("compare response missing 'commits' field")
		}
		if body["files"] == nil {
			t.Errorf("compare response missing 'files' field")
		}

		// Verify files array contains the changed file
		files, ok := body["files"].([]any)
		if !ok {
			t.Fatalf("files: expected array, got %T", body["files"])
		}
		if len(files) == 0 {
			t.Errorf("files: expected at least one file, got empty array")
		} else {
			found := false
			for _, f := range files {
				fm, ok := f.(map[string]any)
				if !ok {
					continue
				}
				if fm["filename"] == "feature.txt" {
					found = true
					if fm["status"] != "modified" {
						t.Errorf("file status: got %v, want %q", fm["status"], "modified")
					}
					break
				}
			}
			if !found {
				t.Errorf("files: expected to find feature.txt, got %v", files)
			}
		}

		// Test compare with same ref (should be identical)
		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-compare/compare/main...main", nil)
		assertStatusCode(t, w, 200)
		body = testharness.DecodeJSON(t, w)
		if body["status"] != "identical" {
			t.Errorf("same ref compare status: got %v, want %q", body["status"], "identical")
		}

		// Test missing revisions
		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-compare/compare/nonexistent...main", nil)
		assertStatusCode(t, w, 200)
		body = testharness.DecodeJSON(t, w)
		// Should return fallback response with empty data
		if body["files"] == nil {
			t.Errorf("fallback response missing 'files' field")
		}
	})

	t.Run("CompareCommitsValidation", func(t *testing.T) {
		h := testharness.New(t)
		createGitRepo(t, h, "git-compare-validation", "main", true)

		// Test missing compare path
		w := h.DoREST(t, "GET", "/api/v3/repos/testuser/git-compare-validation/compare/", nil)
		assertStatusCode(t, w, 422)
		body := testharness.DecodeJSON(t, w)
		if body["message"] == nil {
			t.Errorf("validation response missing 'message' field")
		}

		// Test invalid format (no ...)
		w = h.DoREST(t, "GET", "/api/v3/repos/testuser/git-compare-validation/compare/main", nil)
		assertStatusCode(t, w, 422)
		body = testharness.DecodeJSON(t, w)
		if body["message"] == nil {
			t.Errorf("validation response missing 'message' field")
		}
	})

	// Regression: issue #1297 Bug 1 — matching-refs must match character
	// prefixes across path-component boundaries, not only up to a slash.
	t.Run("MatchingRefsMidComponentPrefix_Issue1297", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()
		fullName := createGitRepo(t, h, "matching-refs-1297", "main", true)

		headSHA, err := h.Svc.Git.HeadSHA(ctx, fullName, "main")
		if err != nil {
			t.Fatalf("head main: %v", err)
		}
		for _, ref := range []string{
			"refs/heads/octoswarm/fix-1-real",
			"refs/heads/octoswarm/fix-2",
			"refs/heads/octoswarm/other",
		} {
			if err := h.Svc.Git.UpdateRef(ctx, fullName, ref, headSHA); err != nil {
				t.Fatalf("create %s: %v", ref, err)
			}
		}

		w := h.DoREST(t, "GET", "/api/v3/repos/"+fullName+"/git/matching-refs/heads/octoswarm/fix-", nil)
		assertStatusCode(t, w, 200)
		arr := testharness.DecodeJSONArray(t, w)
		got := map[string]bool{}
		for _, item := range arr {
			if ref, _ := item["ref"].(string); ref != "" {
				got[ref] = true
			}
		}
		if !got["refs/heads/octoswarm/fix-1-real"] || !got["refs/heads/octoswarm/fix-2"] {
			t.Errorf("mid-component prefix heads/octoswarm/fix- missing expected refs; got %v", got)
		}
		if got["refs/heads/octoswarm/other"] {
			t.Errorf("mid-component prefix leaked non-matching ref refs/heads/octoswarm/other")
		}
	})

	// Regression: issue #1297 Bug 2 — compare must work for refs whose
	// names contain "/" and "-" (real branches), and must return a
	// populated merge_base_commit object.
	t.Run("CompareRefWithSlashAndHyphen_Issue1297", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()
		fullName := createGitRepo(t, h, "compare-1297", "main", true)

		if _, err := h.Svc.Git.WriteFile(ctx, fullName, "main", "base.txt", "seed base branch", []byte("base")); err != nil {
			t.Fatalf("seed main: %v", err)
		}
		mainSHA, err := h.Svc.Git.HeadSHA(ctx, fullName, "main")
		if err != nil {
			t.Fatalf("head main: %v", err)
		}
		if err := h.Svc.Git.UpdateRef(ctx, fullName, "refs/heads/octoswarm/fix-2", mainSHA); err != nil {
			t.Fatalf("create octoswarm/fix-2: %v", err)
		}
		if _, err := h.Svc.Git.WriteFile(ctx, fullName, "octoswarm/fix-2", "feature.txt", "fix-2 work", []byte("fix-2")); err != nil {
			t.Fatalf("write on octoswarm/fix-2: %v", err)
		}

		w := h.DoREST(t, "GET", "/api/v3/repos/"+fullName+"/compare/main...octoswarm/fix-2", nil)
		assertStatusCode(t, w, 200)
		body := testharness.DecodeJSON(t, w)

		if ahead, _ := body["ahead_by"].(float64); ahead < 1 {
			t.Errorf("ahead_by: got %v, want >= 1 (distinct branches)", body["ahead_by"])
		}
		if body["status"] == "identical" {
			t.Errorf("status: got identical for distinct branches")
		}
		commits, _ := body["commits"].([]any)
		if len(commits) == 0 {
			t.Errorf("commits: got empty for distinct branches")
		}
		mb, ok := body["merge_base_commit"].(map[string]any)
		if !ok {
			t.Fatalf("merge_base_commit: expected object, got %T (%v)", body["merge_base_commit"], body["merge_base_commit"])
		}
		if mb["sha"] != mainSHA {
			t.Errorf("merge_base_commit.sha: got %v, want %q", mb["sha"], mainSHA)
		}
		commit, ok := mb["commit"].(map[string]any)
		if !ok {
			t.Fatalf("merge_base_commit.commit: expected object, got %T", mb["commit"])
		}
		committer, ok := commit["committer"].(map[string]any)
		if !ok {
			t.Fatalf("merge_base_commit.commit.committer: expected object, got %T", commit["committer"])
		}
		if date, _ := committer["date"].(string); date == "" {
			t.Errorf("merge_base_commit.commit.committer.date: got empty/null, want real timestamp")
		}
	})

	// Regression for issue #1291: PATCH /git/refs/* must reject a non-
	// fast-forward update when force is omitted or false. Covers both
	// custom namespaces (refs/locks/*) and refs/heads/* — the same
	// CAS contract applies to both per GitHub's REST contract, and
	// the audit-ref CAS workflow on ClawMem depends on it.
	t.Run("PatchRefRejectsNonFastForward_Issue1291", func(t *testing.T) {
		h := testharness.New(t)
		ctx := context.Background()
		fullName := createGitRepo(t, h, "patch-nff", "main", true)

		aSHA, err := h.Svc.Git.HeadSHA(ctx, fullName, "main")
		if err != nil {
			t.Fatalf("head main: %v", err)
		}

		// Two sibling branches at A — diverge with different content
		// so the resulting commits are real siblings, not the same
		// SHA via tree dedup.
		if err := h.Svc.Git.CreateBranch(ctx, fullName, "siblingB", "main"); err != nil {
			t.Fatalf("create siblingB: %v", err)
		}
		if err := h.Svc.Git.CreateBranch(ctx, fullName, "siblingD", "main"); err != nil {
			t.Fatalf("create siblingD: %v", err)
		}
		bSHA, err := h.Svc.Git.WriteFile(ctx, fullName, "siblingB", "b.txt", "B branch", []byte("from B"))
		if err != nil {
			t.Fatalf("write B: %v", err)
		}
		dSHA, err := h.Svc.Git.WriteFile(ctx, fullName, "siblingD", "d.txt", "D branch", []byte("from D"))
		if err != nil {
			t.Fatalf("write D: %v", err)
		}
		if bSHA == dSHA || bSHA == aSHA || dSHA == aSHA {
			t.Fatalf("siblings/parent must all differ: A=%s B=%s D=%s", aSHA, bSHA, dSHA)
		}

		// Create a custom lock ref pointing at B.
		w := h.DoRESTJSON(t, "POST", "/api/v3/repos/"+fullName+"/git/refs", map[string]any{
			"ref": "refs/locks/non-ff-test",
			"sha": bSHA,
		})
		assertStatusCode(t, w, 201)

		// PATCH B → D with force=false: 422, ref unchanged.
		w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/"+fullName+"/git/refs/locks/non-ff-test", map[string]any{
			"sha":   dSHA,
			"force": false,
		})
		assertStatusCode(t, w, 422)
		body := testharness.DecodeJSON(t, w)
		if msg, _ := body["message"].(string); !strings.Contains(msg, "fast forward") {
			t.Errorf("rejection message: got %q, want substring %q", msg, "fast forward")
		}
		w = h.DoREST(t, "GET", "/api/v3/repos/"+fullName+"/git/refs/locks/non-ff-test", nil)
		body = testharness.DecodeJSON(t, w)
		obj, _ := body["object"].(map[string]any)
		if obj["sha"] != bSHA {
			t.Errorf("ref after rejected PATCH: got %v, want %q (B)", obj["sha"], bSHA)
		}

		// PATCH B → D with force=true: 200.
		w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/"+fullName+"/git/refs/locks/non-ff-test", map[string]any{
			"sha":   dSHA,
			"force": true,
		})
		assertStatusCode(t, w, 200)

		// PATCH D → D (same SHA) is a no-op success.
		w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/"+fullName+"/git/refs/locks/non-ff-test", map[string]any{
			"sha": dSHA,
		})
		assertStatusCode(t, w, 200)

		// PATCH D → D' where D' is a fast-forward of D succeeds without force.
		ddSHA, err := h.Svc.Git.WriteFile(ctx, fullName, "siblingD", "d2.txt", "advance D", []byte("more D"))
		if err != nil {
			t.Fatalf("write D': %v", err)
		}
		w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/"+fullName+"/git/refs/locks/non-ff-test", map[string]any{
			"sha": ddSHA,
		})
		assertStatusCode(t, w, 200)

		// Same contract on refs/heads/*: rewind is rejected without force.
		// Point a fresh branch at D', then try to rewind it to A.
		if err := h.Svc.Git.CreateBranch(ctx, fullName, "rewind-target", "siblingD"); err != nil {
			t.Fatalf("create rewind-target: %v", err)
		}
		w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/"+fullName+"/git/refs/heads/rewind-target", map[string]any{
			"sha":   aSHA,
			"force": false,
		})
		assertStatusCode(t, w, 422)
		w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/"+fullName+"/git/refs/heads/rewind-target", map[string]any{
			"sha":   aSHA,
			"force": true,
		})
		assertStatusCode(t, w, 200)
	})
}
