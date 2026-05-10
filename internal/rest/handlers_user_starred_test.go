package rest_test

import (
	"context"
	"testing"

	"gh-server/internal/service"
	"gh-server/internal/testharness"
)

func TestListUserStarredRepos(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	owner, _ := seedHarnessUser(t, h, "stars-owner", false)
	viewer, viewerToken := seedHarnessUser(t, h, "stars-viewer", false)
	target, _ := seedHarnessUser(t, h, "stars-target", false)

	targetRepo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "target-starred",
		Private:    false,
	})
	if err != nil {
		t.Fatalf("CreateRepo targetRepo: %v", err)
	}
	viewerRepo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "viewer-starred",
		Private:    false,
	})
	if err != nil {
		t.Fatalf("CreateRepo viewerRepo: %v", err)
	}

	if err := h.Svc.StarRepo(ctx, targetRepo.FullName, target.Login); err != nil {
		t.Fatalf("StarRepo target: %v", err)
	}
	if err := h.Svc.StarRepo(ctx, viewerRepo.FullName, viewer.Login); err != nil {
		t.Fatalf("StarRepo viewer: %v", err)
	}

	w := h.DoRESTWithToken(t, "GET", "/api/v3/users/"+target.Login+"/starred", viewerToken)
	if w.Code != 200 {
		t.Fatalf("GET /api/v3/users/%s/starred = %d: %s", target.Login, w.Code, w.Body.String())
	}

	repos := testharness.DecodeJSONArray(t, w)
	if len(repos) != 1 {
		t.Fatalf("expected 1 starred repo, got %d", len(repos))
	}
	if repos[0]["full_name"] != targetRepo.FullName {
		t.Fatalf("full_name = %v, want %s", repos[0]["full_name"], targetRepo.FullName)
	}
	if repos[0]["full_name"] == viewerRepo.FullName {
		t.Fatalf("response should list the target user's stars, not the viewer's")
	}
}

func TestListUserStarredRepos_AllowsAnonymousViewOfPublicStars(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	owner, _ := seedHarnessUser(t, h, "anon-stars-owner", false)
	target, _ := seedHarnessUser(t, h, "anon-stars-target", false)

	publicRepo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "anonymous-visible-starred",
		Private:    false,
	})
	if err != nil {
		t.Fatalf("CreateRepo publicRepo: %v", err)
	}
	privateRepo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "anonymous-hidden-starred",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("CreateRepo privateRepo: %v", err)
	}

	if err := h.Svc.StarRepo(ctx, publicRepo.FullName, target.Login); err != nil {
		t.Fatalf("StarRepo publicRepo: %v", err)
	}
	if err := h.Svc.StarRepo(ctx, privateRepo.FullName, target.Login); err != nil {
		t.Fatalf("StarRepo privateRepo: %v", err)
	}

	w := h.DoRESTNoAuth(t, "GET", "/api/v3/users/"+target.Login+"/starred")
	if w.Code != 200 {
		t.Fatalf("GET /api/v3/users/%s/starred anonymously = %d: %s", target.Login, w.Code, w.Body.String())
	}

	repos := testharness.DecodeJSONArray(t, w)
	if len(repos) != 1 {
		t.Fatalf("expected only public starred repo for anonymous viewer, got %d", len(repos))
	}
	if repos[0]["full_name"] != publicRepo.FullName {
		t.Fatalf("full_name = %v, want %s", repos[0]["full_name"], publicRepo.FullName)
	}
}

func TestListUserStarredRepos_FiltersPrivateReposWithoutAccess(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	owner, _ := seedHarnessUser(t, h, "private-stars-owner", false)
	viewer, viewerToken := seedHarnessUser(t, h, "private-stars-viewer", false)
	target, targetToken := seedHarnessUser(t, h, "private-stars-target", false)

	publicRepo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "public-starred",
		Private:    false,
	})
	if err != nil {
		t.Fatalf("CreateRepo publicRepo: %v", err)
	}
	privateRepo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "private-starred",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("CreateRepo privateRepo: %v", err)
	}

	if err := h.Svc.StarRepo(ctx, publicRepo.FullName, target.Login); err != nil {
		t.Fatalf("StarRepo public: %v", err)
	}
	if err := h.Svc.StarRepo(ctx, privateRepo.FullName, target.Login); err != nil {
		t.Fatalf("StarRepo private: %v", err)
	}

	w := h.DoRESTWithToken(t, "GET", "/api/v3/users/"+target.Login+"/starred", viewerToken)
	if w.Code != 200 {
		t.Fatalf("GET /api/v3/users/%s/starred = %d: %s", target.Login, w.Code, w.Body.String())
	}
	repos := testharness.DecodeJSONArray(t, w)
	if len(repos) != 1 {
		t.Fatalf("expected only public starred repo for unrelated viewer, got %d", len(repos))
	}
	if repos[0]["full_name"] != publicRepo.FullName {
		t.Fatalf("full_name = %v, want %s", repos[0]["full_name"], publicRepo.FullName)
	}

	w = h.DoRESTWithToken(t, "GET", "/api/v3/users/"+target.Login+"/starred", targetToken)
	if w.Code != 200 {
		t.Fatalf("GET /api/v3/users/%s/starred as target = %d: %s", target.Login, w.Code, w.Body.String())
	}
	repos = testharness.DecodeJSONArray(t, w)
	if len(repos) != 2 {
		t.Fatalf("expected target to see both starred repos, got %d", len(repos))
	}

	viewerRepo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "viewer-private-access",
		Private:    true,
	})
	if err != nil {
		t.Fatalf("CreateRepo viewerRepo: %v", err)
	}
	if err := h.Svc.AddCollaborator(ctx, viewerRepo.ID, viewer.ID, "read"); err != nil {
		t.Fatalf("AddCollaborator viewerRepo: %v", err)
	}
	if err := h.Svc.StarRepo(ctx, viewerRepo.FullName, target.Login); err != nil {
		t.Fatalf("StarRepo viewerRepo: %v", err)
	}

	w = h.DoRESTWithToken(t, "GET", "/api/v3/users/"+target.Login+"/starred", viewerToken)
	if w.Code != 200 {
		t.Fatalf("GET /api/v3/users/%s/starred after collaborator grant = %d: %s", target.Login, w.Code, w.Body.String())
	}
	repos = testharness.DecodeJSONArray(t, w)
	if len(repos) != 2 {
		t.Fatalf("expected viewer to see public repo plus accessible private repo, got %d", len(repos))
	}
}
