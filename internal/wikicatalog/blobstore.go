package wikicatalog

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"gh-server/internal/randutil"
)

// MaxBodyInlineBytes is the size at or below which a page body is
// stored inline in wiki_pages.body_inline (and the corresponding
// revision row) instead of being persisted to the blob CAS. Bodies of
// this size are common navigation/index pages where one filesystem
// read per request would dominate latency.
const MaxBodyInlineBytes = 4096

// ErrBlobNotFound is returned by BlobStore.Get / Has when no object
// matches the requested SHA.
var ErrBlobNotFound = errors.New("wiki blob not found")

// BlobStore is the content-addressed object store for wiki page
// bodies. It is keyed by the git SHA-1 blob hash so the value returned
// here matches what the legacy git-backed code returned through
// If-Match / ETag, preserving the REST contract.
//
// The store is the wiki equivalent of internal/service/attachment.go's
// on-disk storage. It writes atomically via tmp+rename, skips writes
// for objects that already exist, and refuses path escape attempts.
type BlobStore struct {
	root string
}

// NewBlobStore returns a BlobStore rooted at root/.wikiblobs. The
// directory is created lazily on first Put. An empty root resolves to
// the process working directory at call time (matching the attachment
// store fallback).
func NewBlobStore(root string) *BlobStore {
	return &BlobStore{root: root}
}

// HashContent returns the git SHA-1 blob hash for content, hex-encoded
// lower-case. The framing is the standard git object framing:
//
//	sha1("blob " + decimal(len) + "\0" + content)
//
// Because this is the same hash git computes, blobs uploaded by the
// migration tool match the SHAs the legacy code published via
// If-Match — clients sending stale ETags still see the expected 409s.
func HashContent(content []byte) string {
	h := sha1.New()
	header := "blob " + strconv.Itoa(len(content)) + "\x00"
	_, _ = h.Write([]byte(header))
	_, _ = h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

// Put stores content in the CAS and returns its git blob SHA-1 hash.
// The operation is idempotent: if a file with the computed SHA
// already exists, no write occurs and Put returns the same SHA. The
// store does not maintain reference counts — that is the catalog's
// responsibility inside an ApplyChangeSet transaction.
func (s *BlobStore) Put(_ context.Context, content []byte) (string, error) {
	sha := HashContent(content)
	abs, err := s.absPath(sha)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err == nil {
		return sha, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return "", err
	}
	tmp := abs + ".tmp-" + randutil.Hex(8)
	if err := os.WriteFile(tmp, content, 0o640); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, abs); err != nil {
		_ = os.Remove(tmp)
		// A concurrent Put racing on the same content may have created
		// the destination between our stat and our rename. The
		// rename target already holds the same bytes (content-addressed),
		// so this is success, not failure.
		if _, statErr := os.Stat(abs); statErr == nil {
			return sha, nil
		}
		return "", err
	}
	return sha, nil
}

// Get returns the body content stored under sha.
func (s *BlobStore) Get(_ context.Context, sha string) ([]byte, error) {
	abs, err := s.absPath(sha)
	if err != nil {
		return nil, err
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrBlobNotFound
		}
		return nil, err
	}
	return content, nil
}

// Has reports whether the CAS holds an object for sha.
func (s *BlobStore) Has(_ context.Context, sha string) (bool, error) {
	abs, err := s.absPath(sha)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(abs); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Delete removes the object for sha. Returns nil if it did not exist;
// this is the right behavior for the GC path, where concurrent
// reclamation attempts on the same orphan must not error.
func (s *BlobStore) Delete(_ context.Context, sha string) error {
	abs, err := s.absPath(sha)
	if err != nil {
		return err
	}
	if err := os.Remove(abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Root returns the configured root directory. Useful for tests and
// for surfacing the storage path in admin diagnostics.
func (s *BlobStore) Root() string { return s.resolvedRoot() }

func (s *BlobStore) resolvedRoot() string {
	if s.root != "" {
		return s.root
	}
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		return cwd
	}
	return "."
}

// absPath maps a SHA to its filesystem path under the CAS, fan-out by
// the first two hex prefix pairs (aa/bb/full-sha) so individual
// directories stay small.
func (s *BlobStore) absPath(sha string) (string, error) {
	if err := validateSHA(sha); err != nil {
		return "", err
	}
	return filepath.Join(s.resolvedRoot(), ".wikiblobs", sha[0:2], sha[2:4], sha), nil
}

func validateSHA(sha string) error {
	if len(sha) != 40 {
		return fmt.Errorf("wiki blob sha must be 40 hex characters, got %d", len(sha))
	}
	for i := 0; i < len(sha); i++ {
		c := sha[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return fmt.Errorf("wiki blob sha must be lowercase hex, got %q", sha)
		}
	}
	return nil
}
