package rest_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/rest"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

func TestIssueTypingStreamBroadcastsToConcurrentViewers(t *testing.T) {
	h := testharness.New(t)
	issue := seedTypingIssue(t, h, "typing-broadcast")
	deps := &rest.Deps{Svc: h.Svc}

	streamA, stopA := openTypingStream(t, deps, h.User, issue.ID)
	defer stopA()
	streamB, stopB := openTypingStream(t, deps, h.User, issue.ID)
	defer stopB()

	assertTypingSnapshot(t, waitForSSE(t, streamA, 2*time.Second), issue.ID, 0)
	assertTypingSnapshot(t, waitForSSE(t, streamB, 2*time.Second), issue.ID, 0)

	w := httptest.NewRecorder()
	req := newAuthedJSONRequest(t, context.Background(), http.MethodPost, fmt.Sprintf("/api/v3/issues/%d/typing", issue.ID), h.User, map[string]string{
		"id": fmt.Sprintf("%d", issue.ID),
	}, map[string]any{
		"typing": true,
	})
	deps.SignalIssueTyping(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("POST /api/v3/issues/%d/typing: expected 204, got %d: %s", issue.ID, w.Code, w.Body.String())
	}

	assertTypingUpdate(t, waitForSSE(t, streamA, 2*time.Second), issue.ID, h.User.Login, true)
	assertTypingUpdate(t, waitForSSE(t, streamB, 2*time.Second), issue.ID, h.User.Login, true)
}

func TestIssueTypingClearsWhenCommentIsCreated(t *testing.T) {
	h := testharness.New(t)
	issue := seedTypingIssue(t, h, "typing-comment-clear")
	deps := &rest.Deps{Svc: h.Svc}

	stream, stop := openTypingStream(t, deps, h.User, issue.ID)
	defer stop()

	assertTypingSnapshot(t, waitForSSE(t, stream, 2*time.Second), issue.ID, 0)

	w := httptest.NewRecorder()
	req := newAuthedJSONRequest(t, context.Background(), http.MethodPost, fmt.Sprintf("/api/v3/issues/%d/typing", issue.ID), h.User, map[string]string{
		"id": fmt.Sprintf("%d", issue.ID),
	}, map[string]any{
		"typing": true,
	})
	deps.SignalIssueTyping(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("POST /api/v3/issues/%d/typing: expected 204, got %d: %s", issue.ID, w.Code, w.Body.String())
	}
	assertTypingUpdate(t, waitForSSE(t, stream, 2*time.Second), issue.ID, h.User.Login, true)

	w = httptest.NewRecorder()
	req = newAuthedJSONRequest(t, context.Background(), http.MethodPost, fmt.Sprintf("/api/v3/repos/%s/issues/%d/comments", issue.Repository.FullName, issue.Number), h.User, map[string]string{
		"owner":  issue.Repository.Owner.Login,
		"repo":   issue.Repository.Name,
		"number": fmt.Sprintf("%d", issue.Number),
	}, map[string]any{
		"body": "hello from typing test",
	})
	deps.CreateIssueComment(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST comment: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	assertTypingUpdate(t, waitForSSE(t, stream, 2*time.Second), issue.ID, h.User.Login, false)
}

type sseMessage struct {
	Event string
	Data  string
}

func seedTypingIssue(t *testing.T, h *testharness.Harness, repoName string) db.Issue {
	t.Helper()

	ctx := context.Background()
	repo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: h.User.Login,
		Name:       repoName,
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	created, err := h.Svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: repo.FullName,
		Title:        "typing conversation",
		AuthorLogin:  h.User.Login,
		Labels:       []string{"type:conversation"},
	})
	if err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	issue, err := h.Svc.GetIssueByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("reload issue: %v", err)
	}
	return issue
}

func openTypingStream(t *testing.T, deps *rest.Deps, viewer db.User, issueID uint) (<-chan sseMessage, func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	req := newAuthedJSONRequest(t, ctx, http.MethodGet, fmt.Sprintf("/api/v3/issues/%d/typing", issueID), viewer, map[string]string{
		"id": fmt.Sprintf("%d", issueID),
	}, nil)

	writer, body := newStreamingResponseWriter()
	go func() {
		defer writer.Close()
		deps.SubscribeIssueTyping(writer, req)
	}()

	out := make(chan sseMessage, 16)
	go readSSE(body, out)

	stop := func() {
		cancel()
		body.Close()
	}

	waitForStreamReady(t, writer, 2*time.Second)
	if writer.StatusCode() != http.StatusOK {
		t.Fatalf("typing stream: expected 200, got %d", writer.StatusCode())
	}
	if ct := writer.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("typing stream: expected text/event-stream, got %q", ct)
	}
	return out, stop
}

type streamingResponseWriter struct {
	header http.Header
	pipeW  *io.PipeWriter

	mu         sync.Mutex
	statusCode int
	wroteCode  bool
	ready      chan struct{}
}

func newStreamingResponseWriter() (*streamingResponseWriter, *io.PipeReader) {
	pipeR, pipeW := io.Pipe()
	return &streamingResponseWriter{
		header: make(http.Header),
		pipeW:  pipeW,
		ready:  make(chan struct{}),
	}, pipeR
}

func (w *streamingResponseWriter) Header() http.Header {
	return w.header
}

func (w *streamingResponseWriter) WriteHeader(statusCode int) {
	w.mu.Lock()
	if !w.wroteCode {
		w.statusCode = statusCode
		w.wroteCode = true
		close(w.ready)
	}
	w.mu.Unlock()
}

func (w *streamingResponseWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.pipeW.Write(p)
}

func (w *streamingResponseWriter) Flush() {}

func (w *streamingResponseWriter) Close() error {
	return w.pipeW.Close()
}

func (w *streamingResponseWriter) StatusCode() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.statusCode
}

func waitForStreamReady(t *testing.T, w *streamingResponseWriter, timeout time.Duration) {
	t.Helper()

	select {
	case <-w.ready:
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for stream headers after %s", timeout)
	}
}

func readSSE(body io.ReadCloser, out chan<- sseMessage) {
	defer close(out)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 1024), 64*1024)

	var (
		event string
		data  []string
	)

	flush := func() {
		if event == "" && len(data) == 0 {
			return
		}
		out <- sseMessage{
			Event: event,
			Data:  strings.Join(data, "\n"),
		}
		event = ""
		data = nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			data = append(data, value)
		}
	}
}

func newAuthedJSONRequest(t *testing.T, baseCtx context.Context, method, target string, viewer db.User, params map[string]string, body any) *http.Request {
	t.Helper()

	var reader io.Reader = bytes.NewReader(nil)
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(payload)
	}

	if baseCtx == nil {
		baseCtx = context.Background()
	}
	req := httptest.NewRequest(method, target, reader)
	req = req.WithContext(service.ContextWithUser(baseCtx, viewer))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if len(params) > 0 {
		rc := chi.NewRouteContext()
		for key, value := range params {
			rc.URLParams.Add(key, value)
		}
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
	}
	return req
}

func waitForSSE(t *testing.T, ch <-chan sseMessage, timeout time.Duration) sseMessage {
	t.Helper()

	select {
	case msg, ok := <-ch:
		if !ok {
			t.Fatal("sse stream closed before event was received")
		}
		return msg
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for sse event after %s", timeout)
		return sseMessage{}
	}
}

func assertTypingSnapshot(t *testing.T, msg sseMessage, issueID uint, wantUsers int) {
	t.Helper()

	if msg.Event != "typing_snapshot" {
		t.Fatalf("expected typing_snapshot event, got %q (%s)", msg.Event, msg.Data)
	}
	var payload service.TypingEnvelope
	if err := json.Unmarshal([]byte(msg.Data), &payload); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if payload.IssueID != issueID {
		t.Fatalf("snapshot issue_id: got %d want %d", payload.IssueID, issueID)
	}
	if len(payload.Users) != wantUsers {
		t.Fatalf("snapshot users: got %d want %d", len(payload.Users), wantUsers)
	}
}

func assertTypingUpdate(t *testing.T, msg sseMessage, issueID uint, login string, typing bool) {
	t.Helper()

	if msg.Event != "typing" {
		t.Fatalf("expected typing event, got %q (%s)", msg.Event, msg.Data)
	}
	var payload service.TypingEnvelope
	if err := json.Unmarshal([]byte(msg.Data), &payload); err != nil {
		t.Fatalf("unmarshal typing event: %v", err)
	}
	if payload.IssueID != issueID {
		t.Fatalf("typing issue_id: got %d want %d", payload.IssueID, issueID)
	}
	if payload.User == nil || payload.User.Login != login {
		t.Fatalf("typing user login: got %+v want %q", payload.User, login)
	}
	if payload.Typing != typing {
		t.Fatalf("typing flag: got %v want %v", payload.Typing, typing)
	}
}
