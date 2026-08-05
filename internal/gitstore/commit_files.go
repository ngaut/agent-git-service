package gitstore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage"
	"golang.org/x/sync/errgroup"
)

// FileMutation describes one path change inside a single commit.
type FileMutation struct {
	Path    string
	Content []byte
	Delete  bool
}

// PreparedCommit identifies Git objects written without publishing the branch
// ref yet. Publishing is a separate CAS step so callers can persist the exact
// commit SHA in another system of record before making it visible to Git
// clients.
type PreparedCommit struct {
	SHA       string
	ParentSHA string

	store      *Store
	repo       *git.Repository
	commitHash plumbing.Hash
	parentHash plumbing.Hash
	fullName   string
	cacheKey   string
	tree       *mutableGitTree
	objects    []encodedGitObject
}

const commitTreeCacheLimit = 16
const gitObjectWriteConcurrency = 8

type encodedObjectWriter interface {
	SetEncodedObject(plumbing.EncodedObject) (plumbing.Hash, error)
}

type commitTreeCacheEntry struct {
	commitSHA plumbing.Hash
	tree      *mutableGitTree
}

// CommitFiles applies a set of file mutations and records them in one commit.
func (s *Store) CommitFiles(ctx context.Context, fullName, branch, message string, changes []FileMutation) (string, error) {
	return s.commitFilesAt(ctx, fullName, branch, message, changes, time.Time{})
}

// CommitFilesAt is like CommitFiles but pins the author and committer
// timestamps to the supplied time. Used by the wiki catalog
// post-commit hook so the materialized git commit's timestamp lines
// up exactly with wiki_changesets.committed_at — otherwise the
// wiki_pages.updated_at the catalog records (sub-second precision)
// and the git commit's timestamp (seconds, taken at exec time) drift
// by milliseconds.
func (s *Store) CommitFilesAt(ctx context.Context, fullName, branch, message string, changes []FileMutation, at time.Time) (string, error) {
	return s.commitFilesAt(ctx, fullName, branch, message, changes, at)
}

// CommitRootEmptyTreeAt creates a root commit with an empty tree on a missing
// branch. It is intended for recovery paths that must materialize an empty
// repository state without deleting any existing branch content.
func (s *Store) CommitRootEmptyTreeAt(ctx context.Context, fullName, branch, message string, at time.Time) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	repo, err := s.open(ctx, fullName)
	if err != nil {
		return "", err
	}
	refName := plumbing.NewBranchReferenceName(branch)
	if _, err := repo.Storer.Reference(refName); err == nil {
		return "", fmt.Errorf("branch %s already exists", branch)
	} else if !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return "", fmt.Errorf("resolve branch %s: %w", branch, err)
	}

	tree, err := loadMutableGitTree(repo.Storer, plumbing.ZeroHash)
	if err != nil {
		return "", err
	}
	treeHash, _, treeObjects, err := tree.prepareWrite()
	if err != nil {
		return "", fmt.Errorf("write empty tree: %w", err)
	}
	commit, err := encodeGitCommit(treeHash, plumbing.ZeroHash, message, at)
	if err != nil {
		return "", fmt.Errorf("write commit: %w", err)
	}
	objects := append(treeObjects, commit)
	cacheKey := fullName + "\x00" + branch
	prepared := PreparedCommit{
		SHA:        commit.hash.String(),
		ParentSHA:  plumbing.ZeroHash.String(),
		store:      s,
		repo:       repo,
		commitHash: commit.hash,
		parentHash: plumbing.ZeroHash,
		fullName:   fullName,
		cacheKey:   cacheKey,
		tree:       tree,
		objects:    objects,
	}
	if err := s.PersistPreparedCommit(ctx, prepared); err != nil {
		return "", err
	}
	if err := s.PublishPreparedCommit(ctx, fullName, branch, prepared); err != nil {
		return "", err
	}
	return prepared.SHA, nil
}

func (s *Store) commitFilesAt(ctx context.Context, fullName, branch, message string, changes []FileMutation, at time.Time) (string, error) {
	prepared, err := s.PrepareCommitFilesAt(ctx, fullName, branch, message, changes, at)
	if err != nil {
		return "", err
	}
	if err := s.PublishPreparedCommit(ctx, fullName, branch, prepared); err != nil {
		return "", err
	}
	return prepared.SHA, nil
}

// PrepareCommitFilesAt writes the blob, tree, and commit objects for changes
// without advancing branch. The returned parent is the branch value observed
// while preparing and is enforced when PublishPreparedCommit runs.
func (s *Store) PrepareCommitFilesAt(ctx context.Context, fullName, branch, message string, changes []FileMutation, at time.Time) (PreparedCommit, error) {
	prepared, err := s.BuildCommitFilesAt(ctx, fullName, branch, message, changes, at)
	if err != nil {
		return PreparedCommit{}, err
	}
	if err := s.PersistPreparedCommit(ctx, prepared); err != nil {
		return PreparedCommit{}, err
	}
	return prepared, nil
}

// BuildCommitFilesAt computes the blob, tree, and commit objects for changes
// without writing them or advancing the branch. Callers that need to overlap
// object persistence with another durable operation must call
// PersistPreparedCommit before PublishPreparedCommit.
func (s *Store) BuildCommitFilesAt(ctx context.Context, fullName, branch, message string, changes []FileMutation, at time.Time) (PreparedCommit, error) {
	return s.buildCommitFilesAt(ctx, fullName, branch, nil, message, changes, at)
}

// BuildCommitFilesAtParent computes a commit against an explicit, already
// persisted parent without requiring that parent to be the published branch
// tip yet. PublishPreparedCommit still performs the branch CAS before exposing
// the result.
func (s *Store) BuildCommitFilesAtParent(ctx context.Context, fullName, branch, parentSHA, message string, changes []FileMutation, at time.Time) (PreparedCommit, error) {
	if !plumbing.IsHash(parentSHA) {
		return PreparedCommit{}, fmt.Errorf("invalid parent commit SHA %q", parentSHA)
	}
	parentHash := plumbing.NewHash(parentSHA)
	return s.buildCommitFilesAt(ctx, fullName, branch, &parentHash, message, changes, at)
}

func (s *Store) buildCommitFilesAt(
	ctx context.Context,
	fullName string,
	branch string,
	explicitParent *plumbing.Hash,
	message string,
	changes []FileMutation,
	at time.Time,
) (PreparedCommit, error) {
	if len(changes) == 0 {
		return PreparedCommit{}, fmt.Errorf("no file changes supplied")
	}
	for _, change := range changes {
		if err := validateTreePath(change.Path); err != nil {
			return PreparedCommit{}, fmt.Errorf("invalid file mutation path %q: %w", change.Path, err)
		}
	}

	repo, err := s.open(ctx, fullName)
	if err != nil {
		return PreparedCommit{}, err
	}

	var parentHash plumbing.Hash
	if explicitParent != nil {
		parentHash = *explicitParent
	} else {
		refName := plumbing.NewBranchReferenceName(branch)
		oldRef, err := repo.Storer.Reference(refName)
		if err != nil && !errors.Is(err, plumbing.ErrReferenceNotFound) {
			return PreparedCommit{}, fmt.Errorf("resolve branch %s: %w", branch, err)
		}
		if err == nil {
			parentHash = oldRef.Hash()
		}
	}

	cacheKey := fullName + "\x00" + branch
	tree := s.takeCommitTree(cacheKey, parentHash)
	if tree != nil {
		// The cached tree already came from this exact parent. Keep the
		// object-presence check without reopening and decoding the commit.
		if parentHash != plumbing.ZeroHash {
			if err := repo.Storer.HasEncodedObject(parentHash); err != nil {
				return PreparedCommit{}, fmt.Errorf("check cached parent commit %s: %w", parentHash, err)
			}
		}
	} else {
		var rootHash plumbing.Hash
		if parentHash != plumbing.ZeroHash {
			parent, err := object.GetCommit(repo.Storer, parentHash)
			if err != nil {
				return PreparedCommit{}, fmt.Errorf("load parent commit %s: %w", parentHash, err)
			}
			rootHash = parent.TreeHash
		}
		tree, err = loadMutableGitTree(repo.Storer, rootHash)
		if err != nil {
			return PreparedCommit{}, err
		}
	}
	objects := make([]encodedGitObject, 0, len(changes)+4)
	for _, change := range changes {
		if err := ctx.Err(); err != nil {
			return PreparedCommit{}, err
		}
		if change.Delete {
			if _, err := tree.apply(strings.Split(change.Path, "/"), plumbing.ZeroHash, true); err != nil {
				return PreparedCommit{}, fmt.Errorf("delete %s: %w", change.Path, err)
			}
			continue
		}
		blob, err := encodeGitBlob(change.Content)
		if err != nil {
			return PreparedCommit{}, fmt.Errorf("write blob for %s: %w", change.Path, err)
		}
		objects = append(objects, blob)
		if _, err := tree.apply(strings.Split(change.Path, "/"), blob.hash, false); err != nil {
			return PreparedCommit{}, fmt.Errorf("upsert %s: %w", change.Path, err)
		}
	}

	newTreeHash, _, treeObjects, err := tree.prepareWrite()
	if err != nil {
		return PreparedCommit{}, fmt.Errorf("write tree: %w", err)
	}
	objects = append(objects, treeObjects...)

	commit, err := encodeGitCommit(newTreeHash, parentHash, message, at)
	if err != nil {
		return PreparedCommit{}, fmt.Errorf("write commit: %w", err)
	}
	objects = append(objects, commit)
	if err := ctx.Err(); err != nil {
		return PreparedCommit{}, err
	}
	return PreparedCommit{
		SHA:        commit.hash.String(),
		ParentSHA:  parentHash.String(),
		store:      s,
		repo:       repo,
		commitHash: commit.hash,
		parentHash: parentHash,
		fullName:   fullName,
		cacheKey:   cacheKey,
		tree:       tree,
		objects:    objects,
	}, nil
}

// PersistPreparedCommit durably writes the objects produced by
// BuildCommitFilesAt without publishing the branch ref.
func (s *Store) PersistPreparedCommit(ctx context.Context, prepared PreparedCommit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if prepared.store != s ||
		prepared.fullName == "" ||
		prepared.commitHash == plumbing.ZeroHash ||
		prepared.SHA != prepared.commitHash.String() ||
		len(prepared.objects) == 0 {
		return errors.New("persist prepared commit: untrusted prepared commit")
	}
	if err := s.persistGitObjects(ctx, prepared.fullName, prepared.objects); err != nil {
		return err
	}
	// Make the persisted tree available to the next linear commit before this
	// commit's ref is published. A branch mismatch discards the entry, and ref
	// publication still validates object existence and performs its normal CAS.
	if prepared.tree != nil && prepared.cacheKey != "" {
		s.storeCommitTree(prepared.cacheKey, prepared.commitHash, prepared.tree)
	}
	return nil
}

// PublishPreparedCommit advances branch to a previously prepared commit if the
// branch still points at the parent observed during preparation.
func (s *Store) PublishPreparedCommit(ctx context.Context, fullName, branch string, prepared PreparedCommit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	commitHash := plumbing.NewHash(prepared.SHA)
	if commitHash == plumbing.ZeroHash {
		return fmt.Errorf("prepared commit SHA is required")
	}
	parentHash := plumbing.NewHash(prepared.ParentSHA)

	refName := plumbing.NewBranchReferenceName(branch)
	cacheKey := fullName + "\x00" + branch
	repo, trusted := prepared.trustedFor(s, cacheKey)
	if trusted {
		commitHash = prepared.commitHash
		parentHash = prepared.parentHash
		if err := repo.Storer.HasEncodedObject(commitHash); err != nil {
			return fmt.Errorf("check prepared commit %s: %w", commitHash, err)
		}
	} else {
		var err error
		repo, err = s.open(ctx, fullName)
		if err != nil {
			return err
		}
		commit, err := loadPreparedCommitForPublish(repo.Storer, commitHash)
		if err != nil {
			return err
		}
		actualParent := plumbing.ZeroHash
		switch len(commit.ParentHashes) {
		case 0:
		case 1:
			actualParent = commit.ParentHashes[0]
		default:
			return fmt.Errorf("prepared commit %s has %d parents, want at most one", commitHash, len(commit.ParentHashes))
		}
		if prepared.ParentSHA != "" && parentHash != actualParent {
			return fmt.Errorf("prepared commit parent is %s, metadata says %s", actualParent, parentHash)
		}
		parentHash = actualParent
	}

	newRef := plumbing.NewHashReference(refName, commitHash)
	var oldRef *plumbing.Reference
	if parentHash != plumbing.ZeroHash {
		oldRef = plumbing.NewHashReference(refName, parentHash)
	}
	if err := repo.Storer.CheckAndSetReference(newRef, oldRef); err != nil {
		if errors.Is(err, storage.ErrReferenceHasChanged) {
			return ErrRefChanged
		}
		return fmt.Errorf("update ref %s: %w", refName, err)
	}
	return nil
}

func loadPreparedCommitForPublish(store storer.EncodedObjectStorer, hash plumbing.Hash) (*object.Commit, error) {
	commit, err := object.GetCommit(store, hash)
	if err != nil {
		return nil, fmt.Errorf("load prepared commit %s: %w", hash, err)
	}
	if err := validateCommitObjectClosure(store, commit); err != nil {
		return nil, fmt.Errorf("validate prepared commit %s: %w", hash, err)
	}
	return commit, nil
}

func validateCommitObjectClosure(store storer.EncodedObjectStorer, commit *object.Commit) error {
	if commit == nil {
		return fmt.Errorf("commit is nil")
	}
	return validateTreeObjectClosure(store, commit.TreeHash)
}

func validateTreeObjectClosure(store storer.EncodedObjectStorer, hash plumbing.Hash) error {
	tree, err := object.GetTree(store, hash)
	if err != nil {
		return fmt.Errorf("load tree %s: %w", hash, err)
	}
	for _, entry := range tree.Entries {
		switch entry.Mode {
		case filemode.Dir:
			if err := validateTreeObjectClosure(store, entry.Hash); err != nil {
				return fmt.Errorf("validate subtree %q: %w", entry.Name, err)
			}
		default:
			if err := store.HasEncodedObject(entry.Hash); err != nil {
				return fmt.Errorf("check tree entry %q %s: %w", entry.Name, entry.Hash, err)
			}
		}
	}
	return nil
}

func (p PreparedCommit) trustedFor(store *Store, cacheKey string) (*git.Repository, bool) {
	trusted := p.store == store &&
		p.repo != nil &&
		p.tree != nil &&
		p.cacheKey == cacheKey &&
		p.commitHash != plumbing.ZeroHash &&
		p.SHA == p.commitHash.String() &&
		p.ParentSHA == p.parentHash.String()
	return p.repo, trusted
}

func (s *Store) takeCommitTree(key string, parent plumbing.Hash) *mutableGitTree {
	s.commitTreeCacheMu.Lock()
	defer s.commitTreeCacheMu.Unlock()
	entry, ok := s.commitTreeCache[key]
	if !ok {
		return nil
	}
	delete(s.commitTreeCache, key)
	s.removeCommitTreeCacheOrder(key)
	if entry.commitSHA != parent {
		return nil
	}
	return entry.tree
}

func (s *Store) storeCommitTree(key string, commit plumbing.Hash, tree *mutableGitTree) {
	if tree == nil {
		return
	}
	s.commitTreeCacheMu.Lock()
	defer s.commitTreeCacheMu.Unlock()
	if s.commitTreeCache == nil {
		s.commitTreeCache = make(map[string]commitTreeCacheEntry, commitTreeCacheLimit)
	}
	if _, exists := s.commitTreeCache[key]; exists {
		s.removeCommitTreeCacheOrder(key)
	}
	for len(s.commitTreeCacheOrder) >= commitTreeCacheLimit {
		oldest := s.commitTreeCacheOrder[0]
		s.commitTreeCacheOrder = s.commitTreeCacheOrder[1:]
		delete(s.commitTreeCache, oldest)
	}
	s.commitTreeCache[key] = commitTreeCacheEntry{commitSHA: commit, tree: tree}
	s.commitTreeCacheOrder = append(s.commitTreeCacheOrder, key)
}

func (s *Store) removeCommitTreeCacheOrder(key string) {
	for i, candidate := range s.commitTreeCacheOrder {
		if candidate != key {
			continue
		}
		copy(s.commitTreeCacheOrder[i:], s.commitTreeCacheOrder[i+1:])
		s.commitTreeCacheOrder = s.commitTreeCacheOrder[:len(s.commitTreeCacheOrder)-1]
		return
	}
}

type mutableGitTree struct {
	store         storer.EncodedObjectStorer
	originalHash  plumbing.Hash
	entries       map[string]object.TreeEntry
	sortedEntries []object.TreeEntry
	children      map[string]*mutableGitTree
	dirty         bool
}

func loadMutableGitTree(store storer.EncodedObjectStorer, hash plumbing.Hash) (*mutableGitTree, error) {
	tree := &mutableGitTree{
		store:        store,
		originalHash: hash,
		entries:      make(map[string]object.TreeEntry),
		children:     make(map[string]*mutableGitTree),
	}
	if hash == plumbing.ZeroHash {
		return tree, nil
	}
	original, err := object.GetTree(store, hash)
	if err != nil {
		return nil, fmt.Errorf("load tree %s: %w", hash, err)
	}
	for _, entry := range original.Entries {
		tree.entries[entry.Name] = entry
	}
	tree.sortedEntries = append(tree.sortedEntries, original.Entries...)
	return tree, nil
}

func (t *mutableGitTree) apply(parts []string, hash plumbing.Hash, deletePath bool) (bool, error) {
	name := parts[0]
	entry, exists := t.entries[name]
	if len(parts) == 1 {
		if deletePath {
			if !exists {
				return false, nil
			}
			if entry.Mode == filemode.Dir {
				return false, fmt.Errorf("%q is a directory", name)
			}
			t.deleteEntry(entry)
			delete(t.children, name)
			t.dirty = true
			return true, nil
		}
		if exists && entry.Mode == filemode.Dir {
			return false, fmt.Errorf("%q is a directory", name)
		}
		if exists && entry.Mode == filemode.Regular && entry.Hash == hash {
			return false, nil
		}
		t.setEntry(object.TreeEntry{
			Name: name,
			Mode: filemode.Regular,
			Hash: hash,
		})
		delete(t.children, name)
		t.dirty = true
		return true, nil
	}

	if exists && entry.Mode != filemode.Dir {
		return false, fmt.Errorf("%q is not a directory", name)
	}
	child := t.children[name]
	if child == nil {
		if !exists {
			if deletePath {
				return false, nil
			}
			var err error
			child, err = loadMutableGitTree(t.store, plumbing.ZeroHash)
			if err != nil {
				return false, err
			}
		} else {
			var err error
			child, err = loadMutableGitTree(t.store, entry.Hash)
			if err != nil {
				return false, err
			}
		}
	}
	changed, err := child.apply(parts[1:], hash, deletePath)
	if err != nil || !changed {
		return changed, err
	}
	t.children[name] = child
	t.dirty = true
	return true, nil
}

func (t *mutableGitTree) prepareWrite() (plumbing.Hash, bool, []encodedGitObject, error) {
	var objects []encodedGitObject
	for name, child := range t.children {
		hash, empty, childObjects, err := child.prepareWrite()
		if err != nil {
			return plumbing.ZeroHash, false, nil, err
		}
		objects = append(objects, childObjects...)
		if empty {
			if entry, ok := t.entries[name]; ok {
				t.deleteEntry(entry)
			}
			continue
		}
		t.setEntry(object.TreeEntry{
			Name: name,
			Mode: filemode.Dir,
			Hash: hash,
		})
	}
	if !t.dirty && t.originalHash != plumbing.ZeroHash {
		return t.originalHash, len(t.entries) == 0, objects, nil
	}

	encoded := t.store.NewEncodedObject()
	if err := (&object.Tree{Entries: t.sortedEntries}).Encode(encoded); err != nil {
		return plumbing.ZeroHash, false, nil, err
	}
	hash := encoded.Hash()
	t.originalHash = hash
	t.dirty = false
	objects = append(objects, encodedGitObject{hash: hash, object: encoded})
	return hash, len(t.sortedEntries) == 0, objects, nil
}

func (t *mutableGitTree) setEntry(entry object.TreeEntry) {
	if previous, ok := t.entries[entry.Name]; ok {
		index := t.entryIndex(previous)
		if index >= 0 {
			if gitTreeEntrySortName(previous) == gitTreeEntrySortName(entry) {
				t.sortedEntries[index] = entry
				t.entries[entry.Name] = entry
				return
			}
			t.deleteEntry(previous)
		}
	}

	key := gitTreeEntrySortName(entry)
	index := sort.Search(len(t.sortedEntries), func(i int) bool {
		return gitTreeEntrySortName(t.sortedEntries[i]) >= key
	})
	t.sortedEntries = append(t.sortedEntries, object.TreeEntry{})
	copy(t.sortedEntries[index+1:], t.sortedEntries[index:])
	t.sortedEntries[index] = entry
	t.entries[entry.Name] = entry
}

func (t *mutableGitTree) deleteEntry(entry object.TreeEntry) {
	index := t.entryIndex(entry)
	if index >= 0 {
		copy(t.sortedEntries[index:], t.sortedEntries[index+1:])
		t.sortedEntries[len(t.sortedEntries)-1] = object.TreeEntry{}
		t.sortedEntries = t.sortedEntries[:len(t.sortedEntries)-1]
	}
	delete(t.entries, entry.Name)
}

func (t *mutableGitTree) entryIndex(entry object.TreeEntry) int {
	key := gitTreeEntrySortName(entry)
	index := sort.Search(len(t.sortedEntries), func(i int) bool {
		return gitTreeEntrySortName(t.sortedEntries[i]) >= key
	})
	if index >= len(t.sortedEntries) || t.sortedEntries[index].Name != entry.Name {
		return -1
	}
	return index
}

func gitTreeEntrySortName(entry object.TreeEntry) string {
	if entry.Mode == filemode.Dir {
		return entry.Name + "/"
	}
	return entry.Name
}

type encodedGitObject struct {
	hash   plumbing.Hash
	object plumbing.EncodedObject
}

func encodeGitBlob(content []byte) (encodedGitObject, error) {
	encoded := &plumbing.MemoryObject{}
	encoded.SetType(plumbing.BlobObject)
	writer, err := encoded.Writer()
	if err != nil {
		return encodedGitObject{}, err
	}
	if _, err := writer.Write(content); err != nil {
		_ = writer.Close()
		return encodedGitObject{}, err
	}
	if err := writer.Close(); err != nil {
		return encodedGitObject{}, err
	}
	hash := encoded.Hash()
	return encodedGitObject{hash: hash, object: encoded}, nil
}

func encodeGitCommit(treeHash, parentHash plumbing.Hash, message string, at time.Time) (encodedGitObject, error) {
	if at.IsZero() {
		at = time.Now()
	}
	if !strings.HasSuffix(message, "\n") {
		message += "\n"
	}
	signature := object.Signature{
		Name:  defaultCommitName,
		Email: defaultCommitEmail,
		When:  at,
	}
	commit := object.Commit{
		Author:    signature,
		Committer: signature,
		Message:   message,
		TreeHash:  treeHash,
	}
	if parentHash != plumbing.ZeroHash {
		commit.ParentHashes = []plumbing.Hash{parentHash}
	}
	encoded := &plumbing.MemoryObject{}
	if err := commit.Encode(encoded); err != nil {
		return encodedGitObject{}, err
	}
	hash := encoded.Hash()
	return encodedGitObject{hash: hash, object: encoded}, nil
}

func (s *Store) persistGitObjects(ctx context.Context, fullName string, objects []encodedGitObject) error {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return err
	}
	writerCount := min(len(objects), gitObjectWriteConcurrency)
	writers := make([]encodedObjectWriter, writerCount)
	for i := range writers {
		writers[i] = newLooseObjectStorer(dir)
	}
	return persistGitObjects(ctx, writers, objects)
}

func persistGitObjects(ctx context.Context, writers []encodedObjectWriter, objects []encodedGitObject) error {
	uniqueHashes := make(map[plumbing.Hash]struct{}, len(objects))
	unique := make([]encodedGitObject, 0, len(objects))
	for _, object := range objects {
		if object.hash == plumbing.ZeroHash || object.object == nil {
			return errors.New("persist git objects: invalid encoded object")
		}
		if _, ok := uniqueHashes[object.hash]; ok {
			continue
		}
		uniqueHashes[object.hash] = struct{}{}
		unique = append(unique, object)
	}
	if len(unique) == 0 {
		return nil
	}
	workerCount := min(len(writers), len(unique))
	if workerCount == 0 {
		return errors.New("persist git objects: no object writers")
	}

	group, groupCtx := errgroup.WithContext(ctx)
	for workerIndex := range workerCount {
		workerIndex := workerIndex
		group.Go(func() error {
			writer := writers[workerIndex]
			if writer == nil {
				return errors.New("persist git objects: nil object writer")
			}
			for objectIndex := workerIndex; objectIndex < len(unique); objectIndex += workerCount {
				if err := groupCtx.Err(); err != nil {
					return err
				}
				object := unique[objectIndex]
				actualHash, err := writer.SetEncodedObject(object.object)
				if err != nil {
					return fmt.Errorf("persist git object %s: %w", object.hash, err)
				}
				if actualHash != object.hash {
					return fmt.Errorf("persist git object: wrote %s, expected %s", actualHash, object.hash)
				}
			}
			return nil
		})
	}
	return group.Wait()
}
