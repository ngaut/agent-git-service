// Package gitstore manages on-disk bare git repositories using go-git.
package gitstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	gitcfg "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/storage/filesystem"

	"github.com/ngaut/agent-git-service/internal/tenant"
)

const (
	// ZeroSHA is the null/zero commit SHA used when no real SHA is available.
	ZeroSHA = "0000000000000000000000000000000000000000"
	// RefsHeadsPrefix is the fully-qualified prefix for branch refs.
	RefsHeadsPrefix = "refs/heads/"
	// RefsTagsPrefix is the fully-qualified prefix for tag refs.
	RefsTagsPrefix = "refs/tags/"
)

// Store provides access to bare git repositories on the local filesystem.
type Store struct {
	root          string
	requireTenant bool
	defaultTenant string

	repoLocks sync.Map // per-repo mutexes for write operations
}

// repoLock returns a mutex for the given repo, creating one if needed.
func (s *Store) repoLock(ctx context.Context, fullName string) *sync.Mutex {
	lockKey := fullName
	if s.requireTenant {
		t, ok := tenant.FromContext(ctx)
		if !ok {
			t = s.defaultTenant
		}
		if t != "" {
			lockKey = t + "/" + fullName
		}
	}
	v, _ := s.repoLocks.LoadOrStore(lockKey, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// WithRepoLock serializes callers that need a repo-scoped critical section.
func (s *Store) WithRepoLock(ctx context.Context, fullName string, fn func() error) error {
	mu := s.repoLock(ctx, fullName)
	mu.Lock()
	defer mu.Unlock()
	return fn()
}

// Option configures a Store.
type Option func(*Store)

// WithTenantIsolation makes the store resolve repositories under a per-tenant
// subdirectory (GIT_REPO_DIR/{tenant}/...). When enabled, operations require a
// tenant to be present in the context unless a default is configured via
// WithDefaultTenant.
func WithTenantIsolation() Option {
	return func(s *Store) { s.requireTenant = true }
}

// WithDefaultTenant sets a fallback tenant identifier that is used when
// WithTenantIsolation is enabled but the context does not carry a tenant.
//
// This is intended for single-tenant/local deployments (e.g. "default") so
// unauthenticated or background contexts can still resolve a stable physical
// repository root.
func WithDefaultTenant(tenant string) Option {
	return func(s *Store) { s.defaultTenant = tenant }
}

// New creates a Store rooted at dir, creating it if needed.
func New(dir string, opts ...Option) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("gitstore: mkdir %s: %w", dir, err)
	}
	s := &Store{root: dir}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	if s.defaultTenant != "" {
		if err := validateTenantSegment(s.defaultTenant); err != nil {
			return nil, fmt.Errorf("gitstore: default tenant: %w", err)
		}
	}
	return s, nil
}

func validateTenantSegment(t string) error {
	if t == "" {
		return errors.New("empty tenant")
	}
	if t == "." || t == ".." {
		return fmt.Errorf("invalid tenant %q", t)
	}
	if strings.ContainsAny(t, `/\`) {
		return fmt.Errorf("invalid tenant %q", t)
	}
	if strings.Contains(t, "\x00") {
		return fmt.Errorf("invalid tenant %q", t)
	}
	return nil
}

// validateFullName validates the owner/repo fullName format to prevent path traversal.
// fullName must be in the format "owner/repo" where both owner and repo are valid segments.
func validateFullName(fullName string) error {
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid repo name %q (must be in format owner/repo)", fullName)
	}
	owner, repo := parts[0], parts[1]
	if err := validateNameSegment(owner); err != nil {
		return fmt.Errorf("invalid owner %q: %w", owner, err)
	}
	if err := validateNameSegment(repo); err != nil {
		return fmt.Errorf("invalid repo %q: %w", repo, err)
	}
	return nil
}

// validateNameSegment validates a single name segment (owner or repo) to prevent path traversal.
func validateNameSegment(name string) error {
	if name == "" {
		return errors.New("empty name")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("invalid name %q", name)
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid name %q (must not contain path separators)", name)
	}
	if strings.Contains(name, "\x00") {
		return fmt.Errorf("invalid name %q (contains null byte)", name)
	}
	return nil
}

func (s *Store) rootForCtx(ctx context.Context) (string, error) {
	if !s.requireTenant {
		return s.root, nil
	}
	t, ok := tenant.FromContext(ctx)
	if !ok {
		t = s.defaultTenant
	}
	if t == "" {
		return "", errors.New("gitstore: missing tenant in context")
	}
	if err := validateTenantSegment(t); err != nil {
		return "", fmt.Errorf("gitstore: %w", err)
	}
	return filepath.Join(s.root, t), nil
}

// RepoRoot returns the filesystem root that contains bare repositories for the
// active context. In single-DB mode this is the configured root. In multi-tenant
// mode this is the per-tenant subdirectory (GIT_REPO_DIR/{tenant}).
func (s *Store) RepoRoot(ctx context.Context) (string, error) {
	return s.rootForCtx(ctx)
}

// repoPath returns the filesystem path for a repository's bare .git directory.
func (s *Store) repoPath(ctx context.Context, fullName string) (string, error) {
	root, err := s.rootForCtx(ctx)
	if err != nil {
		return "", err
	}
	if err := validateFullName(fullName); err != nil {
		return "", err
	}
	return filepath.Join(root, fullName+".git"), nil
}

// GetRepoPath returns the filesystem path for a repository's bare .git directory.
func (s *Store) GetRepoPath(ctx context.Context, fullName string) (string, error) {
	return s.repoPath(ctx, fullName)
}

// open opens an existing bare git repository.
func (s *Store) open(ctx context.Context, fullName string) (*git.Repository, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return nil, err
	}
	stg := filesystem.NewStorage(osfs.New(dir), nil)
	repo, err := git.Open(stg, nil)
	if err != nil {
		return nil, fmt.Errorf("gitstore: open %s: %w", fullName, err)
	}
	return repo, nil
}

// Exists reports whether a repository exists in the store.
func (s *Store) Exists(ctx context.Context, fullName string) bool {
	p, err := s.repoPath(ctx, fullName)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// ErrNotFound is returned when a repository doesn't exist in the store.
var ErrNotFound = errors.New("repository not found in gitstore")

// SetupConfig sets remote origin URL in the repo config.
func (s *Store) SetupConfig(ctx context.Context, fullName, baseURL string) error {
	repo, err := s.open(ctx, fullName)
	if err != nil {
		return err
	}
	cfg, err := repo.Config()
	if err != nil {
		return err
	}
	cfg.Remotes["origin"] = &gitcfg.RemoteConfig{
		Name: "origin",
		URLs: []string{baseURL + "/" + fullName + ".git"},
	}
	return repo.SetConfig(cfg)
}
