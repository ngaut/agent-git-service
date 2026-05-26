package service_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func BenchmarkCompactWikiHistory_ManyRevisions(b *testing.B) {
	svc, cleanup := setupTestService(b)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "wiki-compact-bench-owner", Name: "wiki-compact-bench-owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		b.Fatalf("create owner: %v", err)
	}
	if err := svc.DB.Create(&db.User{
		Login: "wiki-bot",
		Name:  "Wiki Bot",
		Email: "gh-server@localhost",
		Type:  db.TypeUser,
	}).Error; err != nil {
		b.Fatalf("seed author user: %v", err)
	}
	full := owner.Login + "/wiki-compact-bench"
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-compact-bench",
		AutoInit:   true,
	}); err != nil {
		b.Fatalf("create repo: %v", err)
	}

	const pages = 12
	const revisionsPerPage = 8
	for p := 0; p < pages; p++ {
		slug := fmt.Sprintf("docs/page-%02d", p)
		var sha string
		for rev := 0; rev < revisionsPerPage; rev++ {
			page, err := svc.PutWikiPage(ctx, full, slug, fmt.Sprintf("# Page %02d\n\nRevision %02d\n", p, rev), fmt.Sprintf("rev %02d", rev), sha)
			if err != nil {
				b.Fatalf("PutWikiPage(%s, %d): %v", slug, rev, err)
			}
			sha = page.SHA
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.CompactWikiHistory(ctx, full); err != nil {
			b.Fatalf("CompactWikiHistory: %v", err)
		}
	}
}
