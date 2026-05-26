package service_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

func TestIssueFlow(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// 1. Setup user and repo
	svc.DB.Create(&db.User{Login: "testuser"})
	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "testuser", Name: "repo1"})
	if err != nil {
		t.Fatalf("failed to setup repo: %v", err)
	}

	// 2. Create Issue
	in := service.CreateIssueInput{
		RepoFullName: "testuser/repo1",
		Title:        "First Issue",
		Body:         "Hello world",
		AuthorLogin:  "testuser",
	}
	issue, err := svc.CreateIssue(ctx, in)
	if err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if issue.Number != 1 {
		t.Errorf("expected issue number 1, got %d", issue.Number)
	}

	// 3. Update Issue
	newState := "closed"
	updated, err := svc.UpdateIssue(ctx, "testuser/repo1", issue.Number, service.UpdateIssueInput{State: &newState})
	if err != nil {
		t.Fatalf("failed to update issue: %v", err)
	}
	if updated.State != "closed" {
		t.Errorf("expected closed state, got %s", updated.State)
	}

	// 4. Add Comment
	comment, err := svc.CreateIssueComment(ctx, "testuser/repo1", issue.Number, "LGTM", "testuser", nil)
	if err != nil {
		t.Fatalf("failed to add comment: %v", err)
	}
	if comment.Body != "LGTM" {
		t.Errorf("expected comment body LGTM")
	}

	// 5. List Issues
	issues, err := svc.ListIssues(ctx, "testuser/repo1", "all", "", "", "", "", "")
	if err != nil {
		t.Fatalf("failed to list issues: %v", err)
	}
	if len(issues) != 1 {
		t.Errorf("expected 1 issue, got %d", len(issues))
	}
}

func TestListIssuesForRESTOmitsBodyOnlyOnRESTPath(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "bodyuser", "bodyrepo")

	repo, err := svc.GetRepo(ctx, "bodyuser/bodyrepo")
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	var author db.User
	if err := svc.DB.First(&author, "login = ?", "bodyuser").Error; err != nil {
		t.Fatalf("load author: %v", err)
	}
	if err := svc.DB.Create(&db.Issue{
		Number:       1,
		RepositoryID: repo.ID,
		Title:        "Issue With Body",
		Body:         "expected issue body",
		State:        db.StateOpen,
		AuthorID:     author.ID,
	}).Error; err != nil {
		t.Fatalf("seed issue: %v", err)
	}

	issues, err := svc.ListIssues(ctx, "bodyuser/bodyrepo", "all", "", "", "", "", "")
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("ListIssues: got %d issues, want 1", len(issues))
	}
	if issues[0].Body != "expected issue body" {
		t.Fatalf("ListIssues: got body %q, want %q", issues[0].Body, "expected issue body")
	}

	restIssues, err := svc.ListIssuesForREST(ctx, "bodyuser/bodyrepo", "all", "", "", "", "", "")
	if err != nil {
		t.Fatalf("ListIssuesForREST: %v", err)
	}
	if len(restIssues) != 1 {
		t.Fatalf("ListIssuesForREST: got %d issues, want 1", len(restIssues))
	}
	if restIssues[0].Body != "" {
		t.Fatalf("ListIssuesForREST: got body %q, want empty body", restIssues[0].Body)
	}
}

func TestListIssuesForRESTPagePaginatesBeyondDefaultListLimit(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "pageuser", "pagerepo")
	repo, err := svc.GetRepo(ctx, "pageuser/pagerepo")
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	var author db.User
	if err := svc.DB.First(&author, "login = ?", "pageuser").Error; err != nil {
		t.Fatalf("load author: %v", err)
	}

	base := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	issues := make([]db.Issue, 1005)
	for i := range issues {
		number := i + 1
		created := base.Add(time.Duration(number) * time.Second)
		issues[i] = db.Issue{
			Number:       number,
			RepositoryID: repo.ID,
			Title:        fmt.Sprintf("Issue %04d", number),
			Body:         "body",
			State:        db.StateOpen,
			AuthorID:     author.ID,
			CreatedAt:    created,
			UpdatedAt:    created,
		}
	}
	if err := svc.DB.CreateInBatches(&issues, 200).Error; err != nil {
		t.Fatalf("seed issues: %v", err)
	}

	page, err := svc.ListIssuesForRESTPage(ctx, service.IssueListPageFilter{
		RepoFullName:  repo.FullName,
		State:         db.StateOpen,
		Page:          11,
		PerPage:       100,
		OmitIssueBody: true,
	})
	if err != nil {
		t.Fatalf("ListIssuesForRESTPage: %v", err)
	}
	if page.Total != 1005 {
		t.Fatalf("total = %d, want 1005", page.Total)
	}
	if len(page.Items) != 5 {
		t.Fatalf("page length = %d, want 5", len(page.Items))
	}
	for i, item := range page.Items {
		if item.Issue == nil {
			t.Fatalf("item %d is not an issue: %#v", i, item)
		}
		wantNumber := 5 - i
		if item.Issue.Number != wantNumber {
			t.Fatalf("item %d number = %d, want %d", i, item.Issue.Number, wantNumber)
		}
		if item.Issue.Body != "" {
			t.Fatalf("item %d body = %q, want omitted body", i, item.Issue.Body)
		}
	}
}

func TestListIssuesForRESTPageSortsCommentsAcrossIssuesAndPRs(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "commentpage", "repo")
	repo, err := svc.GetRepo(ctx, "commentpage/repo")
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	var author db.User
	if err := svc.DB.First(&author, "login = ?", "commentpage").Error; err != nil {
		t.Fatalf("load author: %v", err)
	}

	base := time.Date(2026, 5, 24, 11, 0, 0, 0, time.UTC)
	seedIssues := []db.Issue{
		{Number: 1, RepositoryID: repo.ID, Title: "one comment", State: db.StateOpen, AuthorID: author.ID, CreatedAt: base, UpdatedAt: base},
		{Number: 2, RepositoryID: repo.ID, Title: "three comments", State: db.StateOpen, AuthorID: author.ID, CreatedAt: base.Add(time.Second), UpdatedAt: base.Add(time.Second)},
	}
	if err := svc.DB.Create(&seedIssues).Error; err != nil {
		t.Fatalf("seed issues: %v", err)
	}
	pr := db.PullRequest{
		Number:           3,
		RepositoryID:     repo.ID,
		HeadRepositoryID: repo.ID,
		Title:            "two comments",
		State:            db.StateOpen,
		AuthorID:         author.ID,
		CreatedAt:        base.Add(2 * time.Second),
		UpdatedAt:        base.Add(2 * time.Second),
	}
	if err := svc.DB.Create(&pr).Error; err != nil {
		t.Fatalf("seed pr: %v", err)
	}
	var comments []db.IssueComment
	for issueNumber, count := range map[int]int{1: 1, 2: 3, 3: 2} {
		for i := 0; i < count; i++ {
			comments = append(comments, db.IssueComment{
				RepositoryID: repo.ID,
				IssueNumber:  issueNumber,
				Body:         db.LargeText(fmt.Sprintf("comment %d", i)),
				AuthorID:     author.ID,
			})
		}
	}
	if err := svc.DB.Create(&comments).Error; err != nil {
		t.Fatalf("seed comments: %v", err)
	}

	page, err := svc.ListIssuesForRESTPage(ctx, service.IssueListPageFilter{
		RepoFullName: repo.FullName,
		State:        db.StateOpen,
		Sort:         "comments",
		Direction:    "desc",
		Page:         1,
		PerPage:      3,
	})
	if err != nil {
		t.Fatalf("ListIssuesForRESTPage: %v", err)
	}
	if page.Total != 3 {
		t.Fatalf("total = %d, want 3", page.Total)
	}
	if len(page.Items) != 3 {
		t.Fatalf("page length = %d, want 3", len(page.Items))
	}
	wantNumbers := []int{2, 3, 1}
	wantComments := []int64{3, 2, 1}
	for i, item := range page.Items {
		var number int
		switch {
		case item.Issue != nil:
			number = item.Issue.Number
		case item.PullRequest != nil:
			number = item.PullRequest.Number
		default:
			t.Fatalf("item %d has no issue or PR", i)
		}
		if number != wantNumbers[i] || item.Comments != wantComments[i] {
			t.Fatalf("item %d = number %d comments %d, want number %d comments %d", i, number, item.Comments, wantNumbers[i], wantComments[i])
		}
	}
	if page.Items[1].PullRequest == nil {
		t.Fatalf("second item should be the PR")
	}
}

func TestIssueCloseReopen(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "cruser", "crrepo")

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "cruser/crrepo",
		Title:        "Close Reopen Test",
		Body:         "body",
		AuthorLogin:  "cruser",
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if issue.State != "open" {
		t.Errorf("initial state: got %s, want open", issue.State)
	}
	if issue.ClosedAt != nil {
		t.Error("initial ClosedAt should be nil")
	}

	// Close
	closed := "closed"
	updated, err := svc.UpdateIssue(ctx, "cruser/crrepo", issue.Number, service.UpdateIssueInput{State: &closed})
	if err != nil {
		t.Fatalf("close issue: %v", err)
	}
	if updated.State != "closed" {
		t.Errorf("after close: state got %s, want closed", updated.State)
	}
	if updated.ClosedAt == nil {
		t.Error("after close: ClosedAt should not be nil")
	}
	if updated.StateReason != db.StateReasonCompleted {
		t.Errorf("after close: state_reason got %s, want %s", updated.StateReason, db.StateReasonCompleted)
	}

	// Reopen
	open := "open"
	updated, err = svc.UpdateIssue(ctx, "cruser/crrepo", issue.Number, service.UpdateIssueInput{State: &open})
	if err != nil {
		t.Fatalf("reopen issue: %v", err)
	}
	if updated.State != "open" {
		t.Errorf("after reopen: state got %s, want open", updated.State)
	}
	if updated.ClosedAt != nil {
		t.Error("after reopen: ClosedAt should be nil")
	}
	if updated.StateReason != db.StateReasonReopened {
		t.Errorf("after reopen: state_reason got %s, want %s", updated.StateReason, db.StateReasonReopened)
	}
}

func TestUpdateIssueSetsClosedBy(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	setupRepoForTest(t, svc, "cbuser", "cbrepo")
	var user db.User
	if err := svc.DB.First(&user, "login = ?", "cbuser").Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	ctx := service.ContextWithUser(context.Background(), user)

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "cbuser/cbrepo",
		Title:        "ClosedBy test",
		AuthorLogin:  user.Login,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	closed := db.StateClosed
	updated, err := svc.UpdateIssue(ctx, "cbuser/cbrepo", issue.Number, service.UpdateIssueInput{State: &closed})
	if err != nil {
		t.Fatalf("close issue: %v", err)
	}
	if updated.ClosedByLogin != user.Login {
		t.Errorf("closed_by_login got %q, want %q", updated.ClosedByLogin, user.Login)
	}

	open := db.StateOpen
	updated, err = svc.UpdateIssue(ctx, "cbuser/cbrepo", issue.Number, service.UpdateIssueInput{State: &open})
	if err != nil {
		t.Fatalf("reopen issue: %v", err)
	}
	if updated.ClosedByLogin != "" {
		t.Errorf("closed_by_login after reopen got %q, want empty", updated.ClosedByLogin)
	}
}

func TestUpdateIssueByID(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "byiduser", "byidrepo")

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "byiduser/byidrepo",
		Title:        "ByID Test",
		AuthorLogin:  "byiduser",
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	reload := func() db.Issue {
		t.Helper()
		var iss db.Issue
		if err := svc.DB.First(&iss, issue.ID).Error; err != nil {
			t.Fatalf("reload issue: %v", err)
		}
		return iss
	}

	// Close with default reason
	closed := "closed"
	if err := svc.UpdateIssueByID(ctx, issue.ID, &closed, nil); err != nil {
		t.Fatalf("close: %v", err)
	}
	iss := reload()
	if iss.State != "closed" {
		t.Errorf("close: state got %s, want closed", iss.State)
	}
	if iss.ClosedAt == nil {
		t.Error("close: ClosedAt should not be nil")
	}
	if iss.StateReason != db.StateReasonCompleted {
		t.Errorf("close: state_reason got %s, want %s", iss.StateReason, db.StateReasonCompleted)
	}

	// Reopen
	openState := "open"
	if err := svc.UpdateIssueByID(ctx, issue.ID, &openState, nil); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	iss = reload()
	if iss.State != "open" {
		t.Errorf("reopen: state got %s, want open", iss.State)
	}
	if iss.ClosedAt != nil {
		t.Error("reopen: ClosedAt should be nil")
	}
	if iss.StateReason != db.StateReasonReopened {
		t.Errorf("reopen: state_reason got %s, want %s", iss.StateReason, db.StateReasonReopened)
	}

	// Close with NOT_PLANNED reason
	notPlanned := db.StateReasonNotPlanned
	if err := svc.UpdateIssueByID(ctx, issue.ID, &closed, &notPlanned); err != nil {
		t.Fatalf("close NOT_PLANNED: %v", err)
	}
	iss = reload()
	if iss.StateReason != db.StateReasonNotPlanned {
		t.Errorf("NOT_PLANNED: state_reason got %s, want %s", iss.StateReason, db.StateReasonNotPlanned)
	}
}

func TestCreateIssueWithLabels(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "ciluser", "cilrepo")

	// Create repo-level labels first
	if _, err := svc.CreateLabel(ctx, "ciluser/cilrepo", "cil-bug", "ff0000", ""); err != nil {
		t.Fatalf("create label: %v", err)
	}
	if _, err := svc.CreateLabel(ctx, "ciluser/cilrepo", "cil-feat", "00ff00", ""); err != nil {
		t.Fatalf("create label: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "ciluser/cilrepo",
		Title:        "Issue With Labels",
		AuthorLogin:  "ciluser",
		Labels:       []string{"cil-bug", "cil-feat"},
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	if len(issue.Labels) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(issue.Labels))
	}
	if issue.Repository.Owner.ID == 0 {
		t.Fatal("expected create issue fast path to populate repository owner")
	}
	if issue.Repository.Owner.Login != "ciluser" {
		t.Fatalf("repository owner login: got %q want %q", issue.Repository.Owner.Login, "ciluser")
	}

	names := make(map[string]bool)
	for _, l := range issue.Labels {
		names[l.Name] = true
	}
	if !names["cil-bug"] || !names["cil-feat"] {
		t.Errorf("expected labels [cil-bug, cil-feat], got %v", issue.Labels)
	}
}

func TestAddRemoveIssueAssignees(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "asguser", "asgrepo")

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "asguser/asgrepo",
		Title:        "Assignee Test",
		AuthorLogin:  "asguser",
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	// Add assignees
	updated, err := svc.AddIssueAssignees(ctx, "asguser/asgrepo", issue.Number, []string{"alice_dev", "bob_eng"})
	if err != nil {
		t.Fatalf("add assignees: %v", err)
	}
	if !strings.Contains(updated.AssigneeLogins, "alice_dev") || !strings.Contains(updated.AssigneeLogins, "bob_eng") {
		t.Errorf("add: expected both assignees, got %q", updated.AssigneeLogins)
	}

	// Remove alice_dev
	updated, err = svc.RemoveIssueAssignees(ctx, "asguser/asgrepo", issue.Number, []string{"alice_dev"})
	if err != nil {
		t.Fatalf("remove assignees: %v", err)
	}
	if strings.Contains(updated.AssigneeLogins, "alice_dev") {
		t.Errorf("remove: alice_dev should be gone, got %q", updated.AssigneeLogins)
	}
	if !strings.Contains(updated.AssigneeLogins, "bob_eng") {
		t.Errorf("remove: bob_eng should remain, got %q", updated.AssigneeLogins)
	}
}

func TestListIssuesByState(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "lsuser", "lsrepo")

	if _, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "lsuser/lsrepo", Title: "Open Issue", AuthorLogin: "lsuser",
	}); err != nil {
		t.Fatalf("create open issue: %v", err)
	}
	iss2, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "lsuser/lsrepo", Title: "Closed Issue", AuthorLogin: "lsuser",
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	closed := "closed"
	if _, err := svc.UpdateIssue(ctx, "lsuser/lsrepo", iss2.Number, service.UpdateIssueInput{State: &closed}); err != nil {
		t.Fatalf("close issue: %v", err)
	}

	tests := []struct {
		state      string
		want       int
		wantTitles []string
	}{
		{"open", 1, []string{"Open Issue"}},
		{"closed", 1, []string{"Closed Issue"}},
		{"all", 2, []string{"Open Issue", "Closed Issue"}},
	}
	for _, tt := range tests {
		t.Run("state="+tt.state, func(t *testing.T) {
			got, err := svc.ListIssues(ctx, "lsuser/lsrepo", tt.state, "", "", "", "", "")
			if err != nil {
				t.Fatalf("ListIssues: %v", err)
			}
			if len(got) != tt.want {
				t.Errorf("state=%s: got %d issues, want %d", tt.state, len(got), tt.want)
			}
			gotTitles := make(map[string]bool)
			for _, iss := range got {
				gotTitles[iss.Title] = true
			}
			for _, title := range tt.wantTitles {
				if !gotTitles[title] {
					t.Errorf("state=%s: expected issue %q in results", tt.state, title)
				}
			}
		})
	}
}

func TestListIssuesByLabels(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "lluser", "llrepo")

	bugLabel, err := svc.CreateLabel(ctx, "lluser/llrepo", "ll-bug", "ff0000", "")
	if err != nil {
		t.Fatalf("create bug label: %v", err)
	}
	featLabel, err := svc.CreateLabel(ctx, "lluser/llrepo", "ll-feat", "00ff00", "")
	if err != nil {
		t.Fatalf("create feat label: %v", err)
	}

	iss1, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "lluser/llrepo", Title: "Both Labels", AuthorLogin: "lluser",
	})
	iss2, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "lluser/llrepo", Title: "Bug Only", AuthorLogin: "lluser",
	})
	// iss3 has no labels
	svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "lluser/llrepo", Title: "No Labels", AuthorLogin: "lluser",
	})

	// Direct SQL insert (SQLite-compatible, no INSERT IGNORE)
	for _, pair := range [][2]uint{{iss1.ID, bugLabel.ID}, {iss1.ID, featLabel.ID}, {iss2.ID, bugLabel.ID}} {
		if err := svc.DB.Exec("INSERT INTO issue_labels (issue_id, label_id) VALUES (?, ?)", pair[0], pair[1]).Error; err != nil {
			t.Fatalf("insert issue_labels: %v", err)
		}
	}

	tests := []struct {
		labels     string
		want       int
		wantTitles []string
	}{
		{"ll-bug", 2, []string{"Both Labels", "Bug Only"}},
		{"LL-BUG", 2, []string{"Both Labels", "Bug Only"}},
		{"ll-feat", 1, []string{"Both Labels"}},
		{"ll-bug,ll-feat", 1, []string{"Both Labels"}}, // AND semantics
		{"missing-label", 0, nil},
	}
	for _, tt := range tests {
		t.Run("labels="+tt.labels, func(t *testing.T) {
			got, err := svc.ListIssues(ctx, "lluser/llrepo", "all", tt.labels, "", "", "", "")
			if err != nil {
				t.Fatalf("ListIssues: %v", err)
			}
			if len(got) != tt.want {
				t.Errorf("labels=%s: got %d, want %d", tt.labels, len(got), tt.want)
			}
			gotTitles := make(map[string]bool)
			for _, iss := range got {
				gotTitles[iss.Title] = true
			}
			for _, title := range tt.wantTitles {
				if !gotTitles[title] {
					t.Errorf("labels=%s: expected issue %q in results", tt.labels, title)
				}
			}
		})
	}
}

func TestListIssuesFiltered(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.Create(&db.User{Login: "fltauthor", Name: "fltauthor", Type: db.TypeUser})
	svc.DB.Create(&db.User{Login: "fltother", Name: "fltother", Type: db.TypeUser})
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "fltauthor", Name: "fltrepo"}); err != nil {
		t.Fatalf("create repo: %v", err)
	}

	iss1, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "fltauthor/fltrepo", Title: "By Author", Body: "cc @fltother", AuthorLogin: "fltauthor",
	})
	if err != nil {
		t.Fatalf("create iss1: %v", err)
	}
	iss2, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "fltauthor/fltrepo", Title: "By Other", AuthorLogin: "fltother",
	})
	if err != nil {
		t.Fatalf("create iss2: %v", err)
	}

	// Assign iss1 to fltother
	if _, err := svc.AddIssueAssignees(ctx, "fltauthor/fltrepo", iss1.Number, []string{"fltother"}); err != nil {
		t.Fatalf("add assignee: %v", err)
	}

	// fltauthor mentions fltother in a comment on iss2.
	if _, err := svc.CreateIssueComment(ctx, "fltauthor/fltrepo", iss2.Number, "noted @fltother", "fltauthor", nil); err != nil {
		t.Fatalf("create comment: %v", err)
	}

	// Add label to iss1
	lbl, err := svc.CreateLabel(ctx, "fltauthor/fltrepo", "flt-priority", "ff0000", "")
	if err != nil {
		t.Fatalf("create label: %v", err)
	}
	svc.DB.Exec("INSERT INTO issue_labels (issue_id, label_id) VALUES (?, ?)", iss1.ID, lbl.ID)

	tests := []struct {
		name      string
		assignee  string
		mentioned string
		createdBy string
		labels    string
		wantIDs   []uint
	}{
		{"assignee", "fltother", "", "", "", []uint{iss1.ID}},
		{"createdBy", "", "", "fltauthor", "", []uint{iss1.ID}},
		{"mentioned", "", "fltother", "", "", []uint{iss1.ID, iss2.ID}},
		{"labels", "", "", "", "flt-priority", []uint{iss1.ID}},
		{"labels case-insensitive", "", "", "", "FLT-PRIORITY", []uint{iss1.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.ListIssuesFiltered(ctx, service.IssueListFilter{
				RepoFullName: "fltauthor/fltrepo",
				State:        "all",
				Assignee:     tt.assignee,
				Mentioned:    tt.mentioned,
				CreatedBy:    tt.createdBy,
				Labels:       tt.labels,
			})
			if err != nil {
				t.Fatalf("ListIssuesFiltered: %v", err)
			}
			gotIDs := make(map[uint]bool)
			for _, iss := range got {
				gotIDs[iss.ID] = true
			}
			for _, wid := range tt.wantIDs {
				if !gotIDs[wid] {
					t.Errorf("expected issue ID %d in results, got IDs %v", wid, gotIDs)
				}
			}
			if len(got) != len(tt.wantIDs) {
				t.Errorf("got %d issues, want %d", len(got), len(tt.wantIDs))
			}
		})
	}
}

func TestFindPRsClosingIssue(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "closinguser", "closingrepo")

	// Create an issue
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "closinguser/closingrepo",
		Title:        "Issue to be closed",
		AuthorLogin:  "closinguser",
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	issueNum := fmt.Sprintf("%d", issue.Number)

	// Get repo for direct DB inserts
	repo, err := svc.GetRepo(ctx, "closinguser/closingrepo")
	if err != nil {
		t.Fatalf("get repo: %v", err)
	}

	// Create PRs with different closing reference formats by direct DB insert
	prs := []struct {
		title string
		body  string
	}{
		{"Fix with local ref", "This PR fixes #" + issueNum},
		{"Fix with cross-repo ref", "This PR fixes closinguser/closingrepo#" + issueNum},
		{"Fix with URL ref", "This PR closes https://github.com/closinguser/closingrepo/issues/" + issueNum},
		{"No match", "This PR fixes #999"},
	}

	for i, pr := range prs {
		svc.DB.Create(&db.PullRequest{
			RepositoryID: repo.ID,
			Number:       i + 1,
			Title:        pr.title,
			Body:         db.LargeText(pr.body),
			HeadRef:      "branch-" + issueNum,
			BaseRef:      "main",
			State:        "open",
			AuthorID:     1, // closinguser
		})
	}

	// Test FindPRsClosingIssue
	foundPRs, err := svc.FindPRsClosingIssue(ctx, repo.ID, int(issue.Number))
	if err != nil {
		t.Fatalf("FindPRsClosingIssue: %v", err)
	}

	// Should find 3 PRs (local, cross-repo, URL)
	if len(foundPRs) != 3 {
		t.Errorf("FindPRsClosingIssue: got %d PRs, want 3", len(foundPRs))
		for _, pr := range foundPRs {
			t.Logf("  Found PR: %s", pr.Title)
		}
	}

	// Verify the matching PRs
	foundTitles := make(map[string]bool)
	for _, pr := range foundPRs {
		foundTitles[pr.Title] = true
	}

	if !foundTitles["Fix with local ref"] {
		t.Error("Expected to find PR with local ref (#N)")
	}
	if !foundTitles["Fix with cross-repo ref"] {
		t.Error("Expected to find PR with cross-repo ref (owner/repo#N)")
	}
	if !foundTitles["Fix with URL ref"] {
		t.Error("Expected to find PR with URL ref (https://.../issues/N)")
	}

	// Verify no-match PR is not included
	if foundTitles["No match"] {
		t.Error("PR with different issue number should not be included")
	}
}

func TestListPRsFiltered_MentionedIncludesPRReviewBodies(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "mentionreview", "repo")
	repo, err := svc.GetRepo(ctx, "mentionreview/repo")
	if err != nil {
		t.Fatalf("get repo: %v", err)
	}
	var author db.User
	if err := svc.DB.Where("login = ?", "mentionreview").First(&author).Error; err != nil {
		t.Fatalf("get author: %v", err)
	}
	pr := db.PullRequest{
		RepositoryID:     repo.ID,
		HeadRepositoryID: repo.ID,
		Number:           1,
		Title:            "plain title",
		Body:             "plain body",
		State:            "open",
		AuthorID:         author.ID,
		HeadRef:          "feature",
		BaseRef:          repo.DefaultBranch,
	}
	if pr.BaseRef == "" {
		pr.BaseRef = "main"
	}
	if err := svc.DB.Create(&pr).Error; err != nil {
		t.Fatalf("create PR row: %v", err)
	}
	if err := svc.DB.Create(&db.PullRequestReview{
		PullRequestID: pr.ID,
		AuthorLogin:   "mentionreview",
		State:         "COMMENTED",
		Body:          "please check with @fltother",
	}).Error; err != nil {
		t.Fatalf("create review summary: %v", err)
	}

	got, err := svc.ListPRsFiltered(ctx, service.PRListFilter{
		RepoFullName: repo.FullName,
		State:        "all",
		Mentioned:    "fltother",
	})
	if err != nil {
		t.Fatalf("ListPRsFiltered: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if got[0].Number != pr.Number {
		t.Fatalf("expected PR #%d, got #%d", pr.Number, got[0].Number)
	}
}
