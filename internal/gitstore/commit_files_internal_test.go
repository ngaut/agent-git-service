package gitstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/memory"
)

type observedObjectStorer struct {
	*memory.Storage

	observer  *observedObjectWrites
	storageMu *sync.Mutex
	fail      error
}

type observedObjectWrites struct {
	started chan struct{}
	release chan struct{}
	writes  atomic.Int32
	active  atomic.Int32
	peak    atomic.Int32
}

func (s *observedObjectStorer) SetEncodedObject(object plumbing.EncodedObject) (plumbing.Hash, error) {
	s.observer.writes.Add(1)
	active := s.observer.active.Add(1)
	defer s.observer.active.Add(-1)
	for {
		peak := s.observer.peak.Load()
		if active <= peak || s.observer.peak.CompareAndSwap(peak, active) {
			break
		}
	}
	if s.observer.started != nil {
		s.observer.started <- struct{}{}
	}
	if s.observer.release != nil {
		<-s.observer.release
	}
	if s.fail != nil {
		return plumbing.ZeroHash, s.fail
	}
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	return s.Storage.SetEncodedObject(object)
}

func TestPersistGitObjectsWritesIndependentObjectsConcurrently(t *testing.T) {
	objects := encodedBlobFixtures(t, "one", "two", "three")
	storage := memory.NewStorage()
	var storageMu sync.Mutex
	observer := &observedObjectWrites{
		started: make(chan struct{}, len(objects)),
		release: make(chan struct{}),
	}
	writers := make([]encodedObjectWriter, len(objects))
	for i := range writers {
		writers[i] = &observedObjectStorer{
			Storage:   storage,
			observer:  observer,
			storageMu: &storageMu,
		}
	}
	released := false
	defer func() {
		if !released {
			close(observer.release)
		}
	}()

	done := make(chan error, 1)
	go func() {
		done <- persistGitObjects(context.Background(), writers, objects)
	}()

	for range objects {
		select {
		case <-observer.started:
		case <-time.After(5 * time.Second):
			t.Fatal("object writes did not start concurrently")
		}
	}
	if peak := observer.peak.Load(); peak != int32(len(objects)) {
		t.Fatalf("peak concurrent writes = %d, want %d", peak, len(objects))
	}
	close(observer.release)
	released = true
	if err := <-done; err != nil {
		t.Fatalf("persistGitObjects: %v", err)
	}
	for _, object := range objects {
		if err := storage.HasEncodedObject(object.hash); err != nil {
			t.Fatalf("persisted object %s: %v", object.hash, err)
		}
	}
}

func TestPersistGitObjectsDeduplicatesHashes(t *testing.T) {
	objects := encodedBlobFixtures(t, "same")
	objects = append(objects, objects[0])
	observer := &observedObjectWrites{}
	storer := &observedObjectStorer{
		Storage:   memory.NewStorage(),
		observer:  observer,
		storageMu: &sync.Mutex{},
	}

	if err := persistGitObjects(context.Background(), []encodedObjectWriter{storer}, objects); err != nil {
		t.Fatalf("persistGitObjects: %v", err)
	}
	if writes := observer.writes.Load(); writes != 1 {
		t.Fatalf("object writes = %d, want 1", writes)
	}
}

func TestPersistGitObjectsPropagatesCancellationAndWriteFailure(t *testing.T) {
	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		storer := newObservedObjectStorer(nil)
		err := persistGitObjects(ctx, []encodedObjectWriter{storer}, encodedBlobFixtures(t, "canceled"))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("persistGitObjects error = %v, want context.Canceled", err)
		}
	})

	t.Run("write failure", func(t *testing.T) {
		writeErr := errors.New("write failed")
		storer := newObservedObjectStorer(writeErr)
		err := persistGitObjects(context.Background(), []encodedObjectWriter{storer}, encodedBlobFixtures(t, "failure"))
		if !errors.Is(err, writeErr) {
			t.Fatalf("persistGitObjects error = %v, want %v", err, writeErr)
		}
	})
}

func TestLooseObjectStorerWritesStandardReadOnlyObject(t *testing.T) {
	repoDir := newLooseObjectTestRepo(t)
	object := encodedBlobFixtures(t, "standard loose object")[0]
	storer := newLooseObjectStorer(repoDir)

	gotHash, err := storer.SetEncodedObject(object.object)
	if err != nil {
		t.Fatalf("SetEncodedObject: %v", err)
	}
	if gotHash != object.hash {
		t.Fatalf("SetEncodedObject hash = %s, want %s", gotHash, object.hash)
	}

	hexHash := object.hash.String()
	objectPath := filepath.Join(repoDir, "objects", hexHash[:2], hexHash[2:])
	info, err := os.Stat(objectPath)
	if err != nil {
		t.Fatalf("stat loose object: %v", err)
	}
	if info.Mode().Perm()&0o222 != 0 {
		t.Fatalf("loose object permissions = %o, want read-only", info.Mode().Perm())
	}

	storage := filesystem.NewStorage(osfs.New(repoDir), cache.NewObjectLRUDefault())
	persisted, err := storage.EncodedObject(plumbing.BlobObject, object.hash)
	if err != nil {
		t.Fatalf("read loose object through go-git: %v", err)
	}
	reader, err := persisted.Reader()
	if err != nil {
		t.Fatalf("open persisted object: %v", err)
	}
	body, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		t.Fatalf("read persisted object: %v", readErr)
	}
	if closeErr != nil {
		t.Fatalf("close persisted object: %v", closeErr)
	}
	if got, want := string(body), "standard loose object"; got != want {
		t.Fatalf("persisted body = %q, want %q", got, want)
	}
	assertNoLooseObjectTemps(t, repoDir)
}

func TestLooseObjectStorerPreservesExistingObject(t *testing.T) {
	repoDir := newLooseObjectTestRepo(t)
	object := encodedBlobFixtures(t, "duplicate loose object")[0]
	storer := newLooseObjectStorer(repoDir)

	hexHash := object.hash.String()
	objectPath := filepath.Join(repoDir, "objects", hexHash[:2], hexHash[2:])
	if err := os.MkdirAll(filepath.Dir(objectPath), 0o755); err != nil {
		t.Fatalf("create object fan-out directory: %v", err)
	}
	const existingContents = "pre-existing object contents"
	if err := os.WriteFile(objectPath, []byte(existingContents), 0o444); err != nil {
		t.Fatalf("write existing object: %v", err)
	}
	before, err := os.Stat(objectPath)
	if err != nil {
		t.Fatalf("stat existing object before publication: %v", err)
	}

	gotHash, err := storer.SetEncodedObject(object.object)
	if err != nil {
		t.Fatalf("SetEncodedObject: %v", err)
	}
	if gotHash != object.hash {
		t.Fatalf("SetEncodedObject hash = %s, want %s", gotHash, object.hash)
	}
	after, err := os.Stat(objectPath)
	if err != nil {
		t.Fatalf("stat existing object after publication: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("existing object identity changed during idempotent publication")
	}
	contents, err := os.ReadFile(objectPath)
	if err != nil {
		t.Fatalf("read existing object after publication: %v", err)
	}
	if got := string(contents); got != existingContents {
		t.Fatalf("existing object contents = %q, want %q", got, existingContents)
	}
	assertNoLooseObjectTemps(t, repoDir)
}

func TestLooseObjectStorerPublishesSameObjectConcurrently(t *testing.T) {
	repoDir := newLooseObjectTestRepo(t)
	object := encodedBlobFixtures(t, "concurrent loose object")[0]
	storer := newLooseObjectStorer(repoDir)

	const writerCount = 16
	start := make(chan struct{})
	errCh := make(chan error, writerCount)
	var writers sync.WaitGroup
	for range writerCount {
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			gotHash, err := storer.SetEncodedObject(object.object)
			if err != nil {
				errCh <- err
				return
			}
			if gotHash != object.hash {
				errCh <- fmt.Errorf("SetEncodedObject hash = %s, want %s", gotHash, object.hash)
			}
		}()
	}
	close(start)
	writers.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent SetEncodedObject: %v", err)
	}
	if t.Failed() {
		return
	}

	storage := filesystem.NewStorage(osfs.New(repoDir), cache.NewObjectLRUDefault())
	persisted, err := storage.EncodedObject(plumbing.BlobObject, object.hash)
	if err != nil {
		t.Fatalf("read concurrently published object through go-git: %v", err)
	}
	reader, err := persisted.Reader()
	if err != nil {
		t.Fatalf("open concurrently published object: %v", err)
	}
	body, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		t.Fatalf("read concurrently published object: %v", readErr)
	}
	if closeErr != nil {
		t.Fatalf("close concurrently published object: %v", closeErr)
	}
	if got, want := string(body), "concurrent loose object"; got != want {
		t.Fatalf("concurrently published body = %q, want %q", got, want)
	}
	assertNoLooseObjectTemps(t, repoDir)
}

func TestLooseObjectStorerDoesNotPublishPartialObject(t *testing.T) {
	repoDir := newLooseObjectTestRepo(t)
	object := encodedBlobFixtures(t, "partial loose object")[0]
	writeErr := errors.New("object reader failed")
	failingObject := &failingReaderEncodedObject{
		EncodedObject: object.object,
		err:           writeErr,
	}

	_, err := newLooseObjectStorer(repoDir).SetEncodedObject(failingObject)
	if !errors.Is(err, writeErr) {
		t.Fatalf("SetEncodedObject error = %v, want %v", err, writeErr)
	}
	hexHash := object.hash.String()
	objectPath := filepath.Join(repoDir, "objects", hexHash[:2], hexHash[2:])
	if _, statErr := os.Stat(objectPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial object stat error = %v, want not exist", statErr)
	}
	assertNoLooseObjectTemps(t, repoDir)
}

func newObservedObjectStorer(fail error) *observedObjectStorer {
	return &observedObjectStorer{
		Storage:   memory.NewStorage(),
		observer:  &observedObjectWrites{},
		storageMu: &sync.Mutex{},
		fail:      fail,
	}
}

func encodedBlobFixtures(t *testing.T, bodies ...string) []encodedGitObject {
	t.Helper()
	objects := make([]encodedGitObject, 0, len(bodies))
	for _, body := range bodies {
		object, err := encodeGitBlob([]byte(body))
		if err != nil {
			t.Fatalf("encodeGitBlob: %v", err)
		}
		objects = append(objects, object)
	}
	return objects
}

type failingReaderEncodedObject struct {
	plumbing.EncodedObject
	err error
}

func (o *failingReaderEncodedObject) Reader() (io.ReadCloser, error) {
	return io.NopCloser(failingReader{o.err}), nil
}

type failingReader struct {
	err error
}

func (r failingReader) Read([]byte) (int, error) {
	return 0, r.err
}

func newLooseObjectTestRepo(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, "objects", "pack"), 0o755); err != nil {
		t.Fatalf("create object directories: %v", err)
	}
	return repoDir
}

func assertNoLooseObjectTemps(t *testing.T, repoDir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repoDir, "objects", "pack"))
	if err != nil {
		t.Fatalf("read object temp directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("object temp directory contains %d entries, want empty", len(entries))
	}
}
