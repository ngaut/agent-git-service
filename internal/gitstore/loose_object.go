package gitstore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/objfile"
)

const looseObjectTempAttempts = 10

type looseObjectStorer struct {
	repoDir string
}

func newLooseObjectStorer(repoDir string) *looseObjectStorer {
	return &looseObjectStorer{repoDir: repoDir}
}

func (s *looseObjectStorer) SetEncodedObject(object plumbing.EncodedObject) (plumbing.Hash, error) {
	if object.Type() == plumbing.OFSDeltaObject || object.Type() == plumbing.REFDeltaObject {
		return plumbing.ZeroHash, plumbing.ErrInvalidType
	}

	expectedHash := object.Hash()
	temp, err := createLooseObjectTemp(filepath.Join(s.repoDir, "objects", "pack"))
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("create loose object temp file: %w", err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()

	actualHash, err := writeLooseObject(temp, object)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if actualHash != expectedHash {
		return plumbing.ZeroHash, fmt.Errorf("write loose object: wrote %s, expected %s", actualHash, expectedHash)
	}

	hexHash := actualHash.String()
	targetPath := filepath.Join(s.repoDir, "objects", hexHash[:2], hexHash[2:])
	// The temp file is already read-only and content-addressed. Publishing it
	// directly avoids per-object existence, repeated directory, and chmod round trips.
	if err := publishLooseObject(tempPath, targetPath); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("publish loose object %s: %w", actualHash, err)
	}
	return actualHash, nil
}

func createLooseObjectTemp(dir string) (*os.File, error) {
	var suffix [16]byte
	for range looseObjectTempAttempts {
		if _, err := rand.Read(suffix[:]); err != nil {
			return nil, err
		}
		path := filepath.Join(dir, "tmp_obj_"+hex.EncodeToString(suffix[:]))
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	return nil, errors.New("could not allocate a unique loose object temp file")
}

func writeLooseObject(file *os.File, object plumbing.EncodedObject) (plumbing.Hash, error) {
	reader, err := object.Reader()
	if err != nil {
		_ = file.Close()
		return plumbing.ZeroHash, fmt.Errorf("open encoded object: %w", err)
	}

	writer := objfile.NewWriter(file)
	if err := writer.WriteHeader(object.Type(), object.Size()); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		_ = file.Close()
		return plumbing.ZeroHash, fmt.Errorf("write loose object header: %w", err)
	}
	_, copyErr := io.Copy(writer, reader)
	readerCloseErr := reader.Close()
	writerCloseErr := writer.Close()
	fileCloseErr := file.Close()
	if copyErr != nil {
		return plumbing.ZeroHash, fmt.Errorf("write loose object body: %w", copyErr)
	}
	if readerCloseErr != nil {
		return plumbing.ZeroHash, fmt.Errorf("close encoded object: %w", readerCloseErr)
	}
	if writerCloseErr != nil {
		return plumbing.ZeroHash, fmt.Errorf("finalize loose object: %w", writerCloseErr)
	}
	if fileCloseErr != nil {
		return plumbing.ZeroHash, fmt.Errorf("close loose object temp file: %w", fileCloseErr)
	}
	return writer.Hash(), nil
}

func publishLooseObject(tempPath, targetPath string) error {
	err := publishLooseObjectNoReplace(tempPath, targetPath)
	if errors.Is(err, os.ErrNotExist) {
		if mkdirErr := os.MkdirAll(filepath.Dir(targetPath), 0o755); mkdirErr != nil {
			return mkdirErr
		}
		err = publishLooseObjectNoReplace(tempPath, targetPath)
	}
	if err == nil || errors.Is(err, os.ErrExist) {
		return nil
	}
	return err
}
