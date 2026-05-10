package rest_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/service"
	"gh-server/internal/testharness"
)

func TestCreateMilestone_WithDueOn(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "milestone-create-dueon")

	const dueOn = "2026-12-31T23:59:59Z"

	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/milestone-create-dueon/milestones", map[string]any{
		"title":       "v1.0",
		"description": "release",
		"due_on":      dueOn,
	})
	assertStatusCode(t, w, 201)
	resp := testharness.DecodeJSON(t, w)
	if got := resp["due_on"]; got != dueOn {
		t.Fatalf("create milestone due_on: got %v, want %s", got, dueOn)
	}

	w = h.DoREST(t, "GET", "/api/v3/repos/testuser/milestone-create-dueon/milestones/1", nil)
	assertStatusCode(t, w, 200)
	resp = testharness.DecodeJSON(t, w)
	if got := resp["due_on"]; got != dueOn {
		t.Fatalf("get milestone due_on: got %v, want %s", got, dueOn)
	}
}

func TestMilestoneReadHandlers_NoAuthReturnNotFound(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	full := createPrivateMilestoneRepo(t, h, "milestone-authz-noauth")
	ms, err := h.Svc.CreateMilestone(ctx, full, "v1.0", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "list", method: "GET", path: "/api/v3/repos/testuser/milestone-authz-noauth/milestones"},
		{name: "get", method: "GET", path: "/api/v3/repos/testuser/milestone-authz-noauth/milestones/" + strconv.Itoa(ms.Number)},
		{name: "labels", method: "GET", path: "/api/v3/repos/testuser/milestone-authz-noauth/milestones/" + strconv.Itoa(ms.Number) + "/labels"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := h.DoRESTNoAuth(t, tc.method, tc.path)
			assertStatusCode(t, w, 404)
		})
	}
}

func TestMilestoneHandlers_ReadCollaboratorCanReadButNotMutate(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	full := createPrivateMilestoneRepo(t, h, "milestone-authz-read-collab")
	repo, err := h.Svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	ms, err := h.Svc.CreateMilestone(ctx, full, "v1.0", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}

	reader, readerToken := seedHarnessUser(t, h, "milestone-reader", false)
	if err := h.Svc.AddCollaborator(ctx, repo.ID, reader.ID, "read"); err != nil {
		t.Fatalf("AddCollaborator(read): %v", err)
	}

	readCases := []struct {
		name string
		path string
	}{
		{name: "list", path: "/api/v3/repos/testuser/milestone-authz-read-collab/milestones"},
		{name: "get", path: "/api/v3/repos/testuser/milestone-authz-read-collab/milestones/" + strconv.Itoa(ms.Number)},
		{name: "labels", path: "/api/v3/repos/testuser/milestone-authz-read-collab/milestones/" + strconv.Itoa(ms.Number) + "/labels"},
	}
	for _, tc := range readCases {
		t.Run("read_"+tc.name, func(t *testing.T) {
			w := h.DoRESTWithToken(t, "GET", tc.path, readerToken)
			assertStatusCode(t, w, 200)
		})
	}

	writeCases := []struct {
		name   string
		method string
		path   string
	}{
		{name: "create", method: "POST", path: "/api/v3/repos/testuser/milestone-authz-read-collab/milestones"},
		{name: "update", method: "PATCH", path: "/api/v3/repos/testuser/milestone-authz-read-collab/milestones/" + strconv.Itoa(ms.Number)},
		{name: "delete", method: "DELETE", path: "/api/v3/repos/testuser/milestone-authz-read-collab/milestones/" + strconv.Itoa(ms.Number)},
	}
	for _, tc := range writeCases {
		t.Run("write_"+tc.name, func(t *testing.T) {
			w := h.DoRESTWithToken(t, tc.method, tc.path, readerToken)
			assertStatusCode(t, w, 404)
		})
	}
}

func TestCreateIssue_MilestoneUsesNumberNotID(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	compatSeedRepo(t, h, "milestone-create-src")
	compatSeedRepo(t, h, "milestone-create-target")

	srcMS, err := h.Svc.CreateMilestone(ctx, "testuser/milestone-create-src", "src-ms", "", "open")
	if err != nil {
		t.Fatalf("create source milestone: %v", err)
	}
	targetByNumber := createMilestoneByNumber(t, h, ctx, "testuser/milestone-create-target", int(srcMS.ID))
	if targetByNumber.ID == srcMS.ID {
		t.Fatalf("expected differing IDs for source/target milestones, both got %d", srcMS.ID)
	}

	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/milestone-create-target/issues", map[string]any{
		"title":     "issue with milestone",
		"milestone": int(srcMS.ID),
	})
	assertStatusCode(t, w, 201)
	resp := testharness.DecodeJSON(t, w)
	msObj, ok := resp["milestone"].(map[string]any)
	if !ok {
		t.Fatalf("milestone response type: got %T", resp["milestone"])
	}
	gotID, ok := msObj["id"].(float64)
	if !ok {
		t.Fatalf("milestone.id type: got %T", msObj["id"])
	}
	if uint(gotID) != targetByNumber.ID {
		t.Fatalf("milestone binding mismatch on create: got milestone id %d, want %d", uint(gotID), targetByNumber.ID)
	}
}

func TestUpdateIssue_MilestoneUsesNumberNotID(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	compatSeedRepo(t, h, "milestone-src")
	compatSeedRepo(t, h, "milestone-target")

	// Create issue in target repo.
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/milestone-target/issues", map[string]any{
		"title": "target issue",
	})
	assertStatusCode(t, w, 201)

	// Milestone ID from source repo.
	srcMS, err := h.Svc.CreateMilestone(ctx, "testuser/milestone-src", "src-ms", "", "open")
	if err != nil {
		t.Fatalf("create source milestone: %v", err)
	}

	// Create target milestones until milestone number equals source milestone ID.
	targetByNumber := createMilestoneByNumber(t, h, ctx, "testuser/milestone-target", int(srcMS.ID))
	if targetByNumber.ID == srcMS.ID {
		t.Fatalf("expected differing IDs for source/target milestones, both got %d", srcMS.ID)
	}

	// PATCH uses milestone number. If server mistakenly treats it as DB ID,
	// issue will bind to srcMS.ID instead of targetByNumber.ID.
	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/milestone-target/issues/1", map[string]any{
		"milestone": int(srcMS.ID),
	})
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)

	msObj, ok := resp["milestone"].(map[string]any)
	if !ok {
		t.Fatalf("milestone response type: got %T", resp["milestone"])
	}
	gotID, ok := msObj["id"].(float64)
	if !ok {
		t.Fatalf("milestone.id type: got %T", msObj["id"])
	}
	if uint(gotID) != targetByNumber.ID {
		t.Fatalf("milestone binding mismatch: got milestone id %d, want %d", uint(gotID), targetByNumber.ID)
	}

	issue, err := h.Svc.GetIssue(ctx, "testuser/milestone-target", 1)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.MilestoneID == nil || *issue.MilestoneID != targetByNumber.ID {
		t.Fatalf("issue milestone id: got %v, want %d", issue.MilestoneID, targetByNumber.ID)
	}
}

func TestUpdateIssue_PRFallback_MilestoneUsesNumberNotID(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	compatSeedRepo(t, h, "milestone-pr-src")
	compatSeedRepo(t, h, "milestone-pr-target")

	fullTarget := "testuser/milestone-pr-target"
	if err := h.Svc.Git.CreateBranch(ctx, fullTarget, "feature", "main"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	pr, err := h.Svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: fullTarget,
		Title:        "test pr",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	srcMS, err := h.Svc.CreateMilestone(ctx, "testuser/milestone-pr-src", "src-pr-ms", "", "open")
	if err != nil {
		t.Fatalf("create source milestone: %v", err)
	}
	targetByNumber := createMilestoneByNumber(t, h, ctx, fullTarget, int(srcMS.ID))
	if targetByNumber.ID == srcMS.ID {
		t.Fatalf("expected differing IDs for source/target milestones, both got %d", srcMS.ID)
	}

	w := h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/milestone-pr-target/issues/"+strconv.Itoa(pr.Number), map[string]any{
		"milestone": int(srcMS.ID),
	})
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)
	msObj, ok := resp["milestone"].(map[string]any)
	if !ok {
		t.Fatalf("milestone response type: got %T", resp["milestone"])
	}
	gotID, ok := msObj["id"].(float64)
	if !ok {
		t.Fatalf("milestone.id type: got %T", msObj["id"])
	}
	if uint(gotID) != targetByNumber.ID {
		t.Fatalf("PR milestone binding mismatch: got milestone id %d, want %d", uint(gotID), targetByNumber.ID)
	}
}

func TestUpdateIssue_ClearMilestoneWithNull(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "milestone-clear-null")

	ms, err := h.Svc.CreateMilestone(ctx, "testuser/milestone-clear-null", "v1.0", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}

	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/milestone-clear-null/issues", map[string]any{
		"title":     "issue with milestone",
		"milestone": ms.Number,
	})
	assertStatusCode(t, w, 201)

	w = h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/milestone-clear-null/issues/1", map[string]any{
		"milestone": nil,
	})
	assertStatusCode(t, w, 200)
	resp := testharness.DecodeJSON(t, w)
	if resp["milestone"] != nil {
		t.Fatalf("expected milestone to be cleared, got %v", resp["milestone"])
	}

	issue, err := h.Svc.GetIssue(ctx, "testuser/milestone-clear-null", 1)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.MilestoneID != nil {
		t.Fatalf("expected issue milestone_id nil after clear, got %v", issue.MilestoneID)
	}
}

func TestMilestoneList_SortByNumber(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "milestone-sort-number")
	full := "testuser/milestone-sort-number"

	for i := 1; i <= 3; i++ {
		if _, err := h.Svc.CreateMilestone(ctx, full, fmt.Sprintf("m-%d", i), "", "open"); err != nil {
			t.Fatalf("CreateMilestone #%d: %v", i, err)
		}
	}

	asc := listMilestoneNumbers(t, h, "/api/v3/repos/testuser/milestone-sort-number/milestones?sort=number&direction=asc")
	assertMilestoneOrder(t, asc, []int{1, 2, 3})

	desc := listMilestoneNumbers(t, h, "/api/v3/repos/testuser/milestone-sort-number/milestones?sort=number&direction=desc")
	assertMilestoneOrder(t, desc, []int{3, 2, 1})
}

func TestMilestoneList_SortByCreated(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "milestone-sort-created")
	full := "testuser/milestone-sort-created"

	ms1, err := h.Svc.CreateMilestone(ctx, full, "m-1", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone m-1: %v", err)
	}
	ms2, err := h.Svc.CreateMilestone(ctx, full, "m-2", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone m-2: %v", err)
	}
	ms3, err := h.Svc.CreateMilestone(ctx, full, "m-3", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone m-3: %v", err)
	}

	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(2 * time.Hour)
	t3 := t1.Add(4 * time.Hour)

	updateMilestoneColumns(t, h, ms1.ID, map[string]any{"created_at": t2})
	updateMilestoneColumns(t, h, ms2.ID, map[string]any{"created_at": t1})
	updateMilestoneColumns(t, h, ms3.ID, map[string]any{"created_at": t3})

	asc := listMilestoneNumbers(t, h, "/api/v3/repos/testuser/milestone-sort-created/milestones?sort=created&direction=asc")
	assertMilestoneOrder(t, asc, []int{ms2.Number, ms1.Number, ms3.Number})

	desc := listMilestoneNumbers(t, h, "/api/v3/repos/testuser/milestone-sort-created/milestones?sort=created&direction=desc")
	assertMilestoneOrder(t, desc, []int{ms3.Number, ms1.Number, ms2.Number})
}

func TestMilestoneList_SortByUpdated(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "milestone-sort-updated")
	full := "testuser/milestone-sort-updated"

	ms1, err := h.Svc.CreateMilestone(ctx, full, "m-1", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone m-1: %v", err)
	}
	ms2, err := h.Svc.CreateMilestone(ctx, full, "m-2", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone m-2: %v", err)
	}
	ms3, err := h.Svc.CreateMilestone(ctx, full, "m-3", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone m-3: %v", err)
	}

	t1 := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(3 * time.Hour)
	t3 := t1.Add(6 * time.Hour)

	updateMilestoneColumns(t, h, ms1.ID, map[string]any{"updated_at": t2})
	updateMilestoneColumns(t, h, ms2.ID, map[string]any{"updated_at": t3})
	updateMilestoneColumns(t, h, ms3.ID, map[string]any{"updated_at": t1})

	asc := listMilestoneNumbers(t, h, "/api/v3/repos/testuser/milestone-sort-updated/milestones?sort=updated&direction=asc")
	assertMilestoneOrder(t, asc, []int{ms3.Number, ms1.Number, ms2.Number})

	desc := listMilestoneNumbers(t, h, "/api/v3/repos/testuser/milestone-sort-updated/milestones?sort=updated&direction=desc")
	assertMilestoneOrder(t, desc, []int{ms2.Number, ms1.Number, ms3.Number})
}

func TestMilestoneList_SortByDueOn(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "milestone-sort-dueon")
	full := "testuser/milestone-sort-dueon"

	ms1, err := h.Svc.CreateMilestone(ctx, full, "m-1", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone m-1: %v", err)
	}
	ms2, err := h.Svc.CreateMilestone(ctx, full, "m-2", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone m-2: %v", err)
	}
	ms3, err := h.Svc.CreateMilestone(ctx, full, "m-3", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone m-3: %v", err)
	}

	t1 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(24 * time.Hour)
	t3 := t1.Add(48 * time.Hour)

	updateMilestoneColumns(t, h, ms1.ID, map[string]any{"due_on": t2})
	updateMilestoneColumns(t, h, ms2.ID, map[string]any{"due_on": t3})
	updateMilestoneColumns(t, h, ms3.ID, map[string]any{"due_on": t1})

	asc := listMilestoneNumbers(t, h, "/api/v3/repos/testuser/milestone-sort-dueon/milestones?sort=due_on&direction=asc")
	assertMilestoneOrder(t, asc, []int{ms3.Number, ms1.Number, ms2.Number})

	desc := listMilestoneNumbers(t, h, "/api/v3/repos/testuser/milestone-sort-dueon/milestones?sort=due_on&direction=desc")
	assertMilestoneOrder(t, desc, []int{ms2.Number, ms1.Number, ms3.Number})
}

func TestMilestoneList_Pagination(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "milestone-pagination")
	full := "testuser/milestone-pagination"

	for i := 1; i <= 5; i++ {
		if _, err := h.Svc.CreateMilestone(ctx, full, fmt.Sprintf("m-%d", i), "", "open"); err != nil {
			t.Fatalf("CreateMilestone #%d: %v", i, err)
		}
	}

	path := "/api/v3/repos/testuser/milestone-pagination/milestones?sort=number&direction=asc&page=2&per_page=2"
	w := h.DoREST(t, "GET", path, nil)
	assertStatusCode(t, w, 200)
	items := testharness.DecodeJSONArray(t, w)
	if len(items) != 2 {
		t.Fatalf("expected 2 milestones on page 2, got %d", len(items))
	}
	n1, ok := items[0]["number"].(float64)
	if !ok {
		t.Fatalf("milestone[0].number type: got %T", items[0]["number"])
	}
	n2, ok := items[1]["number"].(float64)
	if !ok {
		t.Fatalf("milestone[1].number type: got %T", items[1]["number"])
	}
	assertMilestoneOrder(t, []int{int(n1), int(n2)}, []int{3, 4})
}

func TestMilestoneList_PaginationWithTotal(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "milestone-pagination-total")
	full := "testuser/milestone-pagination-total"

	for i := 1; i <= 5; i++ {
		if _, err := h.Svc.CreateMilestone(ctx, full, fmt.Sprintf("m-%d", i), "", "open"); err != nil {
			t.Fatalf("CreateMilestone #%d: %v", i, err)
		}
	}

	path := "/api/v3/repos/testuser/milestone-pagination-total/milestones?sort=number&direction=asc&page=2&per_page=2"
	w := h.DoREST(t, "GET", path, nil)
	assertStatusCode(t, w, 200)
	link := w.Header().Get("Link")
	if link == "" {
		t.Fatal("expected Link header for pagination, got none")
	}
	if !strings.Contains(link, "rel=\"last\"") || !strings.Contains(link, "page=3") {
		t.Fatalf("expected Link header to include last page=3, got %q", link)
	}
}

func TestMilestoneList_Counts(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "milestone-list-counts")
	full := "testuser/milestone-list-counts"

	ms1, err := h.Svc.CreateMilestone(ctx, full, "m-1", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone m-1: %v", err)
	}
	ms2, err := h.Svc.CreateMilestone(ctx, full, "m-2", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone m-2: %v", err)
	}

	issueOpen, err := h.Svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: full,
		Title:        "issue-open",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue issue-open: %v", err)
	}
	if err := h.Svc.SetIssueMilestone(ctx, issueOpen.ID, &ms1.ID); err != nil {
		t.Fatalf("SetIssueMilestone issue-open: %v", err)
	}

	closed := db.StateClosed
	issueClosed, err := h.Svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: full,
		Title:        "issue-closed",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue issue-closed: %v", err)
	}
	if err := h.Svc.SetIssueMilestone(ctx, issueClosed.ID, &ms1.ID); err != nil {
		t.Fatalf("SetIssueMilestone issue-closed: %v", err)
	}
	if _, err := h.Svc.UpdateIssue(ctx, full, issueClosed.Number, service.UpdateIssueInput{State: &closed}); err != nil {
		t.Fatalf("UpdateIssue issue-closed: %v", err)
	}

	repo, err := h.Svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if err := h.DB.Create(&db.PullRequest{
		Number:           1,
		RepositoryID:     repo.ID,
		HeadRepositoryID: repo.ID,
		Title:            "pr-open",
		HeadRef:          "feature-open",
		BaseRef:          "main",
		State:            db.StateOpen,
		AuthorID:         repo.OwnerID,
		MilestoneID:      &ms1.ID,
	}).Error; err != nil {
		t.Fatalf("create open PR row: %v", err)
	}
	if err := h.DB.Create(&db.PullRequest{
		Number:           2,
		RepositoryID:     repo.ID,
		HeadRepositoryID: repo.ID,
		Title:            "pr-closed",
		HeadRef:          "feature-closed",
		BaseRef:          "main",
		State:            db.StateClosed,
		AuthorID:         repo.OwnerID,
		MilestoneID:      &ms1.ID,
	}).Error; err != nil {
		t.Fatalf("create closed PR row: %v", err)
	}

	issueOther, err := h.Svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: full,
		Title:        "issue-other",
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue issue-other: %v", err)
	}
	if err := h.Svc.SetIssueMilestone(ctx, issueOther.ID, &ms2.ID); err != nil {
		t.Fatalf("SetIssueMilestone issue-other: %v", err)
	}

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/milestone-list-counts/milestones?sort=number&direction=asc", nil)
	assertStatusCode(t, w, 200)
	items := testharness.DecodeJSONArray(t, w)
	if len(items) != 2 {
		t.Fatalf("expected 2 milestones, got %d", len(items))
	}

	assertMilestoneCounts(t, items[0], 1, 2, 2)
	assertMilestoneCounts(t, items[1], 2, 1, 0)
}

func TestMilestoneList_IncludeIssueCount(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "milestone-include-issue-count")
	full := "testuser/milestone-include-issue-count"

	ms, err := h.Svc.CreateMilestone(ctx, full, "topic-alpha", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}
	issue := createMilestoneIssue(t, h, ctx, full, ms.ID, "issue-alpha")
	closed := db.StateClosed
	if _, err := h.Svc.UpdateIssue(ctx, full, issue.Number, service.UpdateIssueInput{State: &closed}); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/milestone-include-issue-count/milestones?include_issue_count=true", nil)
	assertStatusCode(t, w, 200)

	items := testharness.DecodeJSONArray(t, w)
	if len(items) != 1 {
		t.Fatalf("expected 1 milestone, got %d", len(items))
	}
	assertMilestoneCounts(t, items[0], 1, 0, 1)
}

func TestMilestoneList_InvalidIncludeIssueCount(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "milestone-invalid-include-issue-count")

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/milestone-invalid-include-issue-count/milestones?include_issue_count=maybe", nil)
	assertValidationError(t, w, "include_issue_count must be a boolean")
}

func TestMilestoneIssues_Success(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "milestone-issues-success")
	full := "testuser/milestone-issues-success"

	ms1, err := h.Svc.CreateMilestone(ctx, full, "topic-alpha", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone(ms1): %v", err)
	}
	ms2, err := h.Svc.CreateMilestone(ctx, full, "topic-beta", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone(ms2): %v", err)
	}

	createMilestoneIssue(t, h, ctx, full, ms1.ID, "alpha issue")
	closedIssue := createMilestoneIssue(t, h, ctx, full, ms1.ID, "alpha issue closed")
	closed := db.StateClosed
	if _, err := h.Svc.UpdateIssue(ctx, full, closedIssue.Number, service.UpdateIssueInput{State: &closed}); err != nil {
		t.Fatalf("UpdateIssue(closedIssue): %v", err)
	}
	createMilestoneIssue(t, h, ctx, full, ms2.ID, "beta issue")

	repo, err := h.Svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if err := h.DB.Create(&db.PullRequest{
		Number:           7,
		RepositoryID:     repo.ID,
		HeadRepositoryID: repo.ID,
		Title:            "alpha pr",
		HeadRef:          "feature-alpha",
		BaseRef:          "main",
		State:            db.StateOpen,
		AuthorID:         repo.OwnerID,
		MilestoneID:      &ms1.ID,
	}).Error; err != nil {
		t.Fatalf("create PR: %v", err)
	}

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/milestone-issues-success/milestones/"+strconv.Itoa(ms1.Number)+"/issues?state=all&sort=created&direction=asc", nil)
	assertStatusCode(t, w, 200)

	resp := testharness.DecodeJSONArray(t, w)
	if got := issueTitlesFromResponse(t, resp); !sameStrings(got, []string{"alpha issue", "alpha issue closed"}) {
		t.Fatalf("milestone issue titles: got %v", got)
	}
}

func TestIssueList_FilterByMilestoneTitle(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "issue-filter-milestone-title")
	full := "testuser/issue-filter-milestone-title"

	msAlpha, err := h.Svc.CreateMilestone(ctx, full, "Topic Alpha", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone(msAlpha): %v", err)
	}
	msBeta, err := h.Svc.CreateMilestone(ctx, full, "Topic Beta", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone(msBeta): %v", err)
	}

	createMilestoneIssue(t, h, ctx, full, msAlpha.ID, "alpha issue")
	createMilestoneIssue(t, h, ctx, full, msBeta.ID, "beta issue")

	repo, err := h.Svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if err := h.DB.Create(&db.PullRequest{
		Number:           11,
		RepositoryID:     repo.ID,
		HeadRepositoryID: repo.ID,
		Title:            "alpha pr",
		HeadRef:          "feature-alpha",
		BaseRef:          "main",
		State:            db.StateOpen,
		AuthorID:         repo.OwnerID,
		MilestoneID:      &msAlpha.ID,
	}).Error; err != nil {
		t.Fatalf("create alpha PR: %v", err)
	}
	if err := h.DB.Create(&db.PullRequest{
		Number:           12,
		RepositoryID:     repo.ID,
		HeadRepositoryID: repo.ID,
		Title:            "beta pr",
		HeadRef:          "feature-beta",
		BaseRef:          "main",
		State:            db.StateOpen,
		AuthorID:         repo.OwnerID,
		MilestoneID:      &msBeta.ID,
	}).Error; err != nil {
		t.Fatalf("create beta PR: %v", err)
	}

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/issue-filter-milestone-title/issues?state=all&creator=testuser&milestone=Topic+Alpha&sort=created&direction=asc", nil)
	assertStatusCode(t, w, 200)

	resp := testharness.DecodeJSONArray(t, w)
	if got := issueTitlesFromResponse(t, resp); !sameStrings(got, []string{"alpha issue", "alpha pr"}) {
		t.Fatalf("filtered titles: got %v", got)
	}
}

func TestMilestoneList_InvalidSort(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "milestone-invalid-sort")

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/milestone-invalid-sort/milestones?sort=invalid", nil)
	assertValidationError(t, w, "sort must be one of: created, updated, due_on, number")
}

func TestMilestoneList_InvalidDirection(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "milestone-invalid-direction")

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/milestone-invalid-direction/milestones?direction=sideways", nil)
	assertValidationError(t, w, "direction must be one of: asc, desc")
}

func TestMilestoneList_InvalidSort_ResponseShape(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "milestone-invalid-sort-shape")

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/milestone-invalid-sort-shape/milestones?sort=bad", nil)
	assertStatusCode(t, w, 422)
	resp := testharness.DecodeJSON(t, w)
	if resp["error"] == nil {
		t.Fatalf("expected error field in response, got %v", resp)
	}
	if resp["error"] != "sort must be one of: created, updated, due_on, number" {
		t.Fatalf("error message: got %v", resp["error"])
	}
}

func TestMilestoneList_InvalidState(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "milestone-invalid-state")

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/milestone-invalid-state/milestones?state=unknown", nil)
	assertValidationError(t, w, "state must be one of: open, closed, all")
}

func TestMilestoneCreate_InvalidState(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "milestone-create-invalid-state")

	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/milestone-create-invalid-state/milestones", map[string]any{
		"title": "bad state",
		"state": "invalid",
	})
	assertValidationError(t, w, "state must be one of: open, closed")
}

func TestMilestoneUpdate_InvalidState(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "milestone-update-invalid-state")

	if _, err := h.Svc.CreateMilestone(ctx, "testuser/milestone-update-invalid-state", "m-1", "", "open"); err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}

	w := h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/milestone-update-invalid-state/milestones/1", map[string]any{
		"state": "invalid",
	})
	assertValidationError(t, w, "state must be one of: open, closed")
}

func TestMilestoneCreate_InvalidDueOn(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "milestone-create-invalid-dueon")

	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/milestone-create-invalid-dueon/milestones", map[string]any{
		"title":  "bad due_on",
		"due_on": "not-a-date",
	})
	assertValidationError(t, w, "due_on must be ISO 8601")
}

func TestMilestoneUpdate_InvalidDueOn(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "milestone-update-invalid-dueon")

	if _, err := h.Svc.CreateMilestone(ctx, "testuser/milestone-update-invalid-dueon", "m-1", "", "open"); err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}

	w := h.DoRESTJSON(t, "PATCH", "/api/v3/repos/testuser/milestone-update-invalid-dueon/milestones/1", map[string]any{
		"due_on": "invalid-date",
	})
	assertValidationError(t, w, "due_on must be ISO 8601")
}

func listMilestoneNumbers(t *testing.T, h *testharness.Harness, path string) []int {
	t.Helper()
	w := h.DoREST(t, "GET", path, nil)
	assertStatusCode(t, w, 200)
	items := testharness.DecodeJSONArray(t, w)
	out := make([]int, len(items))
	for i, item := range items {
		num, ok := item["number"].(float64)
		if !ok {
			t.Fatalf("milestone[%d].number type: got %T", i, item["number"])
		}
		out[i] = int(num)
	}
	return out
}

func assertMilestoneOrder(t *testing.T, got, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("milestone count: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("milestone order: got %v, want %v", got, want)
		}
	}
}

func assertMilestoneCounts(t *testing.T, item map[string]any, wantNumber int, wantOpen int64, wantClosed int64) {
	t.Helper()

	gotNumber, ok := item["number"].(float64)
	if !ok {
		t.Fatalf("milestone.number type: got %T", item["number"])
	}
	if int(gotNumber) != wantNumber {
		t.Fatalf("milestone number: got %d, want %d", int(gotNumber), wantNumber)
	}

	gotOpen, ok := item["open_issues"].(float64)
	if !ok {
		t.Fatalf("open_issues type: got %T", item["open_issues"])
	}
	if int64(gotOpen) != wantOpen {
		t.Fatalf("open_issues: got %d, want %d", int64(gotOpen), wantOpen)
	}

	gotClosed, ok := item["closed_issues"].(float64)
	if !ok {
		t.Fatalf("closed_issues type: got %T", item["closed_issues"])
	}
	if int64(gotClosed) != wantClosed {
		t.Fatalf("closed_issues: got %d, want %d", int64(gotClosed), wantClosed)
	}
}

func updateMilestoneColumns(t *testing.T, h *testharness.Harness, id uint, updates map[string]any) {
	t.Helper()
	if err := h.DB.Model(&db.Milestone{}).Where("id = ?", id).UpdateColumns(updates).Error; err != nil {
		t.Fatalf("update milestone %d: %v", id, err)
	}
}

func assertValidationError(t *testing.T, w *httptest.ResponseRecorder, want string) {
	t.Helper()
	assertStatusCode(t, w, 422)
	resp := testharness.DecodeJSON(t, w)
	if resp["error"] != want {
		t.Fatalf("error message: got %v, want %q", resp["error"], want)
	}
	if resp["message"] != want {
		t.Fatalf("message: got %v, want %q", resp["message"], want)
	}
}

func TestListMilestoneLabels_Success(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "milestone-labels-success")

	full := "testuser/milestone-labels-success"
	ms1, err := h.Svc.CreateMilestone(ctx, full, "v1.0", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone(ms1): %v", err)
	}
	ms2, err := h.Svc.CreateMilestone(ctx, full, "v2.0", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone(ms2): %v", err)
	}
	if _, err := h.Svc.CreateLabel(ctx, full, "ml-bug", "ff0000", ""); err != nil {
		t.Fatalf("CreateLabel(ml-bug): %v", err)
	}
	if _, err := h.Svc.CreateLabel(ctx, full, "ml-docs", "00ff00", ""); err != nil {
		t.Fatalf("CreateLabel(ml-docs): %v", err)
	}
	if _, err := h.Svc.CreateLabel(ctx, full, "ml-chore", "0000ff", ""); err != nil {
		t.Fatalf("CreateLabel(ml-chore): %v", err)
	}

	issue1 := createMilestoneIssue(t, h, ctx, full, ms1.ID, "issue-1")
	if _, err := h.Svc.AddIssueLabels(ctx, full, issue1.Number, []string{"ml-bug"}); err != nil {
		t.Fatalf("AddIssueLabels(issue-1): %v", err)
	}

	issue2 := createMilestoneIssue(t, h, ctx, full, ms1.ID, "issue-2")
	if _, err := h.Svc.AddIssueLabels(ctx, full, issue2.Number, []string{"ml-bug", "ml-docs"}); err != nil {
		t.Fatalf("AddIssueLabels(issue-2): %v", err)
	}

	issue3 := createMilestoneIssue(t, h, ctx, full, ms2.ID, "issue-3")
	if _, err := h.Svc.AddIssueLabels(ctx, full, issue3.Number, []string{"ml-chore"}); err != nil {
		t.Fatalf("AddIssueLabels(issue-3): %v", err)
	}

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/milestone-labels-success/milestones/"+strconv.Itoa(ms1.Number)+"/labels", nil)
	assertStatusCode(t, w, 200)

	resp := testharness.DecodeJSONArray(t, w)
	if len(resp) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(resp))
	}
	counts := labelNameCounts(t, resp)
	if counts["ml-bug"] != 1 || counts["ml-docs"] != 1 {
		t.Fatalf("expected ml-bug and ml-docs once each, got %v", counts)
	}
	if counts["ml-chore"] != 0 {
		t.Fatalf("expected ml-chore to be excluded, got %v", counts)
	}
}

func TestListMilestoneLabels_Empty(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "milestone-labels-empty")

	full := "testuser/milestone-labels-empty"
	ms, err := h.Svc.CreateMilestone(ctx, full, "v1.0", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}
	createMilestoneIssue(t, h, ctx, full, ms.ID, "issue-no-labels")

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/milestone-labels-empty/milestones/"+strconv.Itoa(ms.Number)+"/labels", nil)
	assertStatusCode(t, w, 200)

	resp := testharness.DecodeJSONArray(t, w)
	if len(resp) != 0 {
		t.Fatalf("expected 0 labels, got %d", len(resp))
	}
}

func TestListMilestoneLabels_NotFound(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "milestone-labels-notfound")

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/milestone-labels-notfound/milestones/999/labels", nil)
	assertStatusCode(t, w, 404)
}

func createMilestoneByNumber(t *testing.T, h *testharness.Harness, ctx context.Context, fullName string, wantNumber int) db.Milestone {
	t.Helper()
	if wantNumber <= 0 {
		t.Fatalf("wantNumber must be positive, got %d", wantNumber)
	}
	for i := 1; i <= wantNumber+5; i++ {
		m, err := h.Svc.CreateMilestone(ctx, fullName, fmt.Sprintf("m-%d", i), "", "open")
		if err != nil {
			t.Fatalf("CreateMilestone(%s #%d): %v", fullName, i, err)
		}
		if m.Number == wantNumber {
			return m
		}
	}
	t.Fatalf("failed to create milestone number %d in %s", wantNumber, fullName)
	return db.Milestone{}
}

func createMilestoneIssue(t *testing.T, h *testharness.Harness, ctx context.Context, full string, milestoneID uint, title string) db.Issue {
	t.Helper()
	issue, err := h.Svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: full,
		Title:        title,
		AuthorLogin:  h.User.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue(%s): %v", title, err)
	}
	if err := h.Svc.SetIssueMilestone(ctx, issue.ID, &milestoneID); err != nil {
		t.Fatalf("SetIssueMilestone(%s): %v", title, err)
	}
	return issue
}

func labelNameCounts(t *testing.T, labels []map[string]any) map[string]int {
	t.Helper()
	counts := make(map[string]int, len(labels))
	for i, label := range labels {
		name, ok := label["name"].(string)
		if !ok {
			t.Fatalf("label %d missing name, got %T", i, label["name"])
		}
		counts[name]++
	}
	return counts
}

func createPrivateMilestoneRepo(t *testing.T, h *testharness.Harness, name string) string {
	t.Helper()

	w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
		"name":      name,
		"private":   true,
		"auto_init": true,
	})
	assertStatusCode(t, w, 201)

	return "testuser/" + name
}

func issueTitlesFromResponse(t *testing.T, items []map[string]any) []string {
	t.Helper()
	titles := make([]string, 0, len(items))
	for i, item := range items {
		title, ok := item["title"].(string)
		if !ok {
			t.Fatalf("item %d missing title, got %T", i, item["title"])
		}
		titles = append(titles, title)
	}
	return titles
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
