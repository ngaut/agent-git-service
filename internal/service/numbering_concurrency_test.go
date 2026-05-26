package service_test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func TestConcurrentIssueAndPRCreationUsesSharedUniqueNumbers(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "concurrency-user", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "concurrency-user",
		Name:          "concurrency-repo",
		DefaultBranch: "main",
		AddReadme:     true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	const issueWorkers = 12
	const prWorkers = 12
	const total = issueWorkers + prWorkers

	start := make(chan struct{})
	var wg sync.WaitGroup
	numbers := make([]int, total)
	errs := make([]error, total)

	for i := 0; i < issueWorkers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
				RepoFullName: "concurrency-user/concurrency-repo",
				Title:        fmt.Sprintf("issue-%d", idx),
				Body:         "body",
				AuthorLogin:  "concurrency-user",
			})
			if err != nil {
				errs[idx] = fmt.Errorf("create issue %d: %w", idx, err)
				return
			}
			numbers[idx] = issue.Number
		}(i)
	}

	for i := 0; i < prWorkers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			slot := issueWorkers + i
			pr, err := svc.CreatePR(ctx, service.CreatePRInput{
				RepoFullName: "concurrency-user/concurrency-repo",
				Title:        fmt.Sprintf("pr-%d", i),
				Body:         "body",
				HeadRef:      fmt.Sprintf("feature-%d", i),
				BaseRef:      "main",
				AuthorLogin:  "concurrency-user",
			})
			if err != nil {
				errs[slot] = fmt.Errorf("create pr %d: %w", i, err)
				return
			}
			numbers[slot] = pr.Number
		}(i)
	}

	close(start)
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent create failed: %v", err)
		}
	}

	sort.Ints(numbers)
	for i, num := range numbers {
		want := i + 1
		if num != want {
			t.Fatalf("expected global issue/pr number %d, got %d (all numbers=%v)", want, num, numbers)
		}
	}
}
