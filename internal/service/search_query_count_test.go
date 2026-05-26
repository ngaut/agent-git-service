package service_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"

	sqlite3 "github.com/mattn/go-sqlite3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type queryCounterLogger struct {
	gormlogger.Interface
	count int
}

func newQueryCounterLogger() *queryCounterLogger {
	return &queryCounterLogger{Interface: gormlogger.Discard}
}

func (l *queryCounterLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	l.Interface = l.Interface.LogMode(level)
	return l
}

func (l *queryCounterLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	l.count++
	l.Interface.Trace(ctx, begin, fc, err)
}

func TestSearchIssues_CombinedSearchPreloadsOnce(t *testing.T) {
	driverName := fmt.Sprintf("sqlite3_vec_%d", time.Now().UnixNano())
	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return conn.RegisterFunc("VEC_COSINE_DISTANCE", func(embedding, query string) float64 {
				if embedding == query {
					return 0
				}
				return 1
			}, true)
		},
	})

	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{
		OpenDB: func(dbPath string) (*gorm.DB, error) {
			return gorm.Open(sqlite.Dialector{DriverName: driverName, DSN: dbPath}, &gorm.Config{})
		},
	})
	defer cleanup()
	ctx := context.Background()

	user := db.User{Login: "countuser", Type: db.TypeUser}
	if err := svc.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "countuser", Name: "countrepo", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	milestone := db.Milestone{
		RepositoryID: repo.ID,
		Number:       1,
		Title:        "v1",
		CreatorID:    user.ID,
	}
	if err := svc.DB.Create(&milestone).Error; err != nil {
		t.Fatalf("create milestone: %v", err)
	}
	label := db.Label{RepositoryID: repo.ID, Name: "bug", Color: "ff0000"}
	if err := svc.DB.Create(&label).Error; err != nil {
		t.Fatalf("create label: %v", err)
	}

	iss1, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: repo.FullName,
		Title:        "vector search issue one",
		Body:         "body one",
		AuthorLogin:  user.Login,
	})
	if err != nil {
		t.Fatalf("create issue 1: %v", err)
	}
	iss2, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: repo.FullName,
		Title:        "vector search issue two",
		Body:         "body two",
		AuthorLogin:  user.Login,
	})
	if err != nil {
		t.Fatalf("create issue 2: %v", err)
	}
	if _, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: repo.FullName,
		Title:        "unrelated issue",
		Body:         "other body",
		AuthorLogin:  user.Login,
	}); err != nil {
		t.Fatalf("create issue 3: %v", err)
	}

	if err := svc.DB.Model(&db.Issue{}).Where("id IN ?", []uint{iss1.ID, iss2.ID}).Update("milestone_id", milestone.ID).Error; err != nil {
		t.Fatalf("attach milestone: %v", err)
	}
	for _, issueID := range []uint{iss1.ID, iss2.ID} {
		issue := db.Issue{ID: issueID}
		if err := svc.DB.Model(&issue).Association("Labels").Append(&label); err != nil {
			t.Fatalf("attach label to issue %d: %v", issueID, err)
		}
	}

	db.InitVector(svc.DB, 3)
	vec := "[0.1,0.2,0.3]"
	if err := svc.DB.Model(&db.Issue{}).Where("id IN ?", []uint{iss1.ID, iss2.ID}).Update("embedding", vec).Error; err != nil {
		t.Fatalf("set embeddings: %v", err)
	}

	svc.Embedder = &FakeEmbedder{Vec: []float32{0.1, 0.2, 0.3}}
	counter := newQueryCounterLogger()
	svc.DB = svc.DB.Session(&gorm.Session{Logger: counter})

	got, err := svc.SearchIssues(ctx, "repo:countuser/countrepo vector search")
	if err != nil {
		t.Fatalf("SearchIssues err: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(got))
	}

	seen := make(map[uint]struct{}, len(got))
	for _, issue := range got {
		if _, ok := seen[issue.ID]; ok {
			t.Fatalf("duplicate issue %d returned in combined search", issue.ID)
		}
		seen[issue.ID] = struct{}{}
		if issue.Repository.ID != repo.ID {
			t.Fatalf("expected preloaded repository %d, got %d", repo.ID, issue.Repository.ID)
		}
		if issue.Author.ID != user.ID {
			t.Fatalf("expected preloaded author %d, got %d", user.ID, issue.Author.ID)
		}
		if issue.Milestone == nil || issue.Milestone.ID != milestone.ID {
			t.Fatalf("expected preloaded milestone %d, got %#v", milestone.ID, issue.Milestone)
		}
		if len(issue.Labels) != 1 || issue.Labels[0].ID != label.ID {
			t.Fatalf("expected preloaded label %d, got %#v", label.ID, issue.Labels)
		}
	}

	if counter.count > 15 {
		t.Fatalf("expected combined search to stay within 15 queries after single preload pass, got %d", counter.count)
	}
}
