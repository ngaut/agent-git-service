package rest

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/embedding"
	"gh-server/internal/gitstore"
	"gh-server/internal/rest/transform"
	"gh-server/internal/service"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const benchmarkPRCreateCommits = 24

var benchmarkPRCreateResponseSink map[string]any

func BenchmarkPRCreateResponse(b *testing.B) {
	benchmarks := []struct {
		name  string
		build func(*Deps, *http.Request, db.PullRequest) map[string]any
	}{
		{name: "full", build: (*Deps).prWithExtras},
		{name: "lightweight", build: (*Deps).prWithCreateExtras},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			deps, svc, user := newPRBenchmarkDeps(b)
			transform.Init(svc.BaseURL)
			ctx := context.Background()
			fullName := seedBenchmarkPRCreateRepo(b, svc, user, ctx)
			pr, err := svc.CreatePR(ctx, service.CreatePRInput{
				RepoFullName: fullName,
				Title:        "bench pr",
				Body:         "benchmark body",
				HeadRef:      "feature-heavy",
				BaseRef:      "main",
				AuthorLogin:  user.Login,
			})
			if err != nil {
				b.Fatalf("create seed pr: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v3/repos/%s/pulls", fullName), nil)
			samples := make([]float64, b.N)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				start := time.Now()
				benchmarkPRCreateResponseSink = bm.build(deps, req, pr)
				samples[i] = float64(time.Since(start).Microseconds()) / 1000.0
			}
			b.StopTimer()

			b.ReportMetric(benchmarkMeanMS(samples), "mean_ms")
			b.ReportMetric(benchmarkPercentileMS(samples, 0.95), "p95_ms")
		})
	}
}

func newPRBenchmarkDeps(b *testing.B) (*Deps, *service.Service, db.User) {
	b.Helper()

	tmpDir, err := os.MkdirTemp("", "pr-bench-")
	if err != nil {
		b.Fatalf("temp dir: %v", err)
	}
	b.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	dbPath := filepath.Join(tmpDir, "bench.sqlite")
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		b.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.Exec("PRAGMA busy_timeout = 5000").Error; err != nil {
		b.Fatalf("busy_timeout: %v", err)
	}
	if err := gdb.Exec("PRAGMA journal_mode = WAL").Error; err != nil {
		b.Fatalf("journal_mode: %v", err)
	}
	if err := db.Migrate(gdb); err != nil {
		b.Fatalf("migrate: %v", err)
	}

	sqlDB, err := gdb.DB()
	if err != nil {
		b.Fatalf("sql db: %v", err)
	}
	b.Cleanup(func() { _ = sqlDB.Close() })

	store, err := gitstore.New(tmpDir)
	if err != nil {
		b.Fatalf("gitstore: %v", err)
	}

	svc := &service.Service{
		DB:             gdb,
		Git:            store,
		BaseURL:        "http://localhost:8080",
		AttachmentRoot: tmpDir,
		Embedder:       embedding.NopEmbedder{},
	}
	user := db.User{Login: "benchuser", Name: "benchuser", Type: db.TypeUser, SiteAdmin: true}
	if err := gdb.Create(&user).Error; err != nil {
		b.Fatalf("seed user: %v", err)
	}
	if err := gdb.Create(&db.Token{UserID: user.ID, Value: "bench-token"}).Error; err != nil {
		b.Fatalf("seed token: %v", err)
	}

	return &Deps{Svc: svc}, svc, user
}

func seedBenchmarkPRCreateRepo(b *testing.B, svc *service.Service, user db.User, ctx context.Context) string {
	b.Helper()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: user.Login,
		Name:       "pr-create-bench",
		AutoInit:   true,
	})
	if err != nil {
		b.Fatalf("create repo: %v", err)
	}
	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature-heavy", "main"); err != nil {
		b.Fatalf("create branch: %v", err)
	}
	for i := 0; i < benchmarkPRCreateCommits; i++ {
		path := fmt.Sprintf("bench-%02d.txt", i)
		body := bytes.Repeat([]byte(fmt.Sprintf("commit-%02d\n", i)), 32)
		if _, err := svc.Git.WriteFile(ctx, repo.FullName, "feature-heavy", path, fmt.Sprintf("bench commit %02d", i), body); err != nil {
			b.Fatalf("write file %s: %v", path, err)
		}
	}
	return repo.FullName
}

func benchmarkMeanMS(samples []float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	total := 0.0
	for _, sample := range samples {
		total += sample
	}
	return math.Round((total/float64(len(samples)))*100) / 100
}

func benchmarkPercentileMS(samples []float64, percentile float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	index := int(math.Ceil(percentile*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return math.Round(sorted[index]*100) / 100
}
