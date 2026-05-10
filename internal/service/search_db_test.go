package service_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

var errFakeEmbed = errors.New("fake embed error")

func TestSearchIssues_PRvsIssueFilter(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.Create(&db.User{Login: "user1"})
	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "user1", Name: "repo1", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("failed to setup repo: %v", err)
	}

	// Create 1 Issue
	iss1, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "user1/repo1",
		Title:        "Normal Issue",
		Body:         "body",
		AuthorLogin:  "user1",
	})
	if err != nil {
		t.Fatalf("create issue err: %v", err)
	}

	// Create 1 PR (which also creates an issue behind the scenes because is_pr = true)
	pr1, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "user1/repo1",
		Title:        "Normal PR",
		Body:         "body",
		HeadRef:      "feat",
		BaseRef:      "main",
		AuthorLogin:  "user1",
	})
	if err != nil {
		t.Fatalf("create pr err: %v", err)
	}

	// Create a MERGED PR
	pr2, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "user1/repo1",
		Title:        "Merged PR",
		Body:         "body",
		HeadRef:      "feat2",
		BaseRef:      "main",
		AuthorLogin:  "user1",
	})
	// Manually set pr2 as merged in the database instead of doing a full Git merge
	if err := svc.DB.Model(&db.PullRequest{}).Where("id = ?", pr2.ID).Updates(map[string]any{
		"merged": true,
		"state":  "closed",
	}).Error; err != nil {
		t.Fatalf("update pr2 err: %v", err)
	}

	var pr1Issue, pr2Issue db.Issue
	svc.DB.First(&pr1Issue, "repository_id = ? AND number = ?", pr1.RepositoryID, pr1.Number)
	svc.DB.First(&pr2Issue, "repository_id = ? AND number = ?", pr2.RepositoryID, pr2.Number)

	// Add team review request to pr1
	svc.DB.Create(&db.ReviewRequest{
		PullRequestID: pr1.ID,
		TeamSlug:      "test-team",
	})
	// Add user review request to pr1
	svc.DB.Create(&db.ReviewRequest{
		PullRequestID: pr1.ID,
		Login:         "reviewer1",
	})

	tests := []struct {
		name    string
		query   string
		wantIDs []uint // issue.ID or pr.ID (they share the same repository-level number, but we check number for simplicity, wait, issue ID is unique)
		isPR    bool   // whether it returns []db.PullRequest instead of []db.Issue
	}{
		{
			name:    "all (no filters)",
			query:   "repo:user1/repo1",
			wantIDs: []uint{iss1.ID},
			isPR:    false,
		},
		{
			name:    "is:issue",
			query:   "repo:user1/repo1 is:issue",
			wantIDs: []uint{iss1.ID},
			isPR:    false,
		},
		{
			name:    "is:pr",
			query:   "repo:user1/repo1 is:pr",
			wantIDs: []uint{pr1.ID, pr2.ID},
			isPR:    true,
		},
		{
			name:    "is:pr is:merged",
			query:   "repo:user1/repo1 is:pr is:merged",
			wantIDs: []uint{pr2.ID},
			isPR:    true,
		},
		{
			name:    "is:pr is:unmerged",
			query:   "repo:user1/repo1 is:pr is:unmerged",
			wantIDs: []uint{pr1.ID},
			isPR:    true,
		},
		{
			name:    "team review request",
			query:   "repo:user1/repo1 is:pr team-review-requested:test-team",
			wantIDs: []uint{pr1.ID},
			isPR:    true,
		},
		{
			name:    "user review request",
			query:   "repo:user1/repo1 is:pr user-review-requested:reviewer1",
			wantIDs: []uint{pr1.ID},
			isPR:    true,
		},
		{
			name:    "conflicting state and is:closed",
			query:   "repo:user1/repo1 is:pr state:open is:closed",
			wantIDs: []uint{},
			isPR:    true,
		},
		{
			name:    "conflicting state and is:merged",
			query:   "repo:user1/repo1 is:pr state:open is:merged",
			wantIDs: []uint{},
			isPR:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotIDs []uint
			if tt.isPR {
				prs, err := svc.SearchPRs(ctx, tt.query)
				if err != nil {
					t.Fatalf("SearchPRs err: %v", err)
				}
				for _, p := range prs {
					gotIDs = append(gotIDs, p.ID)
				}
			} else {
				issues, err := svc.SearchIssues(ctx, tt.query)
				if err != nil {
					t.Fatalf("SearchIssues err: %v", err)
				}
				for _, iss := range issues {
					gotIDs = append(gotIDs, iss.ID)
				}
			}

			sort.Slice(gotIDs, func(i, j int) bool { return gotIDs[i] < gotIDs[j] })
			sort.Slice(tt.wantIDs, func(i, j int) bool { return tt.wantIDs[i] < tt.wantIDs[j] })

			if len(gotIDs) != len(tt.wantIDs) {
				t.Errorf("got %v, want %v", gotIDs, tt.wantIDs)
			} else {
				for i := range gotIDs {
					if gotIDs[i] != tt.wantIDs[i] {
						t.Errorf("got %v, want %v", gotIDs, tt.wantIDs)
						break
					}
				}
			}
		})
	}
}

// assertIssueIDs is a helper for search test assertions.
func assertIssueIDs(t *testing.T, got []db.Issue, wantIDs []uint) {
	t.Helper()
	gotIDs := make([]uint, len(got))
	for i, iss := range got {
		gotIDs[i] = iss.ID
	}
	sort.Slice(gotIDs, func(i, j int) bool { return gotIDs[i] < gotIDs[j] })
	sort.Slice(wantIDs, func(i, j int) bool { return wantIDs[i] < wantIDs[j] })
	if len(gotIDs) != len(wantIDs) {
		t.Errorf("got IDs %v, want %v", gotIDs, wantIDs)
		return
	}
	for i := range gotIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Errorf("got IDs %v, want %v", gotIDs, wantIDs)
			return
		}
	}
}

// assertPRIDs is a helper for PR search test assertions.
func assertPRIDs(t *testing.T, got []db.PullRequest, wantIDs []uint) {
	t.Helper()
	gotIDs := make([]uint, len(got))
	for i, pr := range got {
		gotIDs[i] = pr.ID
	}
	sort.Slice(gotIDs, func(i, j int) bool { return gotIDs[i] < gotIDs[j] })
	sort.Slice(wantIDs, func(i, j int) bool { return wantIDs[i] < wantIDs[j] })
	if len(gotIDs) != len(wantIDs) {
		t.Errorf("got IDs %v, want %v", gotIDs, wantIDs)
		return
	}
	for i := range gotIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Errorf("got IDs %v, want %v", gotIDs, wantIDs)
			return
		}
	}
}

func TestSearchIssues_StateFilter(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "sfuser", "sfrepo")

	iss1, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "sfuser/sfrepo", Title: "Open One", AuthorLogin: "sfuser",
	})
	iss2, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "sfuser/sfrepo", Title: "Closed One", AuthorLogin: "sfuser",
	})
	closed := "closed"
	svc.UpdateIssue(ctx, "sfuser/sfrepo", iss2.Number, service.UpdateIssueInput{State: &closed})

	tests := []struct {
		name    string
		query   string
		wantIDs []uint
	}{
		{"state:open", "repo:sfuser/sfrepo state:open", []uint{iss1.ID}},
		{"state:closed", "repo:sfuser/sfrepo state:closed", []uint{iss2.ID}},
		{"no state filter", "repo:sfuser/sfrepo", []uint{iss1.ID, iss2.ID}},
		{"conflicting state and is (state first)", "repo:sfuser/sfrepo state:open is:closed", []uint{}},
		{"conflicting state and is (is first)", "repo:sfuser/sfrepo is:closed state:open", []uint{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.SearchIssues(ctx, tt.query)
			if err != nil {
				t.Fatalf("SearchIssues: %v", err)
			}
			assertIssueIDs(t, got, tt.wantIDs)
		})
	}
}

func TestSearchIssues_ByTitle(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "titleuser", "titlerepo")

	// Create an issue with a unique title
	title := "MA Search Issue TestUnique123"
	iss1, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "titleuser/titlerepo", Title: title, Body: "test body", AuthorLogin: "titleuser",
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Create another issue with a different title
	_, err = svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "titleuser/titlerepo", Title: "Different Title", Body: "test body", AuthorLogin: "titleuser",
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	tests := []struct {
		name    string
		query   string
		wantIDs []uint
	}{
		{
			name:    "search by full title with repo",
			query:   title + " repo:titleuser/titlerepo",
			wantIDs: []uint{iss1.ID},
		},
		{
			name:    "search by partial title with repo",
			query:   "MA Search Issue repo:titleuser/titlerepo",
			wantIDs: []uint{iss1.ID},
		},
		{
			name:    "search by unique substring with repo",
			query:   "TestUnique123 repo:titleuser/titlerepo",
			wantIDs: []uint{iss1.ID},
		},
		{
			name:    "search without repo filter should still find issue",
			query:   title,
			wantIDs: []uint{iss1.ID},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.SearchIssues(ctx, tt.query)
			if err != nil {
				t.Fatalf("SearchIssues: %v", err)
			}
			assertIssueIDs(t, got, tt.wantIDs)
		})
	}
}

func TestSearchIssues_AuthorFilter(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.Create(&db.User{Login: "afalice", Name: "afalice", Type: db.TypeUser})
	svc.DB.Create(&db.User{Login: "afbob", Name: "afbob", Type: db.TypeUser})
	svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "afalice", Name: "afrepo"})

	issA, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "afalice/afrepo", Title: "By Alice", AuthorLogin: "afalice",
	})
	issB, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "afalice/afrepo", Title: "By Bob", AuthorLogin: "afbob",
	})

	tests := []struct {
		name    string
		query   string
		wantIDs []uint
	}{
		{"author:afalice", "repo:afalice/afrepo author:afalice", []uint{issA.ID}},
		{"author:afbob", "repo:afalice/afrepo author:afbob", []uint{issB.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.SearchIssues(ctx, tt.query)
			if err != nil {
				t.Fatalf("SearchIssues: %v", err)
			}
			assertIssueIDs(t, got, tt.wantIDs)
		})
	}
}

// TestSearchIssues_NewlyCreatedIssue verifies that searching for a newly created issue works correctly.
// This is a regression test for issue #205.
func TestSearchIssues_NewlyCreatedIssue(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Create a non-anonymous user and repo
	svc.DB.Create(&db.User{Login: "newuser", Name: "newuser", Type: db.TypeUser, IsAnonymous: false})
	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "newuser", Name: "newrepo"})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	// Create an issue with a unique title
	title := "MA Search Issue Test205"
	iss, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "newuser/newrepo", Title: title, Body: "test body", AuthorLogin: "newuser",
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Immediately search for the issue (simulating the gh CLI behavior)
	query := title + " repo:newuser/newrepo"
	got, err := svc.SearchIssues(ctx, query)
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}

	if len(got) != 1 {
		t.Errorf("Search returned %d results, want 1. Query: %q", len(got), query)
	} else if got[0].ID != iss.ID {
		t.Errorf("Search returned issue ID %d, want %d", got[0].ID, iss.ID)
	}
}

func TestSearchIssues_AssigneeFilter(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "asfowner", "asfrepo")

	iss1, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "asfowner/asfrepo", Title: "Assigned", AuthorLogin: "asfowner",
	})
	iss2, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "asfowner/asfrepo", Title: "Unassigned", AuthorLogin: "asfowner",
	})
	svc.AddIssueAssignees(ctx, "asfowner/asfrepo", iss1.Number, []string{"asftester"})

	got, err := svc.SearchIssues(ctx, "repo:asfowner/asfrepo assignee:asftester")
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	assertIssueIDs(t, got, []uint{iss1.ID})

	// Verify unassigned doesn't match
	if len(got) != 1 {
		t.Errorf("expected 1 result, got %d", len(got))
	}
	_ = iss2
}

func TestSearchIssues_LabelFilter(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "slfowner", "slfrepo")

	bugLbl, _ := svc.CreateLabel(ctx, "slfowner/slfrepo", "slf-bug", "ff0000", "")
	featLbl, _ := svc.CreateLabel(ctx, "slfowner/slfrepo", "slf-feat", "00ff00", "")
	wontfixLbl, _ := svc.CreateLabel(ctx, "slfowner/slfrepo", "slf-wontfix", "888888", "")

	iss1, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "slfowner/slfrepo", Title: "Bug+Feat", AuthorLogin: "slfowner",
	})
	iss2, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "slfowner/slfrepo", Title: "Bug+Wontfix", AuthorLogin: "slfowner",
	})
	iss3, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "slfowner/slfrepo", Title: "No Labels", AuthorLogin: "slfowner",
	})

	// Add labels via direct SQL
	for _, pair := range [][2]uint{
		{iss1.ID, bugLbl.ID}, {iss1.ID, featLbl.ID},
		{iss2.ID, bugLbl.ID}, {iss2.ID, wontfixLbl.ID},
	} {
		svc.DB.Exec("INSERT INTO issue_labels (issue_id, label_id) VALUES (?, ?)", pair[0], pair[1])
	}

	tests := []struct {
		name    string
		query   string
		wantIDs []uint
	}{
		{"label:slf-bug", "repo:slfowner/slfrepo label:slf-bug", []uint{iss1.ID, iss2.ID}},
		{"label:slf-bug label:slf-feat (AND)", "repo:slfowner/slfrepo label:slf-bug label:slf-feat", []uint{iss1.ID}},
		{"-label:slf-wontfix", "repo:slfowner/slfrepo -label:slf-wontfix", []uint{iss1.ID, iss3.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.SearchIssues(ctx, tt.query)
			if err != nil {
				t.Fatalf("SearchIssues: %v", err)
			}
			assertIssueIDs(t, got, tt.wantIDs)
		})
	}
}

func TestSearchIssues_MetadataFilters(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "mfowner", "mfrepo")

	lbl, _ := svc.CreateLabel(ctx, "mfowner/mfrepo", "mf-tag", "aabbcc", "")

	issLabeled, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "mfowner/mfrepo", Title: "Labeled", AuthorLogin: "mfowner",
	})
	issNoLabel, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "mfowner/mfrepo", Title: "No Label", AuthorLogin: "mfowner",
	})
	issAssigned, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "mfowner/mfrepo", Title: "Assigned", AuthorLogin: "mfowner",
	})
	issMilestoned, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "mfowner/mfrepo", Title: "Milestoned", AuthorLogin: "mfowner",
	})

	svc.DB.Exec("INSERT INTO issue_labels (issue_id, label_id) VALUES (?, ?)", issLabeled.ID, lbl.ID)
	svc.AddIssueAssignees(ctx, "mfowner/mfrepo", issAssigned.Number, []string{"mftester"})

	// Create a milestone and assign it to one issue so no:milestone is discriminating
	ms, err := svc.CreateMilestone(ctx, "mfowner/mfrepo", "v1.0", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}
	if err := svc.SetIssueMilestone(ctx, issMilestoned.ID, &ms.ID); err != nil {
		t.Fatalf("SetIssueMilestone: %v", err)
	}

	tests := []struct {
		name    string
		query   string
		wantIDs []uint
	}{
		{"no:label", "repo:mfowner/mfrepo no:label", []uint{issNoLabel.ID, issAssigned.ID, issMilestoned.ID}},
		{"has:label", "repo:mfowner/mfrepo has:label", []uint{issLabeled.ID}},
		{"no:assignee", "repo:mfowner/mfrepo no:assignee", []uint{issLabeled.ID, issNoLabel.ID, issMilestoned.ID}},
		{"has:assignee", "repo:mfowner/mfrepo has:assignee", []uint{issAssigned.ID}},
		{"no:milestone excludes milestoned issue", "repo:mfowner/mfrepo no:milestone", []uint{issLabeled.ID, issNoLabel.ID, issAssigned.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.SearchIssues(ctx, tt.query)
			if err != nil {
				t.Fatalf("SearchIssues: %v", err)
			}
			assertIssueIDs(t, got, tt.wantIDs)
		})
	}
}

func TestSearchIssues_ReasonFilter(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "rfowner", "rfrepo")

	issCompleted, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "rfowner/rfrepo", Title: "Completed", AuthorLogin: "rfowner",
	})
	issNotPlanned, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "rfowner/rfrepo", Title: "Not Planned", AuthorLogin: "rfowner",
	})

	// Close with COMPLETED (default)
	closed := "closed"
	svc.UpdateIssue(ctx, "rfowner/rfrepo", issCompleted.Number, service.UpdateIssueInput{State: &closed})

	// Close with NOT_PLANNED via UpdateIssueByID
	notPlanned := db.StateReasonNotPlanned
	svc.UpdateIssueByID(ctx, issNotPlanned.ID, &closed, &notPlanned)

	tests := []struct {
		name    string
		query   string
		wantIDs []uint
	}{
		{"reason:completed", "repo:rfowner/rfrepo reason:completed", []uint{issCompleted.ID}},
		{"reason:NOT_PLANNED", "repo:rfowner/rfrepo reason:NOT_PLANNED", []uint{issNotPlanned.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.SearchIssues(ctx, tt.query)
			if err != nil {
				t.Fatalf("SearchIssues: %v", err)
			}
			assertIssueIDs(t, got, tt.wantIDs)
		})
	}
}

func TestSearchIssues_InvolvesFilter(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.Create(&db.User{Login: "invauthor", Name: "invauthor", Type: db.TypeUser})
	svc.DB.Create(&db.User{Login: "invhelper", Name: "invhelper", Type: db.TypeUser})
	svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "invauthor", Name: "invrepo"})

	// iss1: authored by invhelper
	iss1, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "invauthor/invrepo", Title: "By Helper", AuthorLogin: "invhelper",
	})
	// iss2: invhelper comments
	iss2, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "invauthor/invrepo", Title: "Commented By Helper", AuthorLogin: "invauthor",
	})
	svc.CreateIssueComment(ctx, "invauthor/invrepo", iss2.Number, "helping out", "invhelper", nil)

	// iss3: no involvement from invhelper
	svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "invauthor/invrepo", Title: "No Involvement", AuthorLogin: "invauthor",
	})

	got, err := svc.SearchIssues(ctx, "repo:invauthor/invrepo involves:invhelper")
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	assertIssueIDs(t, got, []uint{iss1.ID, iss2.ID})
}

func TestSearchIssues_CommenterFilter(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.Create(&db.User{Login: "cmtauthor", Name: "cmtauthor", Type: db.TypeUser})
	svc.DB.Create(&db.User{Login: "cmtcommenter", Name: "cmtcommenter", Type: db.TypeUser})
	svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "cmtauthor", Name: "cmtrepo"})

	// iss1: cmtcommenter comments on this issue
	iss1, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "cmtauthor/cmtrepo", Title: "Issue With Comment", AuthorLogin: "cmtauthor",
	})
	svc.CreateIssueComment(ctx, "cmtauthor/cmtrepo", iss1.Number, "great issue", "cmtcommenter", nil)

	// iss2: cmtcommenter also comments here
	iss2, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "cmtauthor/cmtrepo", Title: "Another Commented Issue", AuthorLogin: "cmtauthor",
	})
	svc.CreateIssueComment(ctx, "cmtauthor/cmtrepo", iss2.Number, "nice", "cmtcommenter", nil)

	// iss3: no comment from cmtcommenter
	iss3, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "cmtauthor/cmtrepo", Title: "No Comment From User", AuthorLogin: "cmtauthor",
	})
	svc.CreateIssueComment(ctx, "cmtauthor/cmtrepo", iss3.Number, "other comment", "cmtauthor", nil)

	// Search with commenter filter - should return iss1 and iss2
	got, err := svc.SearchIssues(ctx, "repo:cmtauthor/cmtrepo commenter:cmtcommenter")
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	assertIssueIDs(t, got, []uint{iss1.ID, iss2.ID})

	// Search with non-existent commenter - should return empty
	got, err = svc.SearchIssues(ctx, "repo:cmtauthor/cmtrepo commenter:nosuchuser123")
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 results for non-existent commenter, got %d", len(got))
	}
}

func TestSearchPRs_CommenterFilter(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.Create(&db.User{Login: "prcmtauthor", Name: "prcmtauthor", Type: db.TypeUser})
	svc.DB.Create(&db.User{Login: "prcmtcommenter", Name: "prcmtcommenter", Type: db.TypeUser})
	svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "prcmtauthor", Name: "prcmtrepo", DefaultBranch: "main"})

	// pr1: prcmtcommenter comments on this PR
	pr1, _ := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "prcmtauthor/prcmtrepo", Title: "PR With Comment", HeadRef: "feat1", BaseRef: "main", AuthorLogin: "prcmtauthor",
	})
	svc.CreateIssueComment(ctx, "prcmtauthor/prcmtrepo", pr1.Number, "great pr", "prcmtcommenter", nil)

	// pr2: prcmtcommenter also comments here
	pr2, _ := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "prcmtauthor/prcmtrepo", Title: "Another Commented PR", HeadRef: "feat2", BaseRef: "main", AuthorLogin: "prcmtauthor",
	})
	svc.CreateIssueComment(ctx, "prcmtauthor/prcmtrepo", pr2.Number, "nice work", "prcmtcommenter", nil)

	// pr3: no comment from prcmtcommenter
	pr3, _ := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "prcmtauthor/prcmtrepo", Title: "No Comment From User", HeadRef: "feat3", BaseRef: "main", AuthorLogin: "prcmtauthor",
	})
	svc.CreateIssueComment(ctx, "prcmtauthor/prcmtrepo", pr3.Number, "other comment", "prcmtauthor", nil)

	// Search with commenter filter - should return pr1 and pr2
	got, err := svc.SearchPRs(ctx, "repo:prcmtauthor/prcmtrepo commenter:prcmtcommenter")
	if err != nil {
		t.Fatalf("SearchPRs: %v", err)
	}
	assertPRIDs(t, got, []uint{pr1.ID, pr2.ID})

	// Search with non-existent commenter - should return empty
	got, err = svc.SearchPRs(ctx, "repo:prcmtauthor/prcmtrepo commenter:nosuchuser123")
	if err != nil {
		t.Fatalf("SearchPRs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 results for non-existent commenter, got %d", len(got))
	}
}

func TestSearchPRs_AuthorAssigneeLabel(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	if err := svc.DB.Create(&db.User{Login: "palalice", Name: "palalice", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user palalice: %v", err)
	}
	if err := svc.DB.Create(&db.User{Login: "palbob", Name: "palbob", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user palbob: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "palalice", Name: "palrepo", DefaultBranch: "main"}); err != nil {
		t.Fatalf("create repo: %v", err)
	}

	var repo db.Repository
	if err := svc.DB.First(&repo, "full_name = ?", "palalice/palrepo").Error; err != nil {
		t.Fatalf("load repo: %v", err)
	}
	var bob db.User
	if err := svc.DB.First(&bob, "login = ?", "palbob").Error; err != nil {
		t.Fatalf("load user palbob: %v", err)
	}
	if err := svc.DB.Create(&db.Collaborator{
		RepositoryID: repo.ID,
		UserID:       bob.ID,
		Permission:   "write",
	}).Error; err != nil {
		t.Fatalf("create collaborator: %v", err)
	}

	lbl, err := svc.CreateLabel(ctx, "palalice/palrepo", "pal-urgent", "ff0000", "")
	if err != nil {
		t.Fatalf("create label: %v", err)
	}

	pr1, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "palalice/palrepo", Title: "Alice PR", HeadRef: "feat1", BaseRef: "main", AuthorLogin: "palalice",
	})
	if err != nil {
		t.Fatalf("create PR1: %v", err)
	}
	pr2, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "palalice/palrepo", Title: "Bob PR", HeadRef: "feat2", BaseRef: "main", AuthorLogin: "palbob",
	})
	if err != nil {
		t.Fatalf("create PR2: %v", err)
	}

	// Assign pr1 to palbob
	svc.DB.Model(&db.PullRequest{}).Where("id = ?", pr1.ID).Update("assignee_logins", "palbob")
	// Add label to pr2
	svc.DB.Exec("INSERT INTO pr_labels (pull_request_id, label_id) VALUES (?, ?)", pr2.ID, lbl.ID)

	tests := []struct {
		name    string
		query   string
		wantIDs []uint
	}{
		{"author:palalice", "repo:palalice/palrepo is:pr author:palalice", []uint{pr1.ID}},
		{"assignee:palbob", "repo:palalice/palrepo is:pr assignee:palbob", []uint{pr1.ID}},
		{"label:pal-urgent", "repo:palalice/palrepo is:pr label:pal-urgent", []uint{pr2.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.SearchPRs(ctx, tt.query)
			if err != nil {
				t.Fatalf("SearchPRs: %v", err)
			}
			assertPRIDs(t, got, tt.wantIDs)
		})
	}
}

func TestSearchPRs_ReviewFilters(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.Create(&db.User{Login: "rvowner", Name: "rvowner", Type: db.TypeUser})
	svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "rvowner", Name: "rvrepo", DefaultBranch: "main"})

	prApproved, _ := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "rvowner/rvrepo", Title: "Approved PR", HeadRef: "feat1", BaseRef: "main", AuthorLogin: "rvowner",
	})
	prChanges, _ := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "rvowner/rvrepo", Title: "Changes PR", HeadRef: "feat2", BaseRef: "main", AuthorLogin: "rvowner",
	})
	prNoReview, _ := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "rvowner/rvrepo", Title: "No Review PR", HeadRef: "feat3", BaseRef: "main", AuthorLogin: "rvowner",
	})

	// Add reviews
	svc.DB.Create(&db.PullRequestReview{PullRequestID: prApproved.ID, AuthorLogin: "rvreviewer", State: db.ReviewApproved})
	svc.DB.Create(&db.PullRequestReview{PullRequestID: prChanges.ID, AuthorLogin: "rvreviewer", State: db.ReviewChangesRequested})

	tests := []struct {
		name    string
		query   string
		wantIDs []uint
	}{
		{"review:approved", "repo:rvowner/rvrepo is:pr review:approved", []uint{prApproved.ID}},
		{"review:changes_requested", "repo:rvowner/rvrepo is:pr review:changes_requested", []uint{prChanges.ID}},
		{"review:none", "repo:rvowner/rvrepo is:pr review:none", []uint{prNoReview.ID}},
		{"reviewed-by:rvreviewer", "repo:rvowner/rvrepo is:pr reviewed-by:rvreviewer", []uint{prApproved.ID, prChanges.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.SearchPRs(ctx, tt.query)
			if err != nil {
				t.Fatalf("SearchPRs: %v", err)
			}
			assertPRIDs(t, got, tt.wantIDs)
		})
	}
	_ = prNoReview
}

// TestSearchPRs_ReviewFilters_RESTEvents tests that reviews created via REST API
// events (APPROVE, REQUEST_CHANGES) are correctly normalized and can be found
// by search filters (review:approved, review:changes_requested).
// Regression test for issue #542.
func TestSearchPRs_ReviewFilters_RESTEvents(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.Create(&db.User{Login: "restowner", Name: "restowner", Type: db.TypeUser})
	svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "restowner", Name: "restrepo", DefaultBranch: "main"})

	prApprove, _ := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "restowner/restrepo", Title: "APPROVE Event PR", HeadRef: "feat1", BaseRef: "main", AuthorLogin: "restowner",
	})
	prRequestChanges, _ := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "restowner/restrepo", Title: "REQUEST_CHANGES Event PR", HeadRef: "feat2", BaseRef: "main", AuthorLogin: "restowner",
	})
	prComment, _ := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "restowner/restrepo", Title: "COMMENT Event PR", HeadRef: "feat3", BaseRef: "main", AuthorLogin: "restowner",
	})

	// Simulate REST API events - these use verb forms (APPROVE, REQUEST_CHANGES, COMMENT)
	// The service layer should normalize them to database states (APPROVED, CHANGES_REQUESTED, COMMENTED)
	_, _ = svc.AddPRReview(ctx, prApprove.ID, "reviewer", "APPROVE", "Looks good", "abc123")
	_, _ = svc.AddPRReview(ctx, prRequestChanges.ID, "reviewer", "REQUEST_CHANGES", "Needs work", "def456")
	_, _ = svc.AddPRReview(ctx, prComment.ID, "reviewer", "COMMENT", "Just a comment", "ghi789")

	tests := []struct {
		name    string
		query   string
		wantIDs []uint
	}{
		{"review:approved finds APPROVE event", "repo:restowner/restrepo is:pr review:approved", []uint{prApprove.ID}},
		{"review:changes_requested finds REQUEST_CHANGES event", "repo:restowner/restrepo is:pr review:changes_requested", []uint{prRequestChanges.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.SearchPRs(ctx, tt.query)
			if err != nil {
				t.Fatalf("SearchPRs: %v", err)
			}
			assertPRIDs(t, got, tt.wantIDs)
		})
	}
}

func TestSearchPRs_NegationFilters(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.Create(&db.User{Login: "neghuman", Name: "neghuman", Type: db.TypeUser})
	svc.DB.Create(&db.User{Login: "negbot", Name: "negbot", Type: db.TypeUser})
	svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "neghuman", Name: "negrepo", DefaultBranch: "main"})

	wontfixLbl, _ := svc.CreateLabel(ctx, "neghuman/negrepo", "neg-wontfix", "888888", "")

	prHuman, _ := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "neghuman/negrepo", Title: "Human PR", HeadRef: "feat1", BaseRef: "main", AuthorLogin: "neghuman",
	})
	prBot, _ := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "neghuman/negrepo", Title: "Bot PR", HeadRef: "feat2", BaseRef: "main", AuthorLogin: "negbot",
	})

	// Assign prBot to negbot, label prBot as wontfix
	svc.DB.Model(&db.PullRequest{}).Where("id = ?", prBot.ID).Update("assignee_logins", "negbot")
	svc.DB.Exec("INSERT INTO pr_labels (pull_request_id, label_id) VALUES (?, ?)", prBot.ID, wontfixLbl.ID)

	tests := []struct {
		name    string
		query   string
		wantIDs []uint
	}{
		{"-author:negbot", "repo:neghuman/negrepo is:pr -author:negbot", []uint{prHuman.ID}},
		{"-label:neg-wontfix", "repo:neghuman/negrepo is:pr -label:neg-wontfix", []uint{prHuman.ID}},
		{"-assignee:negbot", "repo:neghuman/negrepo is:pr -assignee:negbot", []uint{prHuman.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.SearchPRs(ctx, tt.query)
			if err != nil {
				t.Fatalf("SearchPRs: %v", err)
			}
			assertPRIDs(t, got, tt.wantIDs)
		})
	}
}

func TestSearchPRs_DraftFilter(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.Create(&db.User{Login: "dfowner", Name: "dfowner", Type: db.TypeUser})
	svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "dfowner", Name: "dfrepo", DefaultBranch: "main"})

	prDraft, _ := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "dfowner/dfrepo", Title: "Draft PR", HeadRef: "draft1", BaseRef: "main", AuthorLogin: "dfowner", Draft: true,
	})
	prReady, _ := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "dfowner/dfrepo", Title: "Ready PR", HeadRef: "ready1", BaseRef: "main", AuthorLogin: "dfowner", Draft: false,
	})

	tests := []struct {
		name    string
		query   string
		wantIDs []uint
	}{
		{"is:draft", "repo:dfowner/dfrepo is:pr is:draft", []uint{prDraft.ID}},
		{"draft:false", "repo:dfowner/dfrepo is:pr draft:false", []uint{prReady.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.SearchPRs(ctx, tt.query)
			if err != nil {
				t.Fatalf("SearchPRs: %v", err)
			}
			assertPRIDs(t, got, tt.wantIDs)
		})
	}
}

func TestSearchPRs_MetadataFilters(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.Create(&db.User{Login: "prmfowner", Name: "prmfowner", Type: db.TypeUser})
	svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "prmfowner", Name: "prmfrepo", DefaultBranch: "main"})

	lbl, err := svc.CreateLabel(ctx, "prmfowner/prmfrepo", "prmf-tag", "aabbcc", "")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}

	prLabeled, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "prmfowner/prmfrepo", Title: "Labeled PR", HeadRef: "feat1", BaseRef: "main", AuthorLogin: "prmfowner",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	prNoLabel, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "prmfowner/prmfrepo", Title: "No Label PR", HeadRef: "feat2", BaseRef: "main", AuthorLogin: "prmfowner",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	prAssigned, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "prmfowner/prmfrepo", Title: "Assigned PR", HeadRef: "feat3", BaseRef: "main", AuthorLogin: "prmfowner",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	prMilestoned, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "prmfowner/prmfrepo", Title: "Milestoned PR", HeadRef: "feat4", BaseRef: "main", AuthorLogin: "prmfowner",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	// Add label to prLabeled
	svc.DB.Exec("INSERT INTO pr_labels (pull_request_id, label_id) VALUES (?, ?)", prLabeled.ID, lbl.ID)

	// Assign prAssigned
	svc.DB.Model(&db.PullRequest{}).Where("id = ?", prAssigned.ID).Update("assignee_logins", "prmfowner")

	// Set milestone on prMilestoned
	ms, err := svc.CreateMilestone(ctx, "prmfowner/prmfrepo", "v1.0", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}
	if err := svc.SetPRMilestone(ctx, prMilestoned.ID, &ms.ID); err != nil {
		t.Fatalf("SetPRMilestone: %v", err)
	}

	tests := []struct {
		name    string
		query   string
		wantIDs []uint
	}{
		{"no:label", "repo:prmfowner/prmfrepo is:pr no:label", []uint{prNoLabel.ID, prAssigned.ID, prMilestoned.ID}},
		{"has:label", "repo:prmfowner/prmfrepo is:pr has:label", []uint{prLabeled.ID}},
		{"no:assignee", "repo:prmfowner/prmfrepo is:pr no:assignee", []uint{prLabeled.ID, prNoLabel.ID, prMilestoned.ID}},
		{"has:assignee", "repo:prmfowner/prmfrepo is:pr has:assignee", []uint{prAssigned.ID}},
		{"no:milestone excludes milestoned PR", "repo:prmfowner/prmfrepo is:pr no:milestone", []uint{prLabeled.ID, prNoLabel.ID, prAssigned.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.SearchPRs(ctx, tt.query)
			if err != nil {
				t.Fatalf("SearchPRs: %v", err)
			}
			assertPRIDs(t, got, tt.wantIDs)
		})
	}
}

func TestSearchIssues_InQualifier(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "inowner", "inrepo")

	// Create an issue where title and body have different keywords
	iss1, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "inowner/inrepo",
		Title:        "MA Search Issue UniqueTitle123",
		Body:         "This body contains DifferentKeyword that is not in the title",
		AuthorLogin:  "inowner",
	})

	// Create another issue with the keyword in body but not title
	iss2, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "inowner/inrepo",
		Title:        "Another Issue",
		Body:         "This body contains DifferentKeyword for testing",
		AuthorLogin:  "inowner",
	})

	tests := []struct {
		name    string
		query   string
		wantIDs []uint
	}{
		{
			name:    "in:title finds issue with keyword in title",
			query:   "repo:inowner/inrepo in:title UniqueTitle123",
			wantIDs: []uint{iss1.ID},
		},
		{
			name:    "in:title does NOT find issue with keyword only in body",
			query:   "repo:inowner/inrepo in:title DifferentKeyword",
			wantIDs: []uint{},
		},
		{
			name:    "in:body finds issue with keyword in body",
			query:   "repo:inowner/inrepo in:body DifferentKeyword",
			wantIDs: []uint{iss1.ID, iss2.ID},
		},
		{
			name:    "no in: qualifier searches both title and body",
			query:   "repo:inowner/inrepo DifferentKeyword",
			wantIDs: []uint{iss1.ID, iss2.ID},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.SearchIssues(ctx, tt.query)
			if err != nil {
				t.Fatalf("SearchIssues: %v", err)
			}
			assertIssueIDs(t, got, tt.wantIDs)
		})
	}
}

// TestSearchIssues_ExplicitRepoQualifierBypass tests that the discoverability
// filter is bypassed when an explicit repo: qualifier is provided.
// This allows searching any repo when the repo is explicitly specified.
func TestSearchIssues_ExplicitRepoQualifierBypass(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Create two regular users and repos
	setupRepoForTest(t, svc, "erpbuser1", "erprepo1")
	setupRepoForTest(t, svc, "erpbuser2", "erprepo2")

	// Create issues in both repos
	iss1, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "erpbuser1/erprepo1", Title: "Repo1 Issue", Body: "test", AuthorLogin: "erpbuser1",
	})
	iss2, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "erpbuser2/erprepo2", Title: "Repo2 Issue", Body: "test", AuthorLogin: "erpbuser2",
	})

	// Create a context with user1
	user1Ctx := service.ContextWithUser(ctx, db.User{Login: "erpbuser1", Type: db.TypeUser})

	// Without repo: qualifier, user should see issues from discoverable repos (both in this case)
	got, err := svc.SearchIssues(user1Ctx, "Issue")
	if err != nil {
		t.Fatalf("SearchIssues without repo: %v", err)
	}
	// Should find both issues since both users are non-anonymous
	if len(got) != 2 {
		t.Errorf("Without repo: qualifier, expected 2 issues, got %d", len(got))
	}

	// With explicit repo: qualifier for repo2, should find only repo2's issue
	got, err = svc.SearchIssues(user1Ctx, "Issue repo:erpbuser2/erprepo2")
	if err != nil {
		t.Fatalf("SearchIssues with repo: %v", err)
	}
	if len(got) != 1 || got[0].ID != iss2.ID {
		t.Errorf("With repo:erpbuser2/erprepo2, expected repo2's issue, got %d results", len(got))
	}

	// With explicit repo: qualifier for repo1, should find only repo1's issue
	got, err = svc.SearchIssues(user1Ctx, "Issue repo:erpbuser1/erprepo1")
	if err != nil {
		t.Fatalf("SearchIssues with repo: %v", err)
	}
	if len(got) != 1 || got[0].ID != iss1.ID {
		t.Errorf("With repo:erpbuser1/erprepo1, expected repo1's issue, got %d results", len(got))
	}
}

// TestSearchPRs_ExplicitRepoQualifierBypass tests that the discoverability
// filter is bypassed for PRs when an explicit repo: qualifier is provided.
func TestSearchPRs_ExplicitRepoQualifierBypass(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Create two regular users and repos
	setupRepoForTest(t, svc, "prpbuser1", "prprepo1")
	setupRepoForTest(t, svc, "prpbuser2", "prprepo2")

	// Create PRs in both repos
	pr1, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "prpbuser1/prprepo1", Title: "Repo1 PR", Body: "test", AuthorLogin: "prpbuser1", HeadRef: "feature1", BaseRef: "main",
	})
	if err != nil {
		t.Fatalf("CreatePR repo1: %v", err)
	}
	pr2, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "prpbuser2/prprepo2", Title: "Repo2 PR", Body: "test", AuthorLogin: "prpbuser2", HeadRef: "feature2", BaseRef: "main",
	})
	if err != nil {
		t.Fatalf("CreatePR repo2: %v", err)
	}

	// Create a context with user1
	user1Ctx := service.ContextWithUser(ctx, db.User{Login: "prpbuser1", Type: db.TypeUser})

	// With explicit repo: qualifier for repo2, should find repo2's PR (bypassing discoverability filter)
	got, err := svc.SearchPRs(user1Ctx, "PR repo:prpbuser2/prprepo2")
	if err != nil {
		t.Fatalf("SearchPRs with repo: %v", err)
	}
	if len(got) != 1 || got[0].ID != pr2.ID {
		t.Errorf("With repo:prpbuser2/prprepo2, expected repo2's PR, got %d results: %v", len(got), got)
	}

	// With explicit repo: qualifier for repo1, should find repo1's PR
	got, err = svc.SearchPRs(user1Ctx, "PR repo:prpbuser1/prprepo1")
	if err != nil {
		t.Fatalf("SearchPRs with repo: %v", err)
	}
	if len(got) != 1 || got[0].ID != pr1.ID {
		t.Errorf("With repo:prpbuser1/prprepo1, expected repo1's PR, got %d results", len(got))
	}
}

// -----------------------------------------------------------------------------
// Vector Search Path Integration Tests (Issue #234)
// -----------------------------------------------------------------------------

// FakeEmbedder is a test embedder that returns deterministic vectors.
type FakeEmbedder struct {
	Vec      []float32
	Err      error
	Called   int
	LastText string
}

func (f *FakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	f.Called++
	f.LastText = text
	return f.Vec, f.Err
}

func (f *FakeEmbedder) Dimensions() int { return len(f.Vec) }

// TestSearchIssues_VectorSearchMergePath tests the full vector search merge path.
func TestSearchIssues_VectorSearchMergePath(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user and repo
	svc.DB.Create(&db.User{Login: "vecuser", Type: db.TypeUser})
	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "vecuser", Name: "vecrepo", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("failed to setup repo: %v", err)
	}

	// Create issues with embeddings pre-set in DB
	iss1, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "vecuser/vecrepo",
		Title:        "vector search test issue one",
		Body:         "body one",
		AuthorLogin:  "vecuser",
	})
	if err != nil {
		t.Fatalf("create issue 1 err: %v", err)
	}

	iss2, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "vecuser/vecrepo",
		Title:        "vector search test issue two",
		Body:         "body two",
		AuthorLogin:  "vecuser",
	})
	if err != nil {
		t.Fatalf("create issue 2 err: %v", err)
	}

	_, err = svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "vecuser/vecrepo",
		Title:        "completely different topic",
		Body:         "body three",
		AuthorLogin:  "vecuser",
	})
	if err != nil {
		t.Fatalf("create issue 3 err: %v", err)
	}

	// Pre-set embeddings in DB (as TEXT column)
	// Note: In real TiDB, this would be a VECTOR column, but SQLite stores as TEXT
	vec1 := "[0.1,0.2,0.3]"
	vec2 := "[0.1,0.2,0.3]" // Same vector as iss1 to simulate similarity
	svc.DB.Model(&db.Issue{}).Where("id = ?", iss1.ID).Update("embedding", vec1)
	svc.DB.Model(&db.Issue{}).Where("id = ?", iss2.ID).Update("embedding", vec2)
	// iss3 has no embedding (NULL)

	// Wire FakeEmbedder into Service
	fakeEmbedder := &FakeEmbedder{Vec: []float32{0.1, 0.2, 0.3}}
	svc.Embedder = fakeEmbedder

	// SearchIssues should return merged LIKE + vector results
	// The query "vector search" should match iss1 and iss2 via LIKE
	// Vector search would also return iss1 and iss2 (same vector)
	// Dedup should ensure no duplicates
	got, err := svc.SearchIssues(ctx, "repo:vecuser/vecrepo vector search")
	if err != nil {
		t.Fatalf("SearchIssues err: %v", err)
	}

	// Should get at least iss1 and iss2 (iss3 doesn't match "vector search")
	if len(got) < 2 {
		t.Errorf("Expected at least 2 issues, got %d: %v", len(got), got)
	}
	seen := make(map[uint]struct{}, len(got))
	for _, issue := range got {
		if _, ok := seen[issue.ID]; ok {
			t.Fatalf("expected deduplicated results, found duplicate issue %d in %#v", issue.ID, got)
		}
		seen[issue.ID] = struct{}{}
	}

	// Verify FakeEmbedder was called (vector path was exercised)
	if fakeEmbedder.Called == 0 {
		t.Error("Expected FakeEmbedder to be called (vector path should be exercised)")
	}
}

// TestSearchIssues_VectorSearchFallback tests graceful fallback when vector query fails.
func TestSearchIssues_VectorSearchFallback(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user and repo
	svc.DB.Create(&db.User{Login: "fbuser", Type: db.TypeUser})
	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "fbuser", Name: "fbrepo", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("failed to setup repo: %v", err)
	}

	// Create an issue
	iss1, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "fbuser/fbrepo",
		Title:        "fallback test issue",
		Body:         "body",
		AuthorLogin:  "fbuser",
	})
	if err != nil {
		t.Fatalf("create issue err: %v", err)
	}

	// Wire FakeEmbedder that returns error
	fakeEmbedder := &FakeEmbedder{Vec: nil, Err: errFakeEmbed}
	svc.Embedder = fakeEmbedder

	// SearchIssues should gracefully fallback to LIKE-only when vector fails
	got, err := svc.SearchIssues(ctx, "repo:fbuser/fbrepo fallback")
	if err != nil {
		t.Fatalf("SearchIssues err: %v", err)
	}

	// Should still get iss1 via LIKE fallback
	if len(got) != 1 || got[0].ID != iss1.ID {
		t.Errorf("Expected 1 issue (iss1) via LIKE fallback, got %d: %v", len(got), got)
	}

	// Verify FakeEmbedder was called (attempted vector path)
	if fakeEmbedder.Called == 0 {
		t.Error("Expected FakeEmbedder to be called (vector path should be attempted)")
	}
}

func TestSearchIssues_TokenAwareLexicalRecall(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.Create(&db.User{Login: "tokuser", Type: db.TypeUser})
	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "tokuser", Name: "tokrepo", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("failed to setup repo: %v", err)
	}

	iss, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "tokuser/tokrepo",
		Title:        "Redis-backed rate limiting",
		Body:         "Lua scripts keep distributed limits atomic.",
		AuthorLogin:  "tokuser",
	})
	if err != nil {
		t.Fatalf("create issue err: %v", err)
	}

	got, err := svc.SearchIssues(ctx, "repo:tokuser/tokrepo redis limiting")
	if err != nil {
		t.Fatalf("SearchIssues err: %v", err)
	}
	if len(got) == 0 || got[0].ID != iss.ID {
		t.Fatalf("expected token-aware lexical recall to find issue %d first, got %+v", iss.ID, got)
	}
}

func TestSearchIssues_MultilingualLexicalRecall(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.Create(&db.User{Login: "mluser", Type: db.TypeUser})
	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "mluser", Name: "mlrepo", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("failed to setup repo: %v", err)
	}

	english, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "mluser/mlrepo",
		Title:        "English lexical search",
		Body:         "Standard tokenizer coverage",
		AuthorLogin:  "mluser",
	})
	if err != nil {
		t.Fatalf("create english issue err: %v", err)
	}

	chinese, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "mluser/mlrepo",
		Title:        "多语言搜索支持",
		Body:         "TiDB 全文检索",
		AuthorLogin:  "mluser",
	})
	if err != nil {
		t.Fatalf("create chinese issue err: %v", err)
	}

	japanese, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "mluser/mlrepo",
		Title:        "日本語タイトル",
		Body:         "こんにちは 検索 体験",
		AuthorLogin:  "mluser",
	})
	if err != nil {
		t.Fatalf("create japanese issue err: %v", err)
	}

	korean, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "mluser/mlrepo",
		Title:        "한국어 검색",
		Body:         "안녕하세요 벡터 인덱스",
		AuthorLogin:  "mluser",
	})
	if err != nil {
		t.Fatalf("create korean issue err: %v", err)
	}

	mixed, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "mluser/mlrepo",
		Title:        "Mixed 搜索 search",
		Body:         "こんにちは hybrid recall",
		AuthorLogin:  "mluser",
	})
	if err != nil {
		t.Fatalf("create mixed-language issue err: %v", err)
	}

	tests := []struct {
		name    string
		query   string
		wantIDs []uint
	}{
		{name: "english term", query: "repo:mluser/mlrepo lexical", wantIDs: []uint{english.ID}},
		{name: "chinese term", query: "repo:mluser/mlrepo 多语言", wantIDs: []uint{chinese.ID}},
		{name: "japanese body term", query: "repo:mluser/mlrepo in:body こんにちは", wantIDs: []uint{japanese.ID, mixed.ID}},
		{name: "korean term", query: "repo:mluser/mlrepo 한국어", wantIDs: []uint{korean.ID}},
		{name: "mixed language query", query: "repo:mluser/mlrepo 搜索 search", wantIDs: []uint{mixed.ID}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.SearchIssues(ctx, tt.query)
			if err != nil {
				t.Fatalf("SearchIssues err: %v", err)
			}
			assertIssueIDs(t, got, tt.wantIDs)
		})
	}
}

// TestSearchPRs_ByNumber tests searching for PRs by #number query.
func TestSearchPRs_ByNumber(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user and repo
	svc.DB.Create(&db.User{Login: "numuser", Type: db.TypeUser})
	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "numuser", Name: "numrepo", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("failed to setup repo: %v", err)
	}

	// Create multiple PRs
	_, err = svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "numuser/numrepo",
		Title:        "PR Number One",
		Body:         "body 1",
		HeadRef:      "feat1",
		BaseRef:      "main",
		AuthorLogin:  "numuser",
	})
	if err != nil {
		t.Fatalf("create pr1 err: %v", err)
	}

	_, err = svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "numuser/numrepo",
		Title:        "PR Number Two",
		Body:         "body 2",
		HeadRef:      "feat2",
		BaseRef:      "main",
		AuthorLogin:  "numuser",
	})
	if err != nil {
		t.Fatalf("create pr2 err: %v", err)
	}

	_, err = svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "numuser/numrepo",
		Title:        "PR Number Three",
		Body:         "body 3",
		HeadRef:      "feat3",
		BaseRef:      "main",
		AuthorLogin:  "numuser",
	})
	if err != nil {
		t.Fatalf("create pr3 err: %v", err)
	}

	// Test searching by PR number using #<number> syntax
	tests := []struct {
		name       string
		query      string
		wantNumber int
		wantFound  bool
	}{
		{
			name:       "find PR #1",
			query:      "#1 repo:numuser/numrepo",
			wantNumber: 1,
			wantFound:  true,
		},
		{
			name:       "find PR #2",
			query:      "#2 repo:numuser/numrepo",
			wantNumber: 2,
			wantFound:  true,
		},
		{
			name:       "find PR #3",
			query:      "#3 repo:numuser/numrepo",
			wantNumber: 3,
			wantFound:  true,
		},
		{
			name:      "non-existent PR #999",
			query:     "#999 repo:numuser/numrepo",
			wantFound: false,
		},
		{
			name:       "find PR with state filter",
			query:      "#1 repo:numuser/numrepo state:open",
			wantNumber: 1,
			wantFound:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.SearchPRs(ctx, tt.query)
			if err != nil {
				t.Fatalf("SearchPRs err: %v", err)
			}

			if tt.wantFound {
				if len(got) != 1 {
					t.Errorf("Expected 1 PR, got %d: %v", len(got), got)
					return
				}
				if got[0].Number != tt.wantNumber {
					t.Errorf("Expected PR number %d, got %d", tt.wantNumber, got[0].Number)
				}
			} else {
				if len(got) != 0 {
					t.Errorf("Expected 0 PRs, got %d: %v", len(got), got)
				}
			}
		})
	}
}

// TestSearchIssues_ByNumber tests searching for issues by #number query.
func TestSearchIssues_ByNumber(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user and repo
	svc.DB.Create(&db.User{Login: "issuser", Type: db.TypeUser})
	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "issuser", Name: "issrepo", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("failed to setup repo: %v", err)
	}

	// Create multiple issues
	_, err = svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "issuser/issrepo",
		Title:        "Issue Number One",
		Body:         "body 1",
		AuthorLogin:  "issuser",
	})
	if err != nil {
		t.Fatalf("create iss1 err: %v", err)
	}

	_, err = svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "issuser/issrepo",
		Title:        "Issue Number Two",
		Body:         "body 2",
		AuthorLogin:  "issuser",
	})
	if err != nil {
		t.Fatalf("create iss2 err: %v", err)
	}

	_, err = svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "issuser/issrepo",
		Title:        "Issue Number Three",
		Body:         "body 3",
		AuthorLogin:  "issuser",
	})
	if err != nil {
		t.Fatalf("create iss3 err: %v", err)
	}

	// Test searching by issue number using #<number> syntax
	tests := []struct {
		name       string
		query      string
		wantNumber int
		wantFound  bool
	}{
		{
			name:       "find issue #1",
			query:      "#1 repo:issuser/issrepo",
			wantNumber: 1,
			wantFound:  true,
		},
		{
			name:       "find issue #2",
			query:      "#2 repo:issuser/issrepo",
			wantNumber: 2,
			wantFound:  true,
		},
		{
			name:       "find issue #3",
			query:      "#3 repo:issuser/issrepo",
			wantNumber: 3,
			wantFound:  true,
		},
		{
			name:      "non-existent issue #999",
			query:     "#999 repo:issuser/issrepo",
			wantFound: false,
		},
		{
			name:       "find issue with state filter",
			query:      "#1 repo:issuser/issrepo state:open",
			wantNumber: 1,
			wantFound:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.SearchIssues(ctx, tt.query)
			if err != nil {
				t.Fatalf("SearchIssues err: %v", err)
			}

			if tt.wantFound {
				if len(got) != 1 {
					t.Errorf("Expected 1 issue, got %d: %v", len(got), got)
					return
				}
				if got[0].Number != tt.wantNumber {
					t.Errorf("Expected issue number %d, got %d", tt.wantNumber, got[0].Number)
				}
			} else {
				if len(got) != 0 {
					t.Errorf("Expected 0 issues, got %d: %v", len(got), got)
				}
			}
		})
	}
}

// TestSearchPRs_ByCommitMessage tests searching for PRs by commit message text.
func TestSearchPRs_ByCommitMessage(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "cmuser", "cmrepo")

	// Create a PR
	pr1, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "cmuser/cmrepo",
		Title:        "Feature PR",
		Body:         "PR body",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  "cmuser",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	// Create another PR with different content
	pr2, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "cmuser/cmrepo",
		Title:        "Another PR",
		Body:         "Different body",
		HeadRef:      "feature2",
		BaseRef:      "main",
		AuthorLogin:  "cmuser",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	// Manually update commit_messages for pr1 (simulating what UpdatePRCommitData does)
	svc.DB.Model(&db.PullRequest{}).Where("id = ?", pr1.ID).Update("commit_messages", "fix bug in authentication module")
	svc.DB.Model(&db.PullRequest{}).Where("id = ?", pr2.ID).Update("commit_messages", "update documentation")

	tests := []struct {
		name    string
		query   string
		wantIDs []uint
	}{
		{
			name:    "search by commit message - authentication",
			query:   "authentication repo:cmuser/cmrepo is:pr",
			wantIDs: []uint{pr1.ID},
		},
		{
			name:    "search by commit message - bug",
			query:   "bug repo:cmuser/cmrepo is:pr",
			wantIDs: []uint{pr1.ID},
		},
		{
			name:    "search by commit message - documentation",
			query:   "documentation repo:cmuser/cmrepo is:pr",
			wantIDs: []uint{pr2.ID},
		},
		{
			name:    "search by commit message - no match",
			query:   "nonexistent repo:cmuser/cmrepo is:pr",
			wantIDs: []uint{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.SearchPRs(ctx, tt.query)
			if err != nil {
				t.Fatalf("SearchPRs: %v", err)
			}
			assertPRIDs(t, got, tt.wantIDs)
		})
	}
}

// TestSearchPRs_ByFilename tests searching for PRs by filename.
func TestSearchPRs_ByFilename(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "fnuser", "fnrepo")

	// Create PRs
	pr1, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "fnuser/fnrepo",
		Title:        "Files PR",
		Body:         "PR body",
		HeadRef:      "files",
		BaseRef:      "main",
		AuthorLogin:  "fnuser",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	pr2, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "fnuser/fnrepo",
		Title:        "Another Files PR",
		Body:         "Different body",
		HeadRef:      "files2",
		BaseRef:      "main",
		AuthorLogin:  "fnuser",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	// Manually update filenames for PRs (simulating what UpdatePRCommitData does)
	svc.DB.Model(&db.PullRequest{}).Where("id = ?", pr1.ID).Update("filenames", "src/auth.go,src/handler.go,tests/auth.go")
	svc.DB.Model(&db.PullRequest{}).Where("id = ?", pr2.ID).Update("filenames", "docs/README.md,docs/api.md")

	tests := []struct {
		name    string
		query   string
		wantIDs []uint
	}{
		{
			name:    "search by filename - auth.go",
			query:   "auth.go repo:fnuser/fnrepo is:pr",
			wantIDs: []uint{pr1.ID},
		},
		{
			name:    "search by filename - handler",
			query:   "handler repo:fnuser/fnrepo is:pr",
			wantIDs: []uint{pr1.ID},
		},
		{
			name:    "search by filename - README",
			query:   "README repo:fnuser/fnrepo is:pr",
			wantIDs: []uint{pr2.ID},
		},
		{
			name:    "search by filename - tests",
			query:   "tests repo:fnuser/fnrepo is:pr",
			wantIDs: []uint{pr1.ID},
		},
		{
			name:    "search by filename - no match",
			query:   "nonexistent.go repo:fnuser/fnrepo is:pr",
			wantIDs: []uint{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.SearchPRs(ctx, tt.query)
			if err != nil {
				t.Fatalf("SearchPRs: %v", err)
			}
			assertPRIDs(t, got, tt.wantIDs)
		})
	}
}

// TestSearchPRs_ByCommitMessageAndFilename tests searching for PRs by both commit message and filename.
func TestSearchPRs_ByCommitMessageAndFilename(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "cfuser", "cfrepo")

	// Create a PR
	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "cfuser/cfrepo",
		Title:        "Combined PR",
		Body:         "PR body",
		HeadRef:      "combined",
		BaseRef:      "main",
		AuthorLogin:  "cfuser",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	// Manually update both commit_messages and filenames
	svc.DB.Model(&db.PullRequest{}).Where("id = ?", pr.ID).Updates(map[string]any{
		"commit_messages": "add new authentication feature",
		"filenames":       "src/auth.go,src/middleware.go",
	})

	tests := []struct {
		name    string
		query   string
		wantIDs []uint
	}{
		{
			name:    "search matches commit message",
			query:   "authentication repo:cfuser/cfrepo is:pr",
			wantIDs: []uint{pr.ID},
		},
		{
			name:    "search matches filename",
			query:   "middleware repo:cfuser/cfrepo is:pr",
			wantIDs: []uint{pr.ID},
		},
		{
			name:    "search matches title",
			query:   "Combined repo:cfuser/cfrepo is:pr",
			wantIDs: []uint{pr.ID},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.SearchPRs(ctx, tt.query)
			if err != nil {
				t.Fatalf("SearchPRs: %v", err)
			}
			assertPRIDs(t, got, tt.wantIDs)
		})
	}
}

// TestSearchPRs_InQualifierWithCommitData tests that in: qualifier restricts search fields.
func TestSearchPRs_InQualifierWithCommitData(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "inuser", "inrepo")

	// Create a PR
	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "inuser/inrepo",
		Title:        "Title Only",
		Body:         "Body text",
		HeadRef:      "inbranch",
		BaseRef:      "main",
		AuthorLogin:  "inuser",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	// Update commit_messages and filenames but NOT title/body
	svc.DB.Model(&db.PullRequest{}).Where("id = ?", pr.ID).Updates(map[string]any{
		"commit_messages": "secret commit message",
		"filenames":       "secret_file.go",
	})

	tests := []struct {
		name    string
		query   string
		wantIDs []uint
	}{
		{
			name:    "default search includes commit messages and filenames",
			query:   "secret repo:inuser/inrepo is:pr",
			wantIDs: []uint{pr.ID},
		},
		{
			name:    "in:title excludes commit messages and filenames",
			query:   "secret in:title repo:inuser/inrepo is:pr",
			wantIDs: []uint{},
		},
		{
			name:    "in:title finds title match",
			query:   "Title in:title repo:inuser/inrepo is:pr",
			wantIDs: []uint{pr.ID},
		},
		{
			name:    "in:body finds body match",
			query:   "Body in:body repo:inuser/inrepo is:pr",
			wantIDs: []uint{pr.ID},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.SearchPRs(ctx, tt.query)
			if err != nil {
				t.Fatalf("SearchPRs: %v", err)
			}
			assertPRIDs(t, got, tt.wantIDs)
		})
	}
}

// TestUpdatePRCommitData tests that UpdatePRCommitData properly fetches and stores commit data.
func TestUpdatePRCommitData(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Use setupPRWithRealBranches to create a repo with real branches
	pr, authCtx, _ := setupPRWithRealBranches(t, svc, "upduser", "uprepo")
	fullName := "upduser/uprepo"

	// Add another commit on the feature branch
	if _, err := svc.Git.WriteFile(authCtx, fullName, "feature", "src/auth.go", "Add auth module", []byte("package auth\n")); err != nil {
		t.Fatalf("WriteFile on feature failed: %v", err)
	}

	// Update the HeadSHA to reflect the new commit
	if err := svc.PushHeadSHA(ctx, fullName, pr.Number); err != nil {
		t.Fatalf("PushHeadSHA: %v", err)
	}

	// Call UpdatePRCommitData
	err := svc.UpdatePRCommitData(ctx, fullName, pr.Number)
	if err != nil {
		t.Fatalf("UpdatePRCommitData: %v", err)
	}

	// Verify commit messages and filenames were updated
	var updatedPR db.PullRequest
	if err := svc.DB.First(&updatedPR, pr.ID).Error; err != nil {
		t.Fatalf("failed to get updated PR: %v", err)
	}

	if updatedPR.CommitMessages == "" {
		t.Error("expected CommitMessages to be populated")
	}
	if updatedPR.Filenames == "" {
		t.Error("expected Filenames to be populated")
	}

	// Verify the content contains expected text
	if !strings.Contains(strings.ToLower(updatedPR.CommitMessages), "auth") {
		t.Errorf("expected commit messages to contain 'auth', got: %q", updatedPR.CommitMessages)
	}
	if !strings.Contains(updatedPR.Filenames, "src/auth.go") {
		t.Errorf("expected filenames to contain 'src/auth.go', got: %q", updatedPR.Filenames)
	}
}
