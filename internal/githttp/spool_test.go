package githttp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpoolChunkedBody(t *testing.T) {
	t.Run("under_cap", func(t *testing.T) {
		body := io.NopCloser(strings.NewReader("hello world"))
		rr := httptest.NewRecorder()

		tmp, n, exceeded, err := spoolChunkedBody(context.Background(), rr, body, 1024)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if exceeded {
			t.Fatalf("unexpected exceeded=true")
		}
		defer func() {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}()

		if n != int64(len("hello world")) {
			t.Errorf("size=%d want %d", n, len("hello world"))
		}
		got, err := io.ReadAll(tmp)
		if err != nil {
			t.Fatalf("read spooled: %v", err)
		}
		if string(got) != "hello world" {
			t.Errorf("contents=%q want %q", got, "hello world")
		}
		if rr.Code != http.StatusOK {
			t.Errorf("recorder status=%d want 0/200", rr.Code)
		}
	})

	t.Run("exactly_at_cap", func(t *testing.T) {
		payload := bytes.Repeat([]byte("x"), 64)
		body := io.NopCloser(bytes.NewReader(payload))
		rr := httptest.NewRecorder()

		tmp, n, exceeded, err := spoolChunkedBody(context.Background(), rr, body, int64(len(payload)))
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if exceeded {
			t.Fatalf("exceeded=true for payload equal to cap")
		}
		defer func() {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}()
		if n != int64(len(payload)) {
			t.Errorf("size=%d want %d", n, len(payload))
		}
	})

	t.Run("exceeds_cap", func(t *testing.T) {
		payload := bytes.Repeat([]byte("x"), 2048)
		body := io.NopCloser(bytes.NewReader(payload))
		rr := httptest.NewRecorder()

		tmp, _, exceeded, err := spoolChunkedBody(context.Background(), rr, body, 1024)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !exceeded {
			t.Fatalf("expected exceeded=true")
		}
		if tmp != nil {
			t.Fatalf("expected nil tmp on exceed")
		}
		if rr.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status=%d want %d", rr.Code, http.StatusRequestEntityTooLarge)
		}
	})

	t.Run("read_error_propagates", func(t *testing.T) {
		body := io.NopCloser(&errReader{})
		rr := httptest.NewRecorder()

		_, _, _, err := spoolChunkedBody(context.Background(), rr, body, 1024)
		if err == nil {
			t.Fatalf("expected error from failing reader")
		}
	})
}

func TestSpoolChunkedBodyHonorsSpoolDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GITHTTP_SPOOL_DIR", dir)

	body := io.NopCloser(strings.NewReader("payload"))
	rr := httptest.NewRecorder()
	tmp, _, _, err := spoolChunkedBody(context.Background(), rr, body, 1024)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	gotDir, _ := filepath.Split(tmp.Name())
	wantDir := dir + string(os.PathSeparator)
	if !strings.HasPrefix(gotDir, wantDir) {
		t.Errorf("temp file dir=%q, want prefix %q", gotDir, wantDir)
	}
}

func TestMaxPushBytes(t *testing.T) {
	t.Setenv("GITHTTP_MAX_PUSH_BYTES", "")
	if got := maxPushBytes(); got != defaultMaxPushBytes {
		t.Errorf("default=%d want %d", got, defaultMaxPushBytes)
	}

	t.Setenv("GITHTTP_MAX_PUSH_BYTES", "1024")
	if got := maxPushBytes(); got != 1024 {
		t.Errorf("override=%d want 1024", got)
	}

	t.Setenv("GITHTTP_MAX_PUSH_BYTES", "not-a-number")
	if got := maxPushBytes(); got != defaultMaxPushBytes {
		t.Errorf("invalid override falls back to %d, got %d", defaultMaxPushBytes, got)
	}

	t.Setenv("GITHTTP_MAX_PUSH_BYTES", "0")
	if got := maxPushBytes(); got != defaultMaxPushBytes {
		t.Errorf("zero override falls back to %d, got %d", defaultMaxPushBytes, got)
	}
}

type errReader struct{}

func (*errReader) Read(_ []byte) (int, error) { return 0, io.ErrUnexpectedEOF }
