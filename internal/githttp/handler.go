// Package githttp serves the Git Smart HTTP protocol backed by system git-http-backend.
// Routes:
//
//	GET  /{owner}/{repo}.git/info/refs?service=git-{upload,receive}-pack
//	POST /{owner}/{repo}.git/git-upload-pack
//	POST /{owner}/{repo}.git/git-receive-pack
package githttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"gh-server/internal/gitstore"
	applog "gh-server/internal/logging"
	"gh-server/internal/rest/respond"
	"gh-server/internal/service"
)

// defaultMaxPushBytes caps a single chunked git push when no explicit override
// is set. Chunked requests must be materialized in full before handing off to
// git-http-backend (the CGI contract requires Content-Length), so an
// unbounded body would translate directly into unbounded disk use.
const defaultMaxPushBytes int64 = 2 * 1024 * 1024 * 1024 // 2 GiB

// Store defines the git storage operations required by the HTTP handler.
type Store interface {
	Exists(ctx context.Context, fullName string) bool
	Init(ctx context.Context, fullName, defaultBranch string, seed bool) error
	GetRepoPath(ctx context.Context, fullName string) (string, error)
	RepoRoot(ctx context.Context) (string, error)
}

// Handler wraps the gitstore for HTTP git protocol serving.
type Handler struct {
	store Store
	Svc   *service.Service
}

type serveRequest struct {
	projectRoot  string
	repoPath     string
	svcName      string
	advertise    bool
	repoFullName string
}

// repoContext holds the resolved repository context for serving git HTTP requests.
type repoContext struct {
	projectRoot   string
	repoPath      string
	repoFullName  string
	defaultBranch string
}

func gitAuthChallenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="GitHub"`)
	respond.Error(w, http.StatusUnauthorized, "Requires authentication")
}

// New creates a new git HTTP handler.
func New(store Store, svc *service.Service) *Handler {
	return &Handler{store: store, Svc: svc}
}

// ensureRepo checks if the repository exists in the gitstore; if not but in DB, inits it.
func (h *Handler) ensureRepo(ctx context.Context, fullName, defaultBranch string) error {
	if h.store.Exists(ctx, fullName) {
		return nil
	}
	if err := h.store.Init(ctx, fullName, defaultBranch, false); err != nil {
		return err
	}
	return nil
}

// resolveRepoContext resolves and prepares repository context for git HTTP serving.
// It handles repo lookup, ensures repo exists, and fetches repo path information.
// Returns repoContext on success, or writes error response and returns false on failure.
func (h *Handler) resolveRepoContext(w http.ResponseWriter, r *http.Request, action string, required service.RepoPermission) (*repoContext, bool) {
	owner := chi.URLParam(r, "owner")
	repo := strings.TrimSuffix(chi.URLParam(r, "repo"), ".git")
	requested := owner + "/" + repo
	applog.AddAttrs(r.Context(), slog.String("repo", requested))

	rep, err := h.Svc.LookupRepoIdentity(r.Context(), requested)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			respond.NotFound(w)
			return nil, false
		}
		slog.ErrorContext(r.Context(), "githttp resolve repo failed", "action", action, "repo", requested, "error", err)
		http.Error(w, "Internal Server Error", 500)
		return nil, false
	}
	fullName := rep.FullName
	applog.AddAttrs(r.Context(), slog.String("repo", fullName))

	allowAnonymousRead := !rep.Private && required.Effective() == service.RepoPermissionRead
	if !allowAnonymousRead {
		viewer, ok := service.UserFromContext(r.Context())
		if !ok {
			gitAuthChallenge(w)
			return nil, false
		}
		perm, err := h.Svc.HasRepoAccess(r.Context(), rep.ID, viewer.ID)
		if err != nil {
			if errors.Is(err, service.ErrNotFound) {
				respond.NotFound(w)
				return nil, false
			}
			slog.ErrorContext(r.Context(), "githttp permission check failed", "action", action, "repo", requested, "error", err)
			http.Error(w, "Internal Server Error", 500)
			return nil, false
		}
		if !perm.AtLeast(required) {
			respond.NotFound(w)
			return nil, false
		}
	}

	if err := h.ensureRepo(r.Context(), fullName, rep.DefaultBranch); err != nil {
		if errors.Is(err, service.ErrNotFound) {
			respond.NotFound(w)
			return nil, false
		}
		slog.ErrorContext(r.Context(), "githttp ensure repo failed", "action", action, "repo", requested, "resolved_repo", fullName, "error", err)
		http.Error(w, "Internal Server Error", 500)
		return nil, false
	}

	repoPath, err := h.store.GetRepoPath(r.Context(), fullName)
	if err != nil {
		slog.ErrorContext(r.Context(), "githttp repo path lookup failed", "action", action, "repo", fullName, "error", err)
		http.Error(w, "Internal Server Error", 500)
		return nil, false
	}

	projectRoot, err := h.store.RepoRoot(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "githttp repo root lookup failed", "action", action, "repo", fullName, "error", err)
		http.Error(w, "Internal Server Error", 500)
		return nil, false
	}

	return &repoContext{
		projectRoot:   projectRoot,
		repoPath:      repoPath,
		repoFullName:  fullName,
		defaultBranch: rep.DefaultBranch,
	}, true
}

// InfoRefs handles GET /{owner}/{repo}.git/info/refs?service=git-*
func (h *Handler) InfoRefs(w http.ResponseWriter, r *http.Request) {
	svc := r.URL.Query().Get("service")
	required := service.RepoPermissionRead
	if svc == "git-receive-pack" {
		required = service.RepoPermissionWrite
	}
	repoCtx, ok := h.resolveRepoContext(w, r, "info/refs", required)
	if !ok {
		return
	}
	if err := h.serve(w, r, serveRequest{
		projectRoot:  repoCtx.projectRoot,
		repoPath:     repoCtx.repoPath,
		svcName:      svc,
		advertise:    true,
		repoFullName: repoCtx.repoFullName,
	}); err != nil {
		slog.ErrorContext(r.Context(), "githttp info refs failed", "repo", repoCtx.repoFullName, "error", err)
		http.Error(w, "Internal Server Error", 500)
	}
}

// UploadPack handles POST /{owner}/{repo}.git/git-upload-pack (clone/fetch)
func (h *Handler) UploadPack(w http.ResponseWriter, r *http.Request) {
	repoCtx, ok := h.resolveRepoContext(w, r, "upload-pack", service.RepoPermissionRead)
	if !ok {
		return
	}
	if err := h.serve(w, r, serveRequest{
		projectRoot:  repoCtx.projectRoot,
		repoPath:     repoCtx.repoPath,
		svcName:      "git-upload-pack",
		advertise:    false,
		repoFullName: repoCtx.repoFullName,
	}); err != nil {
		slog.ErrorContext(r.Context(), "githttp upload-pack failed", "repo", repoCtx.repoFullName, "error", err)
		http.Error(w, "Internal Server Error", 500)
	}
}

// ReceivePack handles POST /{owner}/{repo}.git/git-receive-pack (push)
func (h *Handler) ReceivePack(w http.ResponseWriter, r *http.Request) {
	repoCtx, ok := h.resolveRepoContext(w, r, "receive-pack", service.RepoPermissionWrite)
	if !ok {
		return
	}
	beforeRefs, beforeRefsErr := snapshotRefs(r.Context(), repoCtx.repoPath)
	if beforeRefsErr != nil {
		slog.WarnContext(r.Context(), "githttp snapshot refs before push failed", "repo", repoCtx.repoFullName, "error", beforeRefsErr)
	}
	if rejectOversizedReceivePack(w, r) {
		return
	}
	if err := h.serve(w, r, serveRequest{
		projectRoot:  repoCtx.projectRoot,
		repoPath:     repoCtx.repoPath,
		svcName:      "git-receive-pack",
		advertise:    false,
		repoFullName: repoCtx.repoFullName,
	}); err != nil {
		slog.ErrorContext(r.Context(), "githttp receive-pack failed", "repo", repoCtx.repoFullName, "error", err)
		http.Error(w, "Internal Server Error", 500)
		return
	}
	// After push: fix dangling HEAD and scan for workflows in the background.
	// Propagate the tenant DB (if any) so background writes target the correct database.
	bgCtx := h.Svc.ServerCtx()
	bgCtx = applog.CloneContext(bgCtx, r.Context())
	if tenantDB, ok := service.DBFromContext(r.Context()); ok {
		bgCtx = service.ContextWithDB(bgCtx, tenantDB)
	}
	if u, ok := service.UserFromContext(r.Context()); ok {
		bgCtx = service.ContextWithUser(bgCtx, u)
	}
	applog.AddAttrs(bgCtx, slog.String("repo", repoCtx.repoFullName))
	h.Svc.Wg.Add(1)
	go func() {
		defer h.Svc.Wg.Done()
		fixHEAD(repoCtx.repoPath)
		ctx, cancel := context.WithTimeout(bgCtx, 30*time.Second)
		defer cancel()
		if err := h.handlePostPushWebhooks(ctx, repoCtx.repoFullName, repoCtx.repoPath, beforeRefs); err != nil {
			slog.ErrorContext(ctx, "post-push webhook delivery failed", "error", err)
		}
		if err := h.Svc.SyncWorkflowsFromRepo(ctx, repoCtx.repoFullName); err != nil {
			slog.ErrorContext(ctx, "post-push workflow sync failed", "error", err)
		}
	}()
}

func rejectOversizedReceivePack(w http.ResponseWriter, r *http.Request) bool {
	limit := maxPushBytes()
	if r.ContentLength > limit {
		slog.WarnContext(r.Context(), "githttp push body exceeded limit", "size", r.ContentLength, "limit", limit)
		http.Error(w, "push body exceeds maximum size", http.StatusRequestEntityTooLarge)
		return true
	}
	return false
}

// serve delegates to the system git-http-backend CGI program using net/http/cgi.
func (h *Handler) serve(w http.ResponseWriter, r *http.Request, req serveRequest) error {
	backend, err := findGitHTTPBackend()
	if err != nil {
		return fmt.Errorf("git-http-backend not found: %w", err)
	}

	action := ""
	if strings.HasSuffix(r.URL.Path, "info/refs") {
		action = "info/refs"
	} else if strings.HasSuffix(r.URL.Path, "git-upload-pack") {
		action = "git-upload-pack"
	} else if strings.HasSuffix(r.URL.Path, "git-receive-pack") {
		action = "git-receive-pack"
	}

	// Build PATH_INFO for git-http-backend.
	// The path structure is: /{owner}/{repo}.git/{action}
	owner, repo, ok := strings.Cut(req.repoFullName, "/")
	if !ok || owner == "" || repo == "" {
		return errors.New("invalid repository full name")
	}
	pathInfo := fmt.Sprintf("/%s/%s.git/%s", owner, repo, action)

	env := []string{
		"GIT_PROJECT_ROOT=" + req.projectRoot,
		"GIT_HTTP_EXPORT_ALL=1",
		"PATH_INFO=" + pathInfo,
		"REMOTE_USER=git",
	}

	// Auth is handled by middleware/service before this point. Avoid forwarding
	// bearer credentials to git-http-backend via CGI environment.
	cgiReq := r.Clone(r.Context())
	cgiReq.Header = r.Header.Clone()
	cgiReq.Header.Del("Authorization")

	// git-http-backend runs as a CGI program and requires Content-Length, so we
	// must convert chunked transfer-encoding requests to a sized body. Spool to a
	// temp file instead of RAM so hostile or oversized pushes don't exhaust
	// memory; the cap is configurable via GITHTTP_MAX_PUSH_BYTES and the spool
	// directory via GITHTTP_SPOOL_DIR (for environments where $TMPDIR is tmpfs).
	if hasChunkedTransferEncoding(cgiReq.TransferEncoding) {
		tmp, size, exceeded, err := spoolChunkedBody(r.Context(), w, cgiReq.Body, maxPushBytes())
		if err != nil {
			return fmt.Errorf("spool chunked request body: %w", err)
		}
		if exceeded {
			return nil
		}
		defer func() {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}()
		cgiReq.Body = tmp
		cgiReq.ContentLength = size
		cgiReq.TransferEncoding = nil
		cgiReq.Header.Del("Transfer-Encoding")
	}

	dir, _ := os.Getwd()
	handler := &cgi.Handler{
		Path:       backend,
		Dir:        dir,
		Env:        env,
		InheritEnv: []string{"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "LANG", "LC_ALL"},
	}

	handler.ServeHTTP(w, cgiReq)
	return nil
}

func hasChunkedTransferEncoding(encodings []string) bool {
	for _, encoding := range encodings {
		if strings.EqualFold(encoding, "chunked") {
			return true
		}
	}
	return false
}

// maxPushBytes returns the configured chunked-push size cap.
func maxPushBytes() int64 {
	if env := os.Getenv("GITHTTP_MAX_PUSH_BYTES"); env != "" {
		if n, err := strconv.ParseInt(env, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxPushBytes
}

// pushSpoolDir returns the directory where chunked-push temp files are
// written. Defaults to "" (system tmpdir); set GITHTTP_SPOOL_DIR to force a
// disk-backed location when $TMPDIR is mounted as tmpfs.
func pushSpoolDir() string {
	return os.Getenv("GITHTTP_SPOOL_DIR")
}

// spoolChunkedBody drains a chunked request body into a seekable temp file
// bounded by maxBytes. Returns the spooled file (rewound to offset 0) and its
// size on success — the caller then owns closing and removing the file.
// On overflow, a 413 has already been written to w, the temp file has been
// cleaned up internally, and exceeded=true is returned.
func spoolChunkedBody(ctx context.Context, w http.ResponseWriter, body io.ReadCloser, maxBytes int64) (*os.File, int64, bool, error) {
	tmp, err := os.CreateTemp(pushSpoolDir(), "git-push-*.body")
	if err != nil {
		return nil, 0, false, fmt.Errorf("create temp: %w", err)
	}
	release := tmp
	defer func() {
		if release != nil {
			_ = release.Close()
			_ = os.Remove(release.Name())
		}
	}()

	n, err := io.Copy(tmp, io.LimitReader(body, maxBytes+1))
	_ = body.Close()
	if err != nil {
		return nil, 0, false, fmt.Errorf("spool body: %w", err)
	}
	if n > maxBytes {
		slog.WarnContext(ctx, "githttp push body exceeded limit", "size", n, "limit", maxBytes)
		http.Error(w, "push body exceeds maximum size", http.StatusRequestEntityTooLarge)
		return nil, 0, true, nil
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, 0, false, fmt.Errorf("rewind temp: %w", err)
	}
	release = nil
	return tmp, n, false, nil
}

// fixHEAD updates a bare repo's HEAD to point to the first available branch
// if the current HEAD target doesn't exist. This happens when a repo is created
// with default branch "main" but the client pushes to "master" (or vice versa).
func fixHEAD(repoPath string) {
	// Check if HEAD's target branch exists.
	out, err := exec.Command("git", "-C", repoPath, "symbolic-ref", "HEAD").Output()
	if err != nil {
		return
	}
	headRef := strings.TrimSpace(string(out))
	if _, err := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", headRef).Output(); err == nil {
		return // HEAD is valid
	}
	// HEAD points to a non-existent branch — find the first real branch.
	branchOut, err := exec.Command("git", "-C", repoPath, "for-each-ref", "--format=%(refname)", gitstore.RefsHeadsPrefix, "--count=1").Output()
	if err != nil {
		return
	}
	branch := strings.TrimSpace(string(branchOut))
	if branch == "" {
		return
	}
	_ = exec.Command("git", "-C", repoPath, "symbolic-ref", "HEAD", branch).Run()
}

// findGitHTTPBackend locates the git-http-backend executable.
func findGitHTTPBackend() (string, error) {
	candidates := []string{
		"/usr/lib/git-core/git-http-backend",
		"/usr/libexec/git-core/git-http-backend",
		"/opt/homebrew/libexec/git-core/git-http-backend", // Add homebrew for mac users
	}
	if out, err := exec.Command("git", "--exec-path").Output(); err == nil {
		execPath := strings.TrimSpace(string(out))
		candidates = append([]string{filepath.Join(execPath, "git-http-backend")}, candidates...)
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("not found in common locations")
}
