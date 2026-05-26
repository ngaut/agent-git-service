package rest_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

func TestPRHandlers_GetPRDiff(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	repo := "pr-diff"
	compatSeedRepo(t, h, repo)

	full := "testuser/" + repo
	if err := h.Svc.Git.CreateBranch(ctx, full, "feature", "main"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	if _, err := h.Svc.Git.WriteFile(ctx, full, "feature", "feature.txt", "add feature", []byte("hello")); err != nil {
		t.Fatalf("write file: %v", err)
	}

	w := h.DoRESTJSON(t, "POST", fmt.Sprintf("/api/v3/repos/%s/pulls", full), map[string]any{
		"title": "diff PR",
		"head":  "feature",
		"base":  "main",
	})
	assertStatusCode(t, w, 201)

	req := httptest.NewRequest("GET", fmt.Sprintf("/api/v3/repos/%s/pulls/1", full), nil)
	req.Header.Set("Authorization", "token "+h.Token)
	req.Header.Set("Accept", "application/vnd.github.v3.diff")
	w = httptest.NewRecorder()
	h.Mux.ServeHTTP(w, req)
	assertStatusCode(t, w, 200)

	if ct := w.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("expected Content-Type text/plain; charset=utf-8, got %q", ct)
	}
	if body := w.Body.String(); !strings.Contains(body, "diff --git") {
		t.Fatalf("expected diff output, got %q", body)
	}
	if body := w.Body.String(); !strings.Contains(body, "feature.txt") {
		t.Fatalf("expected diff to include feature.txt, got %q", body)
	}
}

func TestPRHandlers_CreatePR_CrossRepoHeadParsingError(t *testing.T) {
	h := testharness.New(t)
	repo := "cross-head"
	compatSeedRepo(t, h, repo)

	w := h.DoRESTJSON(t, "POST", fmt.Sprintf("/api/v3/repos/testuser/%s/pulls", repo), map[string]any{
		"title": "bad head",
		"head":  "ghost:main",
		"base":  "main",
	})
	assertStatusCode(t, w, 422)
	body := testharness.DecodeJSON(t, w)
	msg, _ := body["message"].(string)
	if !strings.Contains(msg, "head and base must be different") {
		t.Fatalf("expected cross-repo head parsing error, got %q", msg)
	}
}

func TestPRHandlers_UpdateBranch(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	repo := "update-branch-rest"
	compatSeedRepo(t, h, repo)
	full := "testuser/" + repo

	if err := h.Svc.Git.CreateBranch(ctx, full, "feature", "main"); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	if _, err := h.Svc.Git.WriteFile(ctx, full, "feature", "feature.txt", "feature work", []byte("feature\n")); err != nil {
		t.Fatalf("write feature: %v", err)
	}
	pr, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: full,
		Title:        "update branch",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create pr: %v", err)
	}

	var repoModel db.Repository
	if err := h.Svc.DB.Preload("Owner").First(&repoModel, "full_name = ?", full).Error; err != nil {
		t.Fatalf("find repo: %v", err)
	}
	var (
		mu   sync.Mutex
		hits []map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("unmarshal webhook payload: %v", err)
		}
		mu.Lock()
		hits = append(hits, payload)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	hook := &db.Webhook{
		RepositoryID: repoModel.ID,
		Name:         "web",
		Active:       true,
		EventsJSON:   `["pull_request"]`,
		ConfigJSON:   `{"url":"` + server.URL + `","content_type":"json"}`,
	}
	if err := h.Svc.CreateWebhook(ctx, hook); err != nil {
		t.Fatalf("create webhook: %v", err)
	}

	if _, err := h.Svc.Git.WriteFile(ctx, full, "main", "base.txt", "base advanced", []byte("base\n")); err != nil {
		t.Fatalf("advance base: %v", err)
	}

	w := h.DoRESTJSON(t, "PUT", fmt.Sprintf("/api/v3/repos/%s/pulls/%d/update-branch", full, pr.Number), map[string]any{
		"expected_head_sha": "0000000000000000000000000000000000000000",
	})
	assertStatusCode(t, w, 422)

	w = h.DoRESTJSON(t, "PUT", fmt.Sprintf("/api/v3/repos/%s/pulls/%d/update-branch", full, pr.Number), map[string]any{
		"expected_head_sha": pr.HeadSHA,
	})
	assertStatusCode(t, w, 202)
	body := testharness.DecodeJSON(t, w)
	if body["message"] != "Updating pull request branch." {
		t.Fatalf("update branch message: got %v", body["message"])
	}

	updated, err := h.Svc.GetPR(ctx, full, pr.Number)
	if err != nil {
		t.Fatalf("get updated pr: %v", err)
	}
	if updated.HeadSHA == "" || updated.HeadSHA == pr.HeadSHA {
		t.Fatalf("expected PR head SHA to change, before %q after %q", pr.HeadSHA, updated.HeadSHA)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 1 {
		t.Fatalf("expected 1 webhook hit, got %d", len(hits))
	}
	if hits[0]["action"] != "synchronize" {
		t.Fatalf("expected synchronize action, got %v", hits[0]["action"])
	}
	prPayload, ok := hits[0]["pull_request"].(map[string]any)
	if !ok {
		t.Fatalf("expected pull_request payload map, got %T", hits[0]["pull_request"])
	}
	if prPayload["head"] == nil {
		t.Fatal("expected pull_request payload to include head")
	}
}

func TestPRHandlers_ListPRs_BaseHeadFilters(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	repo := "list-pr-filters"
	compatSeedRepo(t, h, repo)
	full := "testuser/" + repo

	for _, branch := range []string{"feature-one", "feature-two"} {
		if err := h.Svc.Git.CreateBranch(ctx, full, branch, "main"); err != nil {
			t.Fatalf("create %s: %v", branch, err)
		}
		if _, err := h.Svc.Git.WriteFile(ctx, full, branch, branch+".txt", "add "+branch, []byte(branch)); err != nil {
			t.Fatalf("write %s: %v", branch, err)
		}
		if _, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
			RepoFullName: full,
			Title:        branch,
			HeadRef:      branch,
			BaseRef:      "main",
			AuthorLogin:  h.User.Login,
		}); err != nil {
			t.Fatalf("create PR for %s: %v", branch, err)
		}
	}

	w := h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/%s/pulls?state=open&base=main&head=testuser:feature-one", full), nil)
	assertStatusCode(t, w, 200)
	items := testharness.DecodeJSONArray(t, w)
	if len(items) != 1 {
		t.Fatalf("expected one PR filtered by head/base, got %d", len(items))
	}
	head, _ := items[0]["head"].(map[string]any)
	if head["ref"] != "feature-one" {
		t.Fatalf("filtered PR head ref: got %v", head["ref"])
	}
}

func TestPRHandlers_RequestedReviewers(t *testing.T) {
	h := testharness.New(t)
	repo := "review-requests"
	compatSeedPR(t, h, repo, "feature")

	path := fmt.Sprintf("/api/v3/repos/testuser/%s/pulls/1/requested_reviewers", repo)

	t.Run("AddRemove", func(t *testing.T) {
		w := h.DoRESTJSON(t, "POST", path, map[string]any{
			"reviewers":      []string{"reviewer1", "reviewer2"},
			"team_reviewers": []string{"core"},
		})
		assertStatusCode(t, w, 200)
		body := testharness.DecodeJSON(t, w)
		assertStringSet(t, collectRequestedReviewers(t, body), []string{"reviewer1", "reviewer2"}, "requested_reviewers")
		assertStringSet(t, collectRequestedTeams(t, body), []string{"core"}, "requested_teams")

		w = h.DoRESTJSON(t, "DELETE", path, map[string]any{
			"reviewers":      []string{"reviewer1", "reviewer2"},
			"team_reviewers": []string{"core"},
		})
		assertStatusCode(t, w, 200)
		body = testharness.DecodeJSON(t, w)
		if got := collectRequestedReviewers(t, body); len(got) != 0 {
			t.Fatalf("expected requested_reviewers to be empty after removal, got %v", got)
		}
		if got := collectRequestedTeams(t, body); len(got) != 0 {
			t.Fatalf("expected requested_teams to be empty after removal, got %v", got)
		}
	})

	t.Run("AuthRequired", func(t *testing.T) {
		w := h.DoRESTNoAuth(t, "POST", path)
		assertStatusCode(t, w, 401)

		w = h.DoRESTNoAuth(t, "DELETE", path)
		assertStatusCode(t, w, 401)
	})

	t.Run("ListEndpointUsesFullUserShape", func(t *testing.T) {
		reviewer := db.User{
			Login: "reviewer-shaped",
			Name:  "Reviewer Shaped",
			Email: "reviewer-shaped@example.com",
			Type:  db.TypeUser,
		}
		if err := h.Svc.DB.Create(&reviewer).Error; err != nil {
			t.Fatalf("create reviewer: %v", err)
		}
		if err := h.Svc.RequestReview(context.Background(), 1, "reviewer-shaped"); err != nil {
			t.Fatalf("RequestReview: %v", err)
		}

		w := h.DoREST(t, "GET", path, nil)
		assertStatusCode(t, w, 200)
		body := testharness.DecodeJSON(t, w)
		users, ok := body["users"].([]any)
		if !ok || len(users) != 1 {
			t.Fatalf("expected one user reviewer, got %v", body["users"])
		}
		user, ok := users[0].(map[string]any)
		if !ok {
			t.Fatalf("expected reviewer object, got %T", users[0])
		}
		if user["login"] != "reviewer-shaped" {
			t.Fatalf("expected reviewer login reviewer-shaped, got %v", user["login"])
		}
		if user["url"] == nil || user["avatar_url"] == nil {
			t.Fatalf("expected full user shape, got %v", user)
		}
	})
}

func TestPRHandlers_GetPR_RequestedReviewersUseFullUserShape(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	repo := "pr-full-reviewers"
	compatSeedPR(t, h, repo, "feature")

	reviewer := db.User{
		Login: "reviewer1",
		Name:  "Reviewer One",
		Email: "reviewer1@example.com",
		Type:  db.TypeUser,
	}
	if err := h.Svc.DB.Create(&reviewer).Error; err != nil {
		t.Fatalf("create reviewer: %v", err)
	}
	if err := h.Svc.RequestReview(ctx, 1, "reviewer1"); err != nil {
		t.Fatalf("RequestReview: %v", err)
	}

	w := h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/testuser/%s/pulls/1", repo), nil)
	assertStatusCode(t, w, 200)
	body := testharness.DecodeJSON(t, w)

	if _, ok := body["reviewers"]; ok {
		t.Fatalf("expected reviewers field to be absent")
	}
	reviewers, ok := body["requested_reviewers"].([]any)
	if !ok || len(reviewers) != 1 {
		t.Fatalf("expected one requested reviewer, got %v", body["requested_reviewers"])
	}
	reviewerBody, ok := reviewers[0].(map[string]any)
	if !ok {
		t.Fatalf("expected reviewer object, got %T", reviewers[0])
	}
	if reviewerBody["login"] != "reviewer1" {
		t.Fatalf("expected reviewer login reviewer1, got %v", reviewerBody["login"])
	}
	if reviewerBody["url"] == nil || reviewerBody["avatar_url"] == nil {
		t.Fatalf("expected full user shape, got %v", reviewerBody)
	}
}

func TestPRHandlers_ListPRs_RequestedReviewersAndMergeableState(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	repo := "pr-list-reviewers-mergeable"
	compatSeedRepo(t, h, repo)
	full := "testuser/" + repo

	reviewer := db.User{
		Login: "reviewer-list",
		Name:  "Reviewer List",
		Email: "reviewer-list@example.com",
		Type:  db.TypeUser,
	}
	if err := h.Svc.DB.Create(&reviewer).Error; err != nil {
		t.Fatalf("create reviewer: %v", err)
	}

	if err := h.Svc.Git.CreateBranch(ctx, full, "clean-feature", "main"); err != nil {
		t.Fatalf("create clean branch: %v", err)
	}
	if _, err := h.Svc.Git.WriteFile(ctx, full, "clean-feature", "clean.txt", "add clean", []byte("clean\n")); err != nil {
		t.Fatalf("write clean branch: %v", err)
	}
	cleanPR, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: full,
		Title:        "clean",
		HeadRef:      "clean-feature",
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("create clean pr: %v", err)
	}
	if err := h.Svc.RequestReview(ctx, cleanPR.ID, "reviewer-list"); err != nil {
		t.Fatalf("RequestReview clean: %v", err)
	}
	if _, err := h.Svc.SetPRAssignees(ctx, full, cleanPR.Number, []string{h.User.Login}); err != nil {
		t.Fatalf("SetPRAssignees clean: %v", err)
	}

	if err := h.Svc.Git.CreateBranch(ctx, full, "conflict-feature", "main"); err != nil {
		t.Fatalf("create conflict branch: %v", err)
	}
	if _, err := h.Svc.Git.WriteFile(ctx, full, "conflict-feature", "conflict.txt", "feature conflict", []byte("feature\n")); err != nil {
		t.Fatalf("write conflict branch: %v", err)
	}
	if _, err := h.Svc.Git.WriteFile(ctx, full, "main", "conflict.txt", "main conflict", []byte("main\n")); err != nil {
		t.Fatalf("write main conflict: %v", err)
	}
	if _, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: full,
		Title:        "conflict",
		HeadRef:      "conflict-feature",
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	}); err != nil {
		t.Fatalf("create conflict pr: %v", err)
	}

	w := h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/%s/pulls?state=open&per_page=10", full), nil)
	assertStatusCode(t, w, 200)
	items := testharness.DecodeJSONArray(t, w)
	if len(items) != 2 {
		t.Fatalf("expected 2 PRs, got %d", len(items))
	}

	states := map[string]map[string]any{}
	for _, item := range items {
		title, _ := item["title"].(string)
		states[title] = item
	}

	cleanBody := states["clean"]
	if cleanBody == nil {
		t.Fatalf("missing clean PR in list response")
	}
	if cleanBody["mergeable"] != true || cleanBody["rebaseable"] != true || cleanBody["mergeable_state"] != "clean" {
		t.Fatalf("unexpected clean mergeability: %v / %v / %v", cleanBody["mergeable"], cleanBody["rebaseable"], cleanBody["mergeable_state"])
	}
	cleanReviewers := collectRequestedReviewers(t, cleanBody)
	assertStringSet(t, cleanReviewers, []string{"reviewer-list"}, "requested_reviewers")
	assignees, ok := cleanBody["assignees"].([]any)
	if !ok || len(assignees) != 1 {
		t.Fatalf("expected one assignee, got %v", cleanBody["assignees"])
	}
	assignee, ok := assignees[0].(map[string]any)
	if !ok {
		t.Fatalf("expected assignee object, got %T", assignees[0])
	}
	if assignee["login"] != h.User.Login || assignee["url"] == nil {
		t.Fatalf("expected full assignee shape, got %v", assignee)
	}

	conflictBody := states["conflict"]
	if conflictBody == nil {
		t.Fatalf("missing conflict PR in list response")
	}
	if conflictBody["mergeable"] != false || conflictBody["rebaseable"] != false || conflictBody["mergeable_state"] != "dirty" {
		t.Fatalf("unexpected conflict mergeability: %v / %v / %v", conflictBody["mergeable"], conflictBody["rebaseable"], conflictBody["mergeable_state"])
	}
}

func TestPRHandlers_GetPR_CrossRepoMergeabilityFallsBackToUnknown(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	compatSeedRepo(t, h, "cross-repo-mergeability")
	forkOwner, _ := seedHarnessUser(t, h, "fork-reviewer", false)

	upstreamFull := "testuser/cross-repo-mergeability"
	upstreamRepo, err := h.Svc.GetRepo(ctx, upstreamFull)
	if err != nil {
		t.Fatalf("get upstream repo: %v", err)
	}
	if err := h.Svc.AddCollaborator(ctx, upstreamRepo.ID, forkOwner.ID, "write"); err != nil {
		t.Fatalf("add collaborator: %v", err)
	}

	forkRepo, err := h.Svc.ForkRepo(ctx, upstreamFull, forkOwner.Login, "")
	if err != nil {
		t.Fatalf("fork repo: %v", err)
	}
	if _, err := h.Svc.Git.WriteFile(ctx, forkRepo.FullName, "main", "fork-only.txt", "fork change", []byte("fork\n")); err != nil {
		t.Fatalf("write fork branch: %v", err)
	}

	if _, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName:     upstreamFull,
		HeadRepoFullName: forkRepo.FullName,
		Title:            "cross repo mergeability",
		HeadRef:          "main",
		BaseRef:          "main",
		AuthorLogin:      forkOwner.Login,
	}); err != nil {
		t.Fatalf("create cross-repo pr: %v", err)
	}

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/cross-repo-mergeability/pulls/1", nil)
	assertStatusCode(t, w, 200)
	body := testharness.DecodeJSON(t, w)
	if body["mergeable"] != nil || body["rebaseable"] != nil || body["mergeable_state"] != "unknown" {
		t.Fatalf("expected cross-repo mergeability fallback to unknown, got %v / %v / %v", body["mergeable"], body["rebaseable"], body["mergeable_state"])
	}
}

func TestPRHandlers_PRReviewLifecycle(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	repo := "review-lifecycle"
	compatSeedPR(t, h, repo, "feature")

	pr, err := h.Svc.GetPR(ctx, "testuser/"+repo, 1)
	if err != nil {
		t.Fatalf("get pr: %v", err)
	}

	t.Run("CreateDefaultsToCommented", func(t *testing.T) {
		w := h.DoRESTJSON(t, "POST", fmt.Sprintf("/api/v3/repos/testuser/%s/pulls/1/reviews", repo), map[string]any{
			"body":      "initial review",
			"commit_id": pr.HeadSHA,
		})
		assertStatusCode(t, w, 200)
		body := testharness.DecodeJSON(t, w)
		if body["state"] != "COMMENTED" {
			t.Fatalf("expected default state COMMENTED, got %v", body["state"])
		}
		if body["commit_id"] != pr.HeadSHA {
			t.Fatalf("expected commit_id %q, got %v", pr.HeadSHA, body["commit_id"])
		}
	})

	t.Run("SubmitTransitionsState", func(t *testing.T) {
		cases := []struct {
			event string
			want  string
		}{
			{event: "APPROVE", want: "APPROVED"},
			{event: "REQUEST_CHANGES", want: "CHANGES_REQUESTED"},
		}
		for _, tc := range cases {
			t.Run(tc.event, func(t *testing.T) {
				w := h.DoRESTJSON(t, "POST", fmt.Sprintf("/api/v3/repos/testuser/%s/pulls/1/reviews", repo), map[string]any{
					"event": "PENDING",
					"body":  "draft review",
				})
				assertStatusCode(t, w, 200)
				review := testharness.DecodeJSON(t, w)
				reviewID := requireID(t, review, "id")
				if review["state"] != "PENDING" {
					t.Fatalf("expected draft review state PENDING, got %v", review["state"])
				}

				finalBody := "final " + strings.ToLower(tc.event)
				w = h.DoRESTJSON(t, "POST", fmt.Sprintf("/api/v3/repos/testuser/%s/pulls/1/reviews/%d/events", repo, reviewID), map[string]any{
					"event": tc.event,
					"body":  finalBody,
				})
				assertStatusCode(t, w, 200)
				submitted := testharness.DecodeJSON(t, w)
				if submitted["state"] != tc.want {
					t.Fatalf("expected state %s, got %v", tc.want, submitted["state"])
				}
				if submitted["body"] != finalBody {
					t.Fatalf("expected body %q, got %v", finalBody, submitted["body"])
				}
				if submittedAt, ok := submitted["submitted_at"].(string); !ok || submittedAt == "" {
					t.Fatalf("expected submitted_at to be set, got %v", submitted["submitted_at"])
				}
			})
		}
	})

	t.Run("SubmitRequiresEvent", func(t *testing.T) {
		w := h.DoRESTJSON(t, "POST", fmt.Sprintf("/api/v3/repos/testuser/%s/pulls/1/reviews", repo), map[string]any{
			"event": "PENDING",
		})
		assertStatusCode(t, w, 200)
		review := testharness.DecodeJSON(t, w)
		reviewID := requireID(t, review, "id")

		w = h.DoRESTJSON(t, "POST", fmt.Sprintf("/api/v3/repos/testuser/%s/pulls/1/reviews/%d/events", repo, reviewID), map[string]any{
			"body": "missing event",
		})
		assertStatusCode(t, w, 422)
		body := testharness.DecodeJSON(t, w)
		if body["message"] != "event is required" {
			t.Fatalf("expected validation message 'event is required', got %v", body["message"])
		}
	})
}

func TestPRHandlers_ListPRs_ExpandsAssigneesAcrossConcurrentItems(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	repo := "list-pr-assignees"
	compatSeedRepo(t, h, repo)

	full := "testuser/" + repo
	const prCount = 10
	for i := 1; i <= prCount; i++ {
		branch := fmt.Sprintf("feature-%d", i)
		if err := h.Svc.Git.CreateBranch(ctx, full, branch, "main"); err != nil {
			t.Fatalf("create branch %s: %v", branch, err)
		}
		if _, err := h.Svc.Git.WriteFile(ctx, full, branch, fmt.Sprintf("file-%d.txt", i), "add file", []byte(strings.Repeat("x", i))); err != nil {
			t.Fatalf("write branch file %s: %v", branch, err)
		}
		if _, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
			RepoFullName: full,
			Title:        fmt.Sprintf("PR %d", i),
			HeadRef:      branch,
			BaseRef:      "main",
			AuthorLogin:  h.User.Login,
		}); err != nil {
			t.Fatalf("create PR %d: %v", i, err)
		}
		if _, err := h.Svc.SetPRAssignees(ctx, full, i, []string{h.User.Login}); err != nil {
			t.Fatalf("set assignees on PR %d: %v", i, err)
		}
	}

	w := h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/%s/pulls?state=open&per_page=%d", full, prCount), nil)
	assertStatusCode(t, w, 200)
	items := testharness.DecodeJSONArray(t, w)
	if len(items) != prCount {
		t.Fatalf("expected %d PRs, got %d", prCount, len(items))
	}

	for _, item := range items {
		assignees, ok := item["assignees"].([]any)
		if !ok {
			t.Fatalf("expected assignees array, got %T", item["assignees"])
		}
		if len(assignees) != 1 {
			t.Fatalf("expected 1 assignee, got %d", len(assignees))
		}
		assignee, ok := assignees[0].(map[string]any)
		if !ok {
			t.Fatalf("expected assignee object, got %T", assignees[0])
		}
		if login, _ := assignee["login"].(string); login != h.User.Login {
			t.Fatalf("expected assignee %q, got %v", h.User.Login, assignee["login"])
		}
	}
}

func collectRequestedReviewers(t *testing.T, body map[string]any) []string {
	t.Helper()
	raw, ok := body["requested_reviewers"].([]any)
	if !ok {
		t.Fatalf("expected requested_reviewers to be array, got %T", body["requested_reviewers"])
	}
	logins := make([]string, 0, len(raw))
	for _, item := range raw {
		reviewer, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("expected reviewer object, got %T", item)
		}
		login, _ := reviewer["login"].(string)
		if login != "" {
			logins = append(logins, login)
		}
	}
	return logins
}

func collectRequestedTeams(t *testing.T, body map[string]any) []string {
	t.Helper()
	raw, ok := body["requested_teams"].([]any)
	if !ok {
		t.Fatalf("expected requested_teams to be array, got %T", body["requested_teams"])
	}
	slugs := make([]string, 0, len(raw))
	for _, item := range raw {
		team, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("expected team object, got %T", item)
		}
		slug, _ := team["slug"].(string)
		if slug != "" {
			slugs = append(slugs, slug)
		}
	}
	return slugs
}

func assertStringSet(t *testing.T, got []string, want []string, label string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: expected %d entries, got %d (%v)", label, len(want), len(got), got)
	}
	gotSet := make(map[string]bool, len(got))
	for _, g := range got {
		gotSet[g] = true
	}
	for _, w := range want {
		if !gotSet[w] {
			t.Fatalf("%s: expected %q in %v", label, w, got)
		}
	}
}
