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
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
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
	root string

	repoLocks sync.Map // per-repo mutexes for write operations

	commitTreeCacheMu    sync.Mutex
	commitTreeCache      map[string]commitTreeCacheEntry
	commitTreeCacheOrder []string
}

// repoLock returns a mutex for the given repo, creating one if needed.
func (s *Store) repoLock(ctx context.Context, fullName string) *sync.Mutex {
	lockKey := fullName
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

// New creates a Store rooted at dir, creating it if needed.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("gitstore: mkdir %s: %w", dir, err)
	}
	return &Store{root: dir}, nil
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

// RepoRoot returns the filesystem root that contains bare repositories.
func (s *Store) RepoRoot(ctx context.Context) (string, error) {
	return s.root, nil
}

// repoPath returns the filesystem path for a repository's bare .git directory.
func (s *Store) repoPath(ctx context.Context, fullName string) (string, error) {
	if err := validateFullName(fullName); err != nil {
		return "", err
	}
	return filepath.Join(s.root, fullName+".git"), nil
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
	stg := filesystem.NewStorage(osfs.New(dir), cache.NewObjectLRUDefault())
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
