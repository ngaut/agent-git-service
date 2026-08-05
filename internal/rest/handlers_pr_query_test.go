package rest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	gormlogger "gorm.io/gorm/logger"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/embedding"
	"github.com/ngaut/agent-git-service/internal/githttp"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/graphql"
	"github.com/ngaut/agent-git-service/internal/oauth"
	rest "github.com/ngaut/agent-git-service/internal/rest"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/router"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness/testdb"
)

type queryCounterLogger struct {
	gormlogger.Interface
	count atomic.Int64
}

func newQueryCounterLogger() *queryCounterLogger {
	return &queryCounterLogger{Interface: gormlogger.Discard}
}

func (l *queryCounterLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	l.Interface = l.Interface.LogMode(level)
	return l
}

func (l *queryCounterLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	l.count.Add(1)
	l.Interface.Trace(ctx, begin, fc, err)
}

func (l *queryCounterLogger) Reset() {
	l.count.Store(0)
}

func (l *queryCounterLogger) Count() int {
	return int(l.count.Load())
}

func TestCreatePR_QueryCount(t *testing.T) {
	tmpDir := t.TempDir()
	logger := newQueryCounterLogger()

	gdb, cleanup := testdb.Open(t, testdb.Options{Prefix: "rest_pr_query", Logger: logger})
	t.Cleanup(cleanup)
	if err := db.Migrate(gdb); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatalf("gitstore: %v", err)
	}

	baseURL := "http://localhost:8080"
	prevBase := transform.Base()
	transform.Init(baseURL)
	t.Cleanup(func() { transform.Init(prevBase) })

	svc := &service.Service{
		DB:       gdb,
		Git:      store,
		BaseURL:  baseURL,
		Embedder: embedding.NopEmbedder{},
	}
	user := db.User{Login: "testuser", Name: "testuser", Type: db.TypeUser, SiteAdmin: true}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	tokenValue := "query-token"
	if err := gdb.Create(&db.Token{UserID: user.ID, Value: tokenValue}).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}

	handlers := &rest.Deps{Svc: svc}
	gqlSrv := graphql.NewServer(svc)
	gitHandler := githttp.New(store, svc)
	oauthHandler := oauth.New(svc)
	mux := router.RegisterRoutes(chi.NewRouter(), handlers, gitHandler, gqlSrv, oauthHandler, "http://console.localhost")

	ctx := context.Background()
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: user.Login,
		Name:       "query-count-pr",
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if _, err := svc.Git.WriteFile(ctx, repo.FullName, "feature", "feature.txt", "add feature", []byte("hello feature")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	logger.Reset()

	body, err := json.Marshal(map[string]any{
		"title": "query count PR",
		"body":  "PR body",
		"head":  "feature",
		"base":  "main",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v3/repos/testuser/query-count-pr/pulls", bytes.NewReader(body))
	req.Header.Set("Authorization", "token "+tokenValue)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("CreatePR status = %d, want %d, body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	queryCount := logger.Count()
	t.Logf("CreatePR query count: %d", queryCount)
	if queryCount > 30 {
		t.Fatalf("CreatePR query count = %d, want <= 30", queryCount)
	}
}
