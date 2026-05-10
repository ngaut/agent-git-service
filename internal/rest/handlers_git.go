package rest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"gh-server/internal/db"
	"gh-server/internal/gitstore"
	"gh-server/internal/rest/respond"
	"gh-server/internal/rest/transform"
	"gh-server/internal/service"
)

// --- Branches ---

// ListBranches handles GET /api/v3/repos/{owner}/{repo}/branches
func (d *Deps) ListBranches(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	page, perPage := parsePagination(r)
	rep, err := d.Svc.GetRepo(r.Context(), full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	// Read branches from the actual git store
	gitBranches, err := d.Svc.Git.ListBranches(r.Context(), full)
	if err != nil || len(gitBranches) == 0 {
		// Fallback: return only the default branch with a zero SHA
		sha := gitstore.ZeroSHA
		respond.JSON(w, 200, []any{
			transform.Branch(full, rep.DefaultBranch, sha),
		})
		return
	}
	out := make([]any, len(gitBranches))
	for i, b := range gitBranches {
		out[i] = transform.Branch(full, b.Name, b.SHA)
	}
	respond.JSON(w, 200, paginate(w, r, d.Svc.BaseURL, out, page, perPage))
}

// GetBranch handles GET /api/v3/repos/{owner}/{repo}/branches/{branch}
// It also handles GET /api/v3/repos/{owner}/{repo}/branches/{branch}/protection
// by detecting the /protection suffix and delegating to GetBranchProtection.
func (d *Deps) GetBranch(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	// Wildcard captures everything after /branches/, including slashes
	branchPath := chi.URLParam(r, "*")

	if branchPath == "" {
		respond.NotFound(w)
		return
	}

	// Check if this is a branch-protection request. The wildcard route also
	// captures GitHub's protection subresources, such as
	// /protection/required_status_checks.
	if branch, resource, ok := d.resolveBranchProtectionPath(r.Context(), full, branchPath); ok {
		d.getBranchProtectionInternal(w, r, full, branch, resource)
		return
	}

	// Regular branch info request
	branch := branchPath
	if _, err := d.Svc.GetRepo(r.Context(), full); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	sha := gitstore.ZeroSHA
	if s, err := d.Svc.Git.HeadSHA(r.Context(), full, branch); err == nil && s != "" {
		sha = s
	}
	respond.JSON(w, 200, transform.Branch(full, branch, sha))
}

// getBranchProtectionInternal is the internal implementation of branch protection lookup
func (d *Deps) getBranchProtectionInternal(w http.ResponseWriter, r *http.Request, full string, branch string, resource string) {
	repo, err := d.Svc.GetRepo(r.Context(), full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	d.getBranchProtectionResource(w, r, repo, branch, resource)
}

// ListCommits handles GET /api/v3/repos/{owner}/{repo}/commits
// Supports optional query parameter: path=<filepath> to filter commits by path
func (d *Deps) ListCommits(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	page, perPage := parsePagination(r)
	if d.mustGetRepo(w, r) == nil {
		return
	}

	// Parse optional path query parameter
	path := r.URL.Query().Get("path")

	var opts *gitstore.ListCommitsOptions
	if path != "" {
		opts = &gitstore.ListCommitsOptions{Path: path}
	}

	commits, err := d.Svc.Git.ListCommits(r.Context(), full, 30, opts)
	if err != nil {
		respond.JSON(w, 200, []any{})
		return
	}
	out := make([]any, len(commits))
	for i, c := range commits {
		out[i] = transform.Commit(full, c.SHA, transform.CommitMeta{
			Message:    c.Message,
			AuthorName: c.Author,
			Email:      c.Email,
			Date:       c.Date,
			ParentSHAs: c.ParentSHAs,
		})
	}
	respond.JSON(w, 200, paginate(w, r, d.Svc.BaseURL, out, page, perPage))
}

// GetCommit handles GET /api/v3/repos/{owner}/{repo}/commits/{sha}
// Supports Accept: application/vnd.github.diff (or application/vnd.github.v3.diff) for raw diff output.
func (d *Deps) GetCommit(w http.ResponseWriter, r *http.Request) {
	sha := chi.URLParam(r, "sha")
	full := repoFullName(r)
	if d.mustGetRepo(w, r) == nil {
		return
	}

	// Handle diff format request
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/vnd.github") && strings.Contains(accept, "diff") {
		// Get commit details to find parent SHA
		commit, err := d.Svc.Git.GetCommit(r.Context(), full, sha)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		// Generate diff between parent and this commit
		if len(commit.ParentSHAs) > 0 && d.Svc.Git != nil {
			parentSHA := commit.ParentSHAs[0]
			diff, diffErr := d.Svc.Git.DiffRaw(r.Context(), full, parentSHA, sha)
			if diffErr == nil {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(200)
				w.Write([]byte(diff))
				return
			}
		}
		// Fallback: return empty diff
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(200)
		w.Write([]byte(""))
		return
	}

	// Normal JSON response with CommitDetails
	details, err := d.Svc.Git.CommitDetails(r.Context(), full, sha)
	if err != nil {
		commit := transform.Commit(full, sha)
		commit["files"] = []any{}
		respond.JSON(w, 200, commit)
		return
	}

	commit := transform.Commit(full, sha, transform.CommitMeta{
		Message:    details.Commit.Message,
		AuthorName: details.Commit.Author,
		Email:      details.Commit.Email,
		Date:       details.Commit.Date,
		ParentSHAs: details.Commit.ParentSHAs,
	})
	patches := map[string]string{}
	if d.Svc.Git != nil {
		if diffPatches, err := d.Svc.Git.CommitFilePatches(r.Context(), full, sha, details.Commit.ParentSHAs); err == nil {
			patches = diffPatches
		}
	}
	blobSHAs := map[string]string{}
	if d.Svc.Git != nil {
		paths := fileLookupPaths(details.Files)
		if len(paths) > 0 {
			if shaMap, err := d.Svc.Git.BlobSHAs(r.Context(), full, sha, paths); err == nil {
				blobSHAs = shaMap
			}
		}
	}
	files := make([]any, len(details.Files))
	for i, f := range details.Files {
		path := normalizeDiffPath(f.Filename)
		if path == "" {
			path = f.Filename
		}
		patch := patches[path]
		if patch == "" && path != f.Filename {
			patch = patches[f.Filename]
		}
		blobSHA := blobSHAs[path]
		if blobSHA == "" && path != f.Filename {
			blobSHA = blobSHAs[f.Filename]
		}
		blobURL, rawURL, contentsURL := buildFileURLs(full, sha, path)
		files[i] = map[string]any{
			"filename":     f.Filename,
			"status":       f.Status,
			"additions":    f.Additions,
			"deletions":    f.Deletions,
			"changes":      f.Additions + f.Deletions,
			"sha":          blobSHA,
			"blob_url":     blobURL,
			"raw_url":      rawURL,
			"contents_url": contentsURL,
		}
		if patch != "" {
			files[i].(map[string]any)["patch"] = patch
		}
	}
	commit["files"] = files
	respond.JSON(w, 200, commit)
}

// GetRepoContents handles GET /api/v3/repos/{owner}/{repo}/contents/*
// Returns file metadata and base64-encoded content for files,
// or a JSON array of file/directory objects for directories.
func (d *Deps) GetRepoContents(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	rawPath := chi.URLParam(r, "*")
	ref := r.URL.Query().Get("ref")
	path := rawPath
	if rawPath == "/" {
		path = ""
	}
	if strings.Contains(path, "..") || strings.HasPrefix(path, "/") {
		respond.NotFound(w)
		return
	}
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	responseRef := contentsResponseRef(*repo, ref)

	// First, check if the path is a directory
	entries, err := d.Svc.Git.ListDirAtRef(r.Context(), full, path, ref)
	if err == nil {
		if len(entries) > 0 || path == "" {
			// It's a directory - return array of entries
			result := make([]any, len(entries))
			for i, entry := range entries {
				result[i] = contentsEntryJSON(repo.FullName, responseRef, entry)
			}
			respond.JSON(w, 200, result)
			return
		}
		if isDir, dirErr := d.Svc.Git.IsDirAtRef(r.Context(), full, path, ref); dirErr == nil && isDir {
			respond.JSON(w, 200, []any{})
			return
		}
	}

	// Not a directory, try to read as a file
	content, err := d.Svc.Git.ReadFileAtRef(r.Context(), full, path, ref)
	if err != nil {
		respond.NotFound(w)
		return
	}
	blobSHA, _ := d.Svc.Git.BlobSHAAtRef(r.Context(), full, path, ref)

	respond.JSON(w, 200, contentsFileJSON(repo.FullName, responseRef, path, blobSHA, content, true))
}

func contentName(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// PutRepoContents handles PUT /api/v3/repos/{owner}/{repo}/contents/*
// Creates or updates a file in the repository.
func (d *Deps) PutRepoContents(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	path := chi.URLParam(r, "*")
	if path == "" || strings.Contains(path, "..") || strings.HasPrefix(path, "/") {
		respond.ValidationFailed(w, "invalid path")
		return
	}
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	var body struct {
		Message string `json:"message"`
		Content string `json:"content"` // base64-encoded
		Branch  string `json:"branch"`
		SHA     string `json:"sha"` // for updates, the current blob SHA
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	if body.Message == "" {
		body.Message = "Update " + path
	}
	if body.Branch == "" {
		body.Branch = repo.DefaultBranch
	}
	content, err := base64.StdEncoding.DecodeString(body.Content)
	if err != nil {
		respond.ValidationFailed(w, "content must be base64-encoded")
		return
	}
	currentSHA, currentErr := d.Svc.Git.BlobSHAAtRef(r.Context(), full, path, body.Branch)
	isUpdate := currentErr == nil
	body.SHA = strings.TrimSpace(body.SHA)
	if isUpdate && body.SHA == "" {
		respond.ValidationFailed(w, "sha is required when updating an existing file")
		return
	}
	if body.SHA != "" {
		if currentErr != nil {
			respond.Error(w, http.StatusConflict, "sha was supplied but the file does not exist")
			return
		}
		if !strings.EqualFold(body.SHA, currentSHA) {
			respond.Error(w, http.StatusConflict, "sha does not match current file")
			return
		}
	}
	commitSHA, err := d.Svc.Git.WriteFile(r.Context(), full, body.Branch, path, body.Message, content)
	if err != nil {
		respond.Error(w, 422, err.Error())
		return
	}
	blobSHA, _ := d.Svc.Git.BlobSHAAtRef(r.Context(), full, path, body.Branch)
	status := http.StatusCreated
	if isUpdate {
		status = http.StatusOK
	}
	respond.JSON(w, status, map[string]any{
		"content": contentsFileJSON(repo.FullName, body.Branch, path, blobSHA, content, false),
		"commit": map[string]any{
			"sha":     commitSHA,
			"message": body.Message,
		},
	})
}

// DeleteRepoContents handles DELETE /api/v3/repos/{owner}/{repo}/contents/*
// Deletes a file from the repository.
func (d *Deps) DeleteRepoContents(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	path := chi.URLParam(r, "*")
	if path == "" || strings.Contains(path, "..") || strings.HasPrefix(path, "/") {
		respond.ValidationFailed(w, "invalid path")
		return
	}
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	var body struct {
		Message string `json:"message"`
		Branch  string `json:"branch"`
		SHA     string `json:"sha"` // current blob SHA
	}
	if err := decodeBodyStrictOptional(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	if body.Message == "" {
		body.Message = "Delete " + path
	}
	if body.Branch == "" {
		body.Branch = repo.DefaultBranch
	}
	body.SHA = strings.TrimSpace(body.SHA)
	if body.SHA == "" {
		respond.ValidationFailed(w, "sha is required")
		return
	}
	currentSHA, err := d.Svc.Git.BlobSHAAtRef(r.Context(), full, path, body.Branch)
	if err != nil {
		respond.NotFound(w)
		return
	}
	if !strings.EqualFold(body.SHA, currentSHA) {
		respond.Error(w, http.StatusConflict, "sha does not match current file")
		return
	}
	commitSHA, err := d.Svc.Git.DeleteFileFromRepo(r.Context(), full, body.Branch, path, body.Message)
	if err != nil {
		respond.Error(w, 422, err.Error())
		return
	}
	respond.JSON(w, 200, map[string]any{
		"content": nil,
		"commit": map[string]any{
			"sha":     commitSHA,
			"message": body.Message,
		},
	})
}

// GetReadme handles GET /api/v3/repos/{owner}/{repo}/readme
func (d *Deps) GetReadme(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	// Try common README filenames
	for _, name := range []string{"README.md", "README", "readme.md", "README.rst", "README.txt"} {
		content, err := d.Svc.Git.ReadFile(r.Context(), full, name)
		if err == nil {
			blobSHA, _ := d.Svc.Git.BlobSHAAtRef(r.Context(), full, name, "")
			respond.JSON(w, 200, contentsFileJSON(repo.FullName, contentsResponseRef(*repo, ""), name, blobSHA, content, true))
			return
		}
	}
	respond.NotFound(w)
}

func contentsResponseRef(repo db.Repository, ref string) string {
	if strings.TrimSpace(ref) != "" {
		return ref
	}
	if repo.DefaultBranch != "" {
		return repo.DefaultBranch
	}
	return "HEAD"
}

func contentsEntryJSON(repoFullName, ref string, entry gitstore.TreeEntry) map[string]any {
	entryType := "file"
	size := entry.Size
	if entry.Type == "tree" {
		entryType = "dir"
		size = 0
	}
	return contentsMetadataJSON(repoFullName, ref, entry.Path, entry.SHA, entryType, size)
}

func contentsFileJSON(repoFullName, ref, path, sha string, content []byte, includeContent bool) map[string]any {
	out := contentsMetadataJSON(repoFullName, ref, path, sha, "file", int64(len(content)))
	if includeContent {
		out["encoding"] = "base64"
		out["content"] = base64.StdEncoding.EncodeToString(content)
	}
	return out
}

func contentsMetadataJSON(repoFullName, ref, path, sha, entryType string, size int64) map[string]any {
	selfURL := contentsSelfURL(repoFullName, path, ref)
	gitURL := contentsGitURL(repoFullName, sha, entryType)
	htmlURL := contentsHTMLURL(repoFullName, ref, path, entryType)
	var downloadURL any
	if entryType == "file" {
		downloadURL = contentsDownloadURL(repoFullName, ref, path)
	}
	return map[string]any{
		"type":         entryType,
		"name":         contentName(path),
		"path":         path,
		"sha":          sha,
		"size":         size,
		"url":          selfURL,
		"git_url":      gitURL,
		"html_url":     htmlURL,
		"download_url": downloadURL,
		"_links": map[string]any{
			"self": selfURL,
			"git":  gitURL,
			"html": htmlURL,
		},
	}
}

func contentsSelfURL(repoFullName, path, ref string) string {
	base := strings.TrimRight(transform.Base(), "/") + "/api/v3/repos/" + repoFullName + "/contents"
	if path != "" {
		base += "/" + escapeURLPath(path)
	}
	if ref != "" {
		base += "?ref=" + url.QueryEscape(ref)
	}
	return base
}

func contentsGitURL(repoFullName, sha, entryType string) string {
	if sha == "" {
		return ""
	}
	kind := "blobs"
	if entryType == "dir" {
		kind = "trees"
	}
	return strings.TrimRight(transform.Base(), "/") + "/api/v3/repos/" + repoFullName + "/git/" + kind + "/" + sha
}

func contentsHTMLURL(repoFullName, ref, path, entryType string) string {
	kind := "blob"
	if entryType == "dir" {
		kind = "tree"
	}
	return strings.TrimRight(transform.HTMLBase(), "/") + "/" + repoFullName + "/" + kind + "/" + url.PathEscape(ref) + "/" + escapeURLPath(path)
}

func contentsDownloadURL(repoFullName, ref, path string) string {
	return strings.TrimRight(transform.HTMLBase(), "/") + "/" + repoFullName + "/raw/" + url.PathEscape(ref) + "/" + escapeURLPath(path)
}

func escapeURLPath(path string) string {
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

// ListTags handles GET /api/v3/repos/{owner}/{repo}/tags
func (d *Deps) ListTags(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	page, perPage := parsePagination(r)
	if _, err := d.Svc.GetRepo(r.Context(), full); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	tags, err := d.Svc.Git.ListTags(r.Context(), full)
	if err != nil {
		respond.JSON(w, 200, []any{})
		return
	}
	out := make([]any, len(tags))
	for i, t := range tags {
		out[i] = map[string]any{
			"name":        t.Name,
			"commit":      map[string]any{"sha": t.SHA},
			"zipball_url": fmt.Sprintf("%s/api/v3/repos/%s/archive/%s%s.zip", transform.Base(), full, gitstore.RefsTagsPrefix, t.Name),
			"tarball_url": fmt.Sprintf("%s/api/v3/repos/%s/archive/%s%s.tar.gz", transform.Base(), full, gitstore.RefsTagsPrefix, t.Name),
		}
	}
	respond.JSON(w, 200, paginate(w, r, d.Svc.BaseURL, out, page, perPage))
}

func isFullSHA1Hash(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		isDigit := ch >= '0' && ch <= '9'
		isLowerHex := ch >= 'a' && ch <= 'f'
		isUpperHex := ch >= 'A' && ch <= 'F'
		if !isDigit && !isLowerHex && !isUpperHex {
			return false
		}
	}
	return true
}

// CreateTag handles POST /api/v3/repos/{owner}/{repo}/tags
// Creates an annotated tag from {tag_name,message,target_commitish}.
func (d *Deps) CreateTag(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	var body struct {
		TagName         string `json:"tag_name"`
		Message         string `json:"message"`
		TargetCommitish string `json:"target_commitish"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	body.TagName = strings.TrimSpace(body.TagName)
	if body.TagName == "" {
		respond.ValidationFailed(w, "tag_name is required")
		return
	}

	repo, err := d.Svc.GetRepo(r.Context(), full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}

	target := strings.TrimSpace(body.TargetCommitish)
	if target == "" {
		target = repo.DefaultBranch
	}
	sha, err := d.Svc.Git.HeadSHA(r.Context(), full, target)
	if err != nil {
		if !isFullSHA1Hash(target) {
			respond.ValidationFailed(w, fmt.Sprintf("target_commitish %q not found", target))
			return
		}
		sha = target
	}

	message := strings.TrimSpace(body.Message)
	if message == "" {
		message = "Tag " + body.TagName
	}

	existed := false
	if tags, err := d.Svc.Git.ListTags(r.Context(), full); err == nil {
		for _, t := range tags {
			if t.Name == body.TagName {
				existed = true
				break
			}
		}
	}

	if err := d.Svc.Git.CreateTagIfNotExists(r.Context(), full, body.TagName, message, sha); err != nil {
		respond.ValidationFailed(w, err.Error())
		return
	}

	status := http.StatusCreated
	if existed {
		status = http.StatusOK
	}
	respond.JSON(w, status, map[string]any{
		"name":    body.TagName,
		"message": message,
		"commit":  map[string]any{"sha": sha},
	})
}

// CompareCommitsReal handles GET /api/v3/repos/{owner}/{repo}/compare/{base}...{head}
// with real git diff stats.
func (d *Deps) CompareCommitsReal(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	// Wildcard captures "base...head" - parse to extract base and head
	comparePath := chi.URLParam(r, "*")
	if comparePath == "" {
		respond.ValidationFailed(w, "base and head revisions are required (format: base...head)")
		return
	}
	// Split on "..." to get base and head
	parts := strings.SplitN(comparePath, "...", 2)
	if len(parts) != 2 {
		respond.ValidationFailed(w, "invalid compare format, expected base...head")
		return
	}
	base := parts[0]
	head := parts[1]

	if d.mustGetRepo(w, r) == nil {
		return
	}
	result, err := d.Svc.Git.Compare(r.Context(), full, base, head)
	if err != nil {
		// Fallback to basic response if git compare fails (e.g. ref not
		// found). Keeps callers tolerant to missing branches without
		// surfacing a 500, matching the pre-existing contract.
		respond.JSON(w, 200, map[string]any{
			"status": "ahead", "ahead_by": 0, "behind_by": 0,
			"total_commits": 0, "commits": []any{}, "files": []any{},
			"merge_base_commit": nil,
		})
		return
	}

	status := "ahead"
	if result.AheadBy == 0 && result.BehindBy == 0 {
		status = "identical"
	} else if result.AheadBy == 0 {
		status = "behind"
	} else if result.BehindBy > 0 {
		status = "diverged"
	}

	commits := make([]any, len(result.Commits))
	for i, c := range result.Commits {
		commits[i] = transform.Commit(full, c.SHA, transform.CommitMeta{
			Message: c.Message, AuthorName: c.Author, Email: c.Email, Date: c.Date,
		})
	}
	patches := map[string]string{}
	if d.Svc.Git != nil {
		if diffPatches, err := d.Svc.Git.CompareFilePatches(r.Context(), full, base, head); err == nil {
			patches = diffPatches
		}
	}
	blobSHAs := map[string]string{}
	if d.Svc.Git != nil {
		paths := fileLookupPaths(result.Files)
		if len(paths) > 0 {
			if shaMap, err := d.Svc.Git.BlobSHAs(r.Context(), full, head, paths); err == nil {
				blobSHAs = shaMap
			}
		}
	}
	files := make([]any, len(result.Files))
	for i, f := range result.Files {
		path := normalizeDiffPath(f.Filename)
		if path == "" {
			path = f.Filename
		}
		patch := patches[path]
		if patch == "" && path != f.Filename {
			patch = patches[f.Filename]
		}
		blobSHA := blobSHAs[path]
		if blobSHA == "" && path != f.Filename {
			blobSHA = blobSHAs[f.Filename]
		}
		blobURL, rawURL, contentsURL := buildFileURLs(full, head, path)
		files[i] = map[string]any{
			"filename":     f.Filename,
			"status":       f.Status,
			"additions":    f.Additions,
			"deletions":    f.Deletions,
			"changes":      f.Additions + f.Deletions,
			"sha":          blobSHA,
			"blob_url":     blobURL,
			"raw_url":      rawURL,
			"contents_url": contentsURL,
		}
		if patch != "" {
			files[i].(map[string]any)["patch"] = patch
		}
	}

	var mergeBaseCommit any
	if result.MergeBaseSHA != "" {
		if info, cErr := d.Svc.Git.GetCommit(r.Context(), full, result.MergeBaseSHA); cErr == nil && info != nil {
			mergeBaseCommit = transform.Commit(full, info.SHA, transform.CommitMeta{
				Message:    info.Message,
				AuthorName: info.Author,
				Email:      info.Email,
				Date:       info.Date,
				ParentSHAs: info.ParentSHAs,
			})
		}
	}

	respond.JSON(w, 200, map[string]any{
		"status":            status,
		"ahead_by":          result.AheadBy,
		"behind_by":         result.BehindBy,
		"total_commits":     len(result.Commits),
		"commits":           commits,
		"files":             files,
		"merge_base_commit": mergeBaseCommit,
	})
}

func buildFileURLs(repoFullName, ref, path string) (string, string, string) {
	htmlBase := transform.HTMLBase()
	apiBase := transform.Base()
	blobURL := fmt.Sprintf("%s/%s/blob/%s/%s", htmlBase, repoFullName, ref, path)
	rawURL := fmt.Sprintf("%s/%s/raw/%s/%s", htmlBase, repoFullName, ref, path)
	contentsURL := fmt.Sprintf("%s/api/v3/repos/%s/contents/%s?ref=%s", apiBase, repoFullName, path, ref)
	return blobURL, rawURL, contentsURL
}

func fileLookupPaths(files []gitstore.FileInfo) []string {
	seen := make(map[string]struct{})
	for _, f := range files {
		path := normalizeDiffPath(f.Filename)
		if path == "" {
			path = f.Filename
		}
		if path == "" {
			continue
		}
		seen[path] = struct{}{}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	return paths
}

func normalizeDiffPath(path string) string {
	if path == "" {
		return path
	}
	normalized := path
	for {
		start := strings.Index(normalized, "{")
		end := strings.Index(normalized, "}")
		if start == -1 || end == -1 || end < start {
			break
		}
		inside := normalized[start+1 : end]
		parts := strings.SplitN(inside, "=>", 2)
		if len(parts) != 2 {
			break
		}
		replacement := strings.TrimSpace(parts[1])
		normalized = normalized[:start] + replacement + normalized[end+1:]
	}
	if strings.Contains(normalized, "=>") {
		parts := strings.Split(normalized, "=>")
		normalized = strings.TrimSpace(parts[len(parts)-1])
	}
	return normalized
}

// GetContributors handles GET /api/v3/repos/{owner}/{repo}/contributors
func (d *Deps) GetContributors(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	if d.mustGetRepo(w, r) == nil {
		return
	}
	contributors, err := d.Svc.Git.Contributors(r.Context(), full)
	if err != nil {
		respond.JSON(w, 200, []any{})
		return
	}
	out := make([]any, len(contributors))
	for i, c := range contributors {
		// Parse "Name <email>" format
		name := c
		login := c
		if idx := strings.Index(c, " <"); idx >= 0 {
			name = c[:idx]
			login = name
		}
		out[i] = map[string]any{
			"login":         login,
			"contributions": 1,
			"avatar_url":    fmt.Sprintf("%s/avatars/%s", transform.Base(), login),
			"html_url":      fmt.Sprintf("%s/%s", transform.Base(), login),
			"type":          "User",
		}
	}
	respond.JSON(w, 200, out)
}

type gitCommitActorPayload struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Date  string `json:"date"`
}

// GetGitCommit handles GET /api/v3/repos/{owner}/{repo}/git/commits/{sha}
func (d *Deps) GetGitCommit(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	sha := chi.URLParam(r, "sha")
	if d.mustGetRepo(w, r) == nil {
		return
	}

	commit, err := d.Svc.Git.GetGitCommitObject(r.Context(), full, sha)
	if err != nil {
		respond.NotFound(w)
		return
	}

	respond.JSON(w, http.StatusOK, d.gitCommitResponse(r.Context(), full, commit))
}

// CreateGitCommit handles POST /api/v3/repos/{owner}/{repo}/git/commits
func (d *Deps) CreateGitCommit(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}

	var body struct {
		Message   string                 `json:"message"`
		Tree      string                 `json:"tree"`
		Parents   []string               `json:"parents"`
		Author    *gitCommitActorPayload `json:"author"`
		Committer *gitCommitActorPayload `json:"committer"`
		Signature string                 `json:"signature"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		respond.ValidationFailed(w, "message is required")
		return
	}
	if strings.TrimSpace(body.Tree) == "" {
		respond.ValidationFailed(w, "tree is required")
		return
	}

	defaultSig := d.defaultGitCommitSignature(r)
	authorSig := mergeGitCommitSignature(body.Author, defaultSig)
	committerSig := mergeGitCommitSignature(body.Committer, defaultSig)

	parents := make([]string, 0, len(body.Parents))
	for _, parent := range body.Parents {
		if parent = strings.TrimSpace(parent); parent != "" {
			parents = append(parents, parent)
		}
	}

	commit, err := d.Svc.Git.CreateCommitObject(r.Context(), full, gitstore.CreateCommitOptions{
		Message:    body.Message,
		TreeSHA:    strings.TrimSpace(body.Tree),
		ParentSHAs: parents,
		Author:     authorSig,
		Committer:  committerSig,
		Signature:  body.Signature,
	})
	if err != nil {
		respond.ValidationFailed(w, err.Error())
		return
	}

	d.logGitAudit(r.Context(), repo, service.AuditActionGitCommitCreate, full, "sha="+commit.SHA)
	respond.JSON(w, http.StatusCreated, d.gitCommitResponse(r.Context(), full, commit))
}

// GetGitTree handles GET /api/v3/repos/{owner}/{repo}/git/trees/{sha}
func (d *Deps) GetGitTree(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	sha := chi.URLParam(r, "sha")
	if d.mustGetRepo(w, r) == nil {
		return
	}

	recursive := false
	if _, ok := r.URL.Query()["recursive"]; ok {
		recursive = true
	}

	tree, err := d.Svc.Git.GetGitTree(r.Context(), full, sha, recursive)
	if err != nil {
		respond.NotFound(w)
		return
	}

	respond.JSON(w, http.StatusOK, transform.GitTree(full, tree))
}

func (d *Deps) defaultGitCommitSignature(r *http.Request) gitstore.GitSignature {
	now := time.Now().UTC().Format(time.RFC3339)
	sig := gitstore.GitSignature{
		Name:  "gh-server",
		Email: "gh-server@localhost",
		Date:  now,
	}

	user, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		return sig
	}

	if name := strings.TrimSpace(user.Name); name != "" {
		sig.Name = name
	} else if login := strings.TrimSpace(user.Login); login != "" {
		sig.Name = login
	}

	if email := strings.TrimSpace(user.Email); email != "" {
		sig.Email = email
	} else if login := strings.TrimSpace(user.Login); login != "" {
		sig.Email = fmt.Sprintf("%s@users.noreply.localhost", login)
	}

	return sig
}

func mergeGitCommitSignature(payload *gitCommitActorPayload, fallback gitstore.GitSignature) gitstore.GitSignature {
	if payload == nil {
		return fallback
	}

	sig := fallback
	if name := strings.TrimSpace(payload.Name); name != "" {
		sig.Name = name
	}
	if email := strings.TrimSpace(payload.Email); email != "" {
		sig.Email = email
	}
	if date := strings.TrimSpace(payload.Date); date != "" {
		sig.Date = date
	}
	return sig
}

// GetGitRef handles GET /api/v3/repos/{owner}/{repo}/git/refs/heads/{branch}
func (d *Deps) GetGitRef(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	branch := chi.URLParam(r, "*")
	if d.mustGetRepo(w, r) == nil {
		return
	}
	sha, err := d.Svc.Git.HeadSHA(r.Context(), full, branch)
	if err != nil {
		respond.NotFound(w)
		return
	}
	respond.JSON(w, 200, map[string]any{
		"ref": gitstore.RefsHeadsPrefix + branch,
		"object": map[string]any{
			"sha":  sha,
			"type": "commit",
		},
	})
}

// DeleteGitRef handles DELETE /api/v3/repos/{owner}/{repo}/git/refs/heads/{branch}
func (d *Deps) DeleteGitRef(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	branch := chi.URLParam(r, "*")
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	ref := gitstore.RefsHeadsPrefix + branch
	if err := d.Svc.Git.DeleteRef(r.Context(), full, ref); err != nil {
		respond.Error(w, 422, err.Error())
		return
	}
	d.logGitAudit(r.Context(), repo, service.AuditActionGitRefDelete, full, "ref="+ref)
	respond.NoContent(w)
}

// GetGitTagRef handles GET /api/v3/repos/{owner}/{repo}/git/refs/tags/{tag}
func (d *Deps) GetGitTagRef(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	tag := chi.URLParam(r, "*")
	if _, err := d.Svc.GetRepo(r.Context(), full); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	tags, err := d.Svc.Git.ListTags(r.Context(), full)
	if err != nil {
		respond.NotFound(w)
		return
	}
	for _, t := range tags {
		if t.Name == tag {
			respond.JSON(w, 200, map[string]any{
				"ref": gitstore.RefsTagsPrefix + tag,
				"object": map[string]any{
					"sha":  t.SHA,
					"type": "commit",
				},
			})
			return
		}
	}
	respond.NotFound(w)
}

// DeleteGitTagRef handles DELETE /api/v3/repos/{owner}/{repo}/git/refs/tags/{tag}
func (d *Deps) DeleteGitTagRef(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	tag := chi.URLParam(r, "*")
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	ref := gitstore.RefsTagsPrefix + tag
	if err := d.Svc.Git.DeleteRef(r.Context(), full, ref); err != nil {
		respond.Error(w, 422, err.Error())
		return
	}
	respond.NoContent(w)
}

// UpdateGitRef handles PATCH /api/v3/repos/{owner}/{repo}/git/refs/heads/{branch}
func (d *Deps) UpdateGitRef(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	branch := chi.URLParam(r, "*")
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	var body struct {
		SHA   string `json:"sha"`
		Force bool   `json:"force"`
	}
	decodeBody(r, &body)
	ref := gitstore.RefsHeadsPrefix + branch
	if err := d.Svc.Git.UpdateRefSafe(r.Context(), full, ref, body.SHA, body.Force); err != nil {
		if errors.Is(err, gitstore.ErrNonFastForward) {
			respond.Error(w, 422, "Update is not a fast forward")
			return
		}
		if errors.Is(err, gitstore.ErrRefNotFound) {
			respond.NotFound(w)
			return
		}
		respond.Error(w, 422, err.Error())
		return
	}
	d.logGitAudit(r.Context(), repo, service.AuditActionGitRefUpdate, full, "ref="+ref+" sha="+body.SHA)
	respond.JSON(w, 200, map[string]any{
		"ref": ref,
		"object": map[string]any{
			"sha":  body.SHA,
			"type": "commit",
		},
	})
}

func normalizeGenericGitRef(rest string) (string, bool) {
	rest = strings.TrimSpace(rest)
	if rest == "" || rest == "refs" || strings.HasPrefix(rest, "/") {
		return "", false
	}
	return "refs/" + rest, true
}

// GetGitRefGeneric handles GET /api/v3/repos/{owner}/{repo}/git/refs/*
// for any ref namespace outside refs/heads and refs/tags (those have their
// own handlers). Chi evaluates the heads/ and tags/ routes first because
// they're registered with a more specific pattern; anything else falls
// through to this one, which resolves the ref with git show-ref.
func (d *Deps) GetGitRefGeneric(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	refName, ok := normalizeGenericGitRef(chi.URLParam(r, "*"))
	if !ok {
		respond.Error(w, 422, "invalid ref")
		return
	}
	if d.mustGetRepo(w, r) == nil {
		return
	}
	sha, err := d.Svc.Git.LookupRef(r.Context(), full, refName)
	if err != nil {
		respond.NotFound(w)
		return
	}
	respond.JSON(w, 200, transform.GitRef(refName, sha))
}

// UpdateGitRefGeneric handles PATCH /api/v3/repos/{owner}/{repo}/git/refs/*
// for any ref namespace outside refs/heads and refs/tags.
func (d *Deps) UpdateGitRefGeneric(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	refName, ok := normalizeGenericGitRef(chi.URLParam(r, "*"))
	if !ok {
		respond.Error(w, 422, "invalid ref")
		return
	}
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	var body struct {
		SHA   string `json:"sha"`
		Force bool   `json:"force"`
	}
	decodeBody(r, &body)
	if err := d.Svc.Git.UpdateRefSafe(r.Context(), full, refName, body.SHA, body.Force); err != nil {
		if errors.Is(err, gitstore.ErrNonFastForward) {
			respond.Error(w, 422, "Update is not a fast forward")
			return
		}
		if errors.Is(err, gitstore.ErrRefNotFound) {
			respond.NotFound(w)
			return
		}
		respond.Error(w, 422, err.Error())
		return
	}
	d.logGitAudit(r.Context(), repo, service.AuditActionGitRefUpdate, full, "ref="+refName+" sha="+body.SHA)
	respond.JSON(w, 200, transform.GitRef(refName, body.SHA))
}

// DeleteGitRefGeneric handles DELETE /api/v3/repos/{owner}/{repo}/git/refs/*
// for any ref namespace outside refs/heads and refs/tags.
func (d *Deps) DeleteGitRefGeneric(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	refName, ok := normalizeGenericGitRef(chi.URLParam(r, "*"))
	if !ok {
		respond.Error(w, 422, "invalid ref")
		return
	}
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	if err := d.Svc.Git.DeleteRef(r.Context(), full, refName); err != nil {
		respond.Error(w, 422, err.Error())
		return
	}
	d.logGitAudit(r.Context(), repo, service.AuditActionGitRefDelete, full, "ref="+refName)
	respond.NoContent(w)
}

// ListMatchingRefs handles GET /api/v3/repos/{owner}/{repo}/git/matching-refs/*
// Returns every ref whose name begins with the given prefix (git's
// for-each-ref semantics). Matches GitHub's Git Database API contract so
// clients iterating refs/locks/* (or similar custom namespaces) get a
// non-404 response.
func (d *Deps) ListMatchingRefs(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	rest := chi.URLParam(r, "*")
	if d.mustGetRepo(w, r) == nil {
		return
	}
	prefix := "refs/" + rest
	if rest == "" {
		prefix = ""
	}
	refs, err := d.Svc.Git.ListRefsWithPrefix(r.Context(), full, prefix)
	if err != nil {
		respond.JSON(w, 200, []any{})
		return
	}
	out := make([]any, 0, len(refs))
	for _, rf := range refs {
		out = append(out, map[string]any{
			"ref": rf.Ref,
			"object": map[string]any{
				"sha":  rf.SHA,
				"type": "commit",
			},
		})
	}
	respond.JSON(w, 200, out)
}

// CreateGitRef handles POST /api/v3/repos/{owner}/{repo}/git/refs
// Creates a new git reference pointing to the given SHA. Atomic
// create-if-not-exists: returns 422 "Reference already exists" if the ref
// name is already taken, matching github.com's documented contract so
// callers (octokit, go-github, distributed-coordination tools) get the
// signal they need to detect contention.
func (d *Deps) CreateGitRef(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	var body struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	if body.Ref == "" || body.SHA == "" {
		respond.Error(w, 422, "ref and sha are required")
		return
	}
	if err := d.Svc.Git.CreateRef(r.Context(), full, body.Ref, body.SHA); err != nil {
		if errors.Is(err, gitstore.ErrRefAlreadyExists) {
			respond.Error(w, 422, "Reference already exists")
			return
		}
		respond.Error(w, 422, err.Error())
		return
	}
	d.logGitAudit(r.Context(), repo, service.AuditActionGitRefCreate, full, "ref="+body.Ref+" sha="+body.SHA)
	respond.JSON(w, 201, map[string]any{
		"ref": body.Ref,
		"object": map[string]any{
			"sha":  body.SHA,
			"type": "commit",
		},
	})
}

// GetGitBlob handles GET /api/v3/repos/{owner}/{repo}/git/blobs/{sha}.
// Returns the blob's bytes base64-encoded, matching GitHub's REST contract.
func (d *Deps) GetGitBlob(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	sha := chi.URLParam(r, "sha")
	if d.mustGetRepo(w, r) == nil {
		return
	}

	blob, err := d.Svc.Git.GetGitBlob(r.Context(), full, sha)
	if err != nil {
		if errors.Is(err, gitstore.ErrBlobTooLarge) {
			respond.Error(w, http.StatusForbidden, "This blob is too large to retrieve via this API")
			return
		}
		respond.NotFound(w)
		return
	}
	respond.JSON(w, http.StatusOK, transform.GitBlob(full, blob))
}

// CreateGitBlob handles POST /api/v3/repos/{owner}/{repo}/git/blobs.
// Mirrors GitHub's create-blob API: accepts {content, encoding} where
// encoding is "utf-8" (default) or "base64", and returns {sha, url}.
func (d *Deps) CreateGitBlob(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}

	var body struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}

	encoding := strings.TrimSpace(body.Encoding)
	if encoding == "" {
		encoding = "utf-8"
	}

	var raw []byte
	switch encoding {
	case "utf-8":
		raw = []byte(body.Content)
	case "base64":
		decoded, err := base64.StdEncoding.DecodeString(body.Content)
		if err != nil {
			respond.ValidationFailed(w, "invalid base64 content")
			return
		}
		raw = decoded
	default:
		respond.ValidationFailed(w, "unsupported encoding")
		return
	}

	blob, err := d.Svc.Git.CreateBlobObject(r.Context(), full, raw)
	if err != nil {
		respond.ValidationFailed(w, err.Error())
		return
	}

	d.logGitAudit(r.Context(), repo, service.AuditActionGitBlobCreate, full, "sha="+blob.SHA)
	respond.JSON(w, http.StatusCreated, map[string]any{
		"sha": blob.SHA,
		"url": fmt.Sprintf("%s/api/v3/repos/%s/git/blobs/%s", transform.Base(), full, blob.SHA),
	})
}

// GetGitTag handles GET /api/v3/repos/{owner}/{repo}/git/tags/{sha}.
func (d *Deps) GetGitTag(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	sha := chi.URLParam(r, "sha")
	if d.mustGetRepo(w, r) == nil {
		return
	}

	tag, err := d.Svc.Git.GetGitTagObject(r.Context(), full, sha)
	if err != nil {
		respond.NotFound(w)
		return
	}
	respond.JSON(w, http.StatusOK, transform.GitTag(full, tag))
}

// CreateGitTag handles POST /api/v3/repos/{owner}/{repo}/git/tags.
// Mirrors GitHub's annotated tag object API; it does not create refs/tags/*.
func (d *Deps) CreateGitTag(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}

	var body struct {
		Tag     string `json:"tag"`
		Message string `json:"message"`
		Object  string `json:"object"`
		Type    string `json:"type"`
		Tagger  *struct {
			Name  string `json:"name"`
			Email string `json:"email"`
			Date  string `json:"date"`
		} `json:"tagger"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}

	var tagger gitstore.GitSignature
	if body.Tagger != nil {
		tagger = gitstore.GitSignature{
			Name:  body.Tagger.Name,
			Email: body.Tagger.Email,
			Date:  body.Tagger.Date,
		}
	} else if viewer, ok := service.UserFromContext(r.Context()); ok {
		tagger = gitstore.GitSignature{
			Name:  viewer.Login,
			Email: viewer.Email,
		}
	}
	tag, err := d.Svc.Git.CreateTagObject(r.Context(), full, gitstore.CreateTagOptions{
		Tag:       body.Tag,
		Message:   body.Message,
		ObjectSHA: body.Object,
		Type:      body.Type,
		Tagger:    tagger,
	})
	if err != nil {
		respond.ValidationFailed(w, err.Error())
		return
	}
	respond.JSON(w, http.StatusCreated, transform.GitTag(full, tag))
}

// CreateGitTree handles POST /api/v3/repos/{owner}/{repo}/git/trees.
// Accepts {base_tree?, tree: [{path, mode, type, sha|content}]} and
// returns the new tree in the same shape as GET /git/trees/{sha}.
func (d *Deps) CreateGitTree(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}

	var body struct {
		BaseTree string `json:"base_tree"`
		Tree     []struct {
			Path    string               `json:"path"`
			Mode    string               `json:"mode"`
			Type    string               `json:"type"`
			SHA     gitTreeEntrySHAParam `json:"sha"`
			Content *string              `json:"content"`
		} `json:"tree"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	if len(body.Tree) == 0 {
		respond.ValidationFailed(w, "tree must contain at least one entry")
		return
	}

	entries := make([]gitstore.CreateTreeEntryInput, 0, len(body.Tree))
	for _, e := range body.Tree {
		sha := ""
		if e.SHA.Value != nil {
			sha = *e.SHA.Value
		}
		entries = append(entries, gitstore.CreateTreeEntryInput{
			Path:      e.Path,
			Mode:      e.Mode,
			Type:      e.Type,
			SHA:       sha,
			Content:   e.Content,
			DeleteSHA: e.SHA.Set && e.SHA.Value == nil,
		})
	}

	tree, err := d.Svc.Git.CreateTreeObject(r.Context(), full, gitstore.CreateTreeOptions{
		BaseTree: body.BaseTree,
		Entries:  entries,
	})
	if err != nil {
		respond.ValidationFailed(w, err.Error())
		return
	}

	d.logGitAudit(r.Context(), repo, service.AuditActionGitTreeCreate, full, "sha="+tree.SHA)
	respond.JSON(w, http.StatusCreated, transform.GitTree(full, tree))
}

type gitTreeEntrySHAParam struct {
	Set   bool
	Value *string
}

func (p *gitTreeEntrySHAParam) UnmarshalJSON(data []byte) error {
	p.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		p.Value = nil
		return nil
	}
	var v string
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	p.Value = &v
	return nil
}

func (d *Deps) logGitAudit(ctx context.Context, repo *db.Repository, action, repoFullName, details string) {
	if repo == nil {
		return
	}
	ev := service.AuditEvent{
		Action:             action,
		RepositoryFullName: repoFullName,
		Details:            details,
	}
	if repo.Owner.Type == db.TypeOrganization && repo.OwnerID != 0 {
		ev.OrganizationID = &repo.OwnerID
	}
	_ = d.Svc.LogAudit(ctx, ev)
}
