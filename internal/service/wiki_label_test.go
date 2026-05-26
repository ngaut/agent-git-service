package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func TestWikiPageLabelsLifecycleAndRecall(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "wiki-label-owner", Name: "wiki-label-owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-labels",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	full := owner.Login + "/wiki-labels"

	if _, err := svc.CreateLabel(ctx, full, "auth", "d73a4a", "Authentication, OAuth, sessions, token lifecycle, permission checks"); err != nil {
		t.Fatalf("create auth label: %v", err)
	}
	if _, err := svc.CreateLabel(ctx, full, "runbook", "0e8a16", "Operational procedures and recovery steps"); err != nil {
		t.Fatalf("create runbook label: %v", err)
	}
	if _, err := svc.CreateLabel(ctx, full, "deprecated", "cccccc", "Outdated wiki material"); err != nil {
		t.Fatalf("create deprecated label: %v", err)
	}

	page, err := svc.PutWikiPage(ctx, full, "guides/rotation", "# Rotation\n\nUse the admin console to rotate credentials.", "create rotation", "")
	if err != nil {
		t.Fatalf("put rotation page: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "guides/legacy", "# Legacy\n\nOld credential process.", "create legacy", ""); err != nil {
		t.Fatalf("put legacy page: %v", err)
	}
	svc.Wg.Wait()

	labels, err := svc.SetWikiPageLabels(ctx, full, "guides/rotation", []string{"auth", "runbook"})
	if err != nil {
		t.Fatalf("SetWikiPageLabels(rotation): %v", err)
	}
	if got := labelNames(labels); len(got) != 2 || got[0] != "auth" || got[1] != "runbook" {
		t.Fatalf("rotation labels = %v, want [auth runbook]", got)
	}
	if _, err := svc.SetWikiPageLabels(ctx, full, "guides/legacy", []string{"deprecated"}); err != nil {
		t.Fatalf("SetWikiPageLabels(legacy): %v", err)
	}
	svc.Wg.Wait()

	gotPage, err := svc.GetWikiPage(ctx, full, "guides/rotation")
	if err != nil {
		t.Fatalf("GetWikiPage: %v", err)
	}
	if got := labelNames(gotPage.Labels); len(got) != 2 || got[0] != "auth" || got[1] != "runbook" {
		t.Fatalf("page labels = %v, want [auth runbook]", got)
	}

	pages, err := svc.ListWikiPages(ctx, full, service.ListWikiPagesOptions{
		Recursive: true,
		Labels:    []string{"auth"},
	})
	if err != nil {
		t.Fatalf("ListWikiPages(label): %v", err)
	}
	if len(pages) != 1 || pages[0].Slug != "guides/rotation" {
		t.Fatalf("label-filtered pages = %#v, want guides/rotation", pages)
	}

	resp, err := svc.SearchWikiPagesWithOptions(ctx, full, "token lifecycle", service.WikiSearchOptions{
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("SearchWikiPagesWithOptions(label recall): %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Slug != "guides/rotation" {
		t.Fatalf("label recall results = %#v, want guides/rotation", resp.Results)
	}

	resp, err = svc.SearchWikiPagesWithOptions(ctx, full, "credential", service.WikiSearchOptions{
		Limit:         20,
		ExcludeLabels: []string{"deprecated"},
	})
	if err != nil {
		t.Fatalf("SearchWikiPagesWithOptions(exclude): %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Slug != "guides/rotation" {
		t.Fatalf("exclude label results = %#v, want only guides/rotation", resp.Results)
	}

	moved, err := svc.MoveWikiPage(ctx, full, "guides/rotation", "runbooks/rotation", page.SHA, "move rotation")
	if err != nil {
		t.Fatalf("MoveWikiPage: %v", err)
	}
	svc.Wg.Wait()
	if got := labelNames(moved.Moved.Labels); len(got) != 2 || got[0] != "auth" || got[1] != "runbook" {
		t.Fatalf("moved labels = %v, want [auth runbook]", got)
	}
	if oldLabels, err := svc.ListWikiPageLabels(ctx, full, "guides/rotation"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("old page labels = %#v, err=%v, want ErrNotFound", oldLabels, err)
	}

	if err := svc.DeleteWikiPage(ctx, full, "runbooks/rotation", "delete rotation"); err != nil {
		t.Fatalf("DeleteWikiPage: %v", err)
	}
	svc.Wg.Wait()
	var count int64
	if err := svc.DB.Table("wiki_page_labels").Where("slug = ?", "runbooks/rotation").Count(&count).Error; err != nil {
		t.Fatalf("count wiki labels: %v", err)
	}
	if count != 0 {
		t.Fatalf("wiki_page_labels rows after delete = %d, want 0", count)
	}
}

func TestMoveWikiPagePrefix_PreservesLabelsOnMovedPages(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "wiki-bulk-label-owner", Name: "wiki-bulk-label-owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-bulk-labels",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	full := owner.Login + "/wiki-bulk-labels"

	if _, err := svc.CreateLabel(ctx, full, "auth", "d73a4a", "Authentication docs"); err != nil {
		t.Fatalf("create auth label: %v", err)
	}
	if _, err := svc.CreateLabel(ctx, full, "runbook", "0e8a16", "Operational docs"); err != nil {
		t.Fatalf("create runbook label: %v", err)
	}

	intro, err := svc.PutWikiPage(ctx, full, "tutorial/intro", "# Intro\n", "create intro", "")
	if err != nil {
		t.Fatalf("PutWikiPage(intro): %v", err)
	}
	deep, err := svc.PutWikiPage(ctx, full, "tutorial/deep/link", "# Deep\n", "create deep", "")
	if err != nil {
		t.Fatalf("PutWikiPage(deep): %v", err)
	}
	if _, err := svc.SetWikiPageLabels(ctx, full, "tutorial/intro", []string{"auth"}); err != nil {
		t.Fatalf("SetWikiPageLabels(intro): %v", err)
	}
	if _, err := svc.SetWikiPageLabels(ctx, full, "tutorial/deep/link", []string{"runbook"}); err != nil {
		t.Fatalf("SetWikiPageLabels(deep): %v", err)
	}
	svc.Wg.Wait()

	result, err := svc.MoveWikiPagePrefix(ctx, full, "tutorial", "guides", map[string]string{
		"tutorial/intro":     intro.SHA,
		"tutorial/deep/link": deep.SHA,
	}, "move tutorial")
	if err != nil {
		t.Fatalf("MoveWikiPagePrefix: %v", err)
	}
	svc.Wg.Wait()
	if len(result.Moved) != 2 {
		t.Fatalf("moved = %+v, want 2 rows", result.Moved)
	}

	introLabels, err := svc.ListWikiPageLabels(ctx, full, "guides/intro")
	if err != nil {
		t.Fatalf("ListWikiPageLabels(guides/intro): %v", err)
	}
	if got := labelNames(introLabels); len(got) != 1 || got[0] != "auth" {
		t.Fatalf("guides/intro labels = %v, want [auth]", got)
	}
	deepLabels, err := svc.ListWikiPageLabels(ctx, full, "guides/deep/link")
	if err != nil {
		t.Fatalf("ListWikiPageLabels(guides/deep/link): %v", err)
	}
	if got := labelNames(deepLabels); len(got) != 1 || got[0] != "runbook" {
		t.Fatalf("guides/deep/link labels = %v, want [runbook]", got)
	}

	if _, err := svc.ListWikiPageLabels(ctx, full, "tutorial/intro"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("old intro labels err = %v, want ErrNotFound", err)
	}
	if _, err := svc.ListWikiPageLabels(ctx, full, "tutorial/deep/link"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("old deep labels err = %v, want ErrNotFound", err)
	}
}

func TestWikiWrites_RecreateMissingGitProjection_Issue1446(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "wiki-projection-owner", Name: "wiki-projection-owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-projection-rebuild",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	full := owner.Login + "/wiki-projection-rebuild"

	intro, err := svc.PutWikiPage(ctx, full, "tutorial/intro", "# Intro\n", "create intro", "")
	if err != nil {
		t.Fatalf("PutWikiPage(intro): %v", err)
	}
	deep, err := svc.PutWikiPage(ctx, full, "tutorial/deep/link", "# Deep\n", "create deep", "")
	if err != nil {
		t.Fatalf("PutWikiPage(deep): %v", err)
	}

	if err := svc.Git.Delete(ctx, full+".wiki"); err != nil {
		t.Fatalf("delete wiki projection before move: %v", err)
	}
	if _, err := svc.MoveWikiPage(ctx, full, "tutorial/intro", "guides/intro", intro.SHA, "move intro"); err != nil {
		t.Fatalf("MoveWikiPage after projection loss: %v", err)
	}
	if !svc.Git.Exists(ctx, full+".wiki") {
		t.Fatalf("wiki projection was not recreated after MoveWikiPage")
	}

	if err := svc.Git.Delete(ctx, full+".wiki"); err != nil {
		t.Fatalf("delete wiki projection before bulk move: %v", err)
	}
	if _, err := svc.MoveWikiPagePrefix(ctx, full, "tutorial", "guides", map[string]string{
		"tutorial/deep/link": deep.SHA,
	}, "bulk move tutorial"); err != nil {
		t.Fatalf("MoveWikiPagePrefix after projection loss: %v", err)
	}
	if !svc.Git.Exists(ctx, full+".wiki") {
		t.Fatalf("wiki projection was not recreated after MoveWikiPagePrefix")
	}

	if err := svc.Git.Delete(ctx, full+".wiki"); err != nil {
		t.Fatalf("delete wiki projection before delete: %v", err)
	}
	if err := svc.DeleteWikiPage(ctx, full, "guides/deep/link", "delete deep"); err != nil {
		t.Fatalf("DeleteWikiPage after projection loss: %v", err)
	}
	if !svc.Git.Exists(ctx, full+".wiki") {
		t.Fatalf("wiki projection was not recreated after DeleteWikiPage")
	}
}

func labelNames(labels []db.Label) []string {
	names := make([]string, 0, len(labels))
	for _, label := range labels {
		names = append(names, label.Name)
	}
	return names
}
