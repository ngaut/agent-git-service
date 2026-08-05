package service_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func BenchmarkPutWikiPages(b *testing.B) {
	for _, pageCount := range []int{100, 1000, 3000} {
		b.Run(fmt.Sprintf("create/%d", pageCount), func(b *testing.B) {
			benchmarkPutWikiPages(b, pageCount, false)
		})
		b.Run(fmt.Sprintf("recreate/%d", pageCount), func(b *testing.B) {
			benchmarkPutWikiPages(b, pageCount, true)
		})
	}
}

func benchmarkPutWikiPages(b *testing.B, pageCount int, recreate bool) {
	b.ReportAllocs()
	b.ReportMetric(float64(pageCount), "pages/op")

	for iteration := 0; iteration < b.N; iteration++ {
		b.StopTimer()
		svc, cleanup := setupTestService(b)
		ctx := context.Background()
		mode := "create"
		if recreate {
			mode = "recreate"
		}
		login := fmt.Sprintf("wiki-write-%s-%d-%d", mode, pageCount, iteration)
		owner := db.User{
			Login: login,
			Name:  login,
			Type:  db.TypeUser,
		}
		if err := svc.DB.Create(&owner).Error; err != nil {
			cleanup()
			b.Fatalf("create owner: %v", err)
		}
		full := login + "/pages"
		if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: login,
			Name:       "pages",
		}); err != nil {
			cleanup()
			b.Fatalf("create repo: %v", err)
		}
		ctx = service.ContextWithUser(ctx, owner)
		if recreate {
			seedWikiTombstones(b, svc, ctx, full, pageCount)
		}

		b.StartTimer()
		for page := 0; page < pageCount; page++ {
			slug := fmt.Sprintf("generated/page-%05d", page)
			body := fmt.Sprintf("# Page %05d\n\nGenerated body %d.\n", page, page)
			if _, err := svc.PutWikiPage(ctx, full, slug, body, "write "+slug, ""); err != nil {
				b.Fatalf("PutWikiPage(%s): %v", slug, err)
			}
		}
		b.StopTimer()

		svc.Wg.Wait()
		cleanup()
	}

	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*pageCount), "ns/page")
}

func seedWikiTombstones(b *testing.B, svc *service.Service, ctx context.Context, repoFullName string, pageCount int) {
	b.Helper()

	for page := 0; page < pageCount; page++ {
		slug := fmt.Sprintf("generated/page-%05d", page)
		if _, err := svc.PutWikiPage(ctx, repoFullName, slug, fmt.Sprintf("# Seed %05d\n", page), "seed "+slug, ""); err != nil {
			b.Fatalf("seed PutWikiPage(%s): %v", slug, err)
		}
	}
	for page := 0; page < pageCount; page++ {
		slug := fmt.Sprintf("generated/page-%05d", page)
		if err := svc.DeleteWikiPage(ctx, repoFullName, slug, "delete "+slug); err != nil {
			b.Fatalf("seed DeleteWikiPage(%s): %v", slug, err)
		}
	}
	commits, err := svc.Git.CountCommits(ctx, repoFullName+".wiki", nil)
	if err != nil {
		b.Fatalf("count seeded wiki commits: %v", err)
	}
	if commits != pageCount*2 {
		b.Fatalf("seeded wiki commits = %d, want %d", commits, pageCount*2)
	}
}
