package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

func TestCommentFlow(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "cmtuser", "cmtrepo")

	// Create issue to comment on
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "cmtuser/cmtrepo",
		Title:        "Issue for comments",
		AuthorLogin:  "cmtuser",
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Create comment via REST path
	c, err := svc.CreateIssueComment(ctx, "cmtuser/cmtrepo", issue.Number, "First comment", "cmtuser", nil)
	if err != nil {
		t.Fatalf("CreateIssueComment failed: %v", err)
	}
	if c.Body != "First comment" {
		t.Errorf("expected body 'First comment', got %s", c.Body)
	}

	// Create comment via GQL path (by issue DB ID)
	c2, err := svc.AddCommentByIssueID(ctx, issue.ID, "Second comment", "cmtuser")
	if err != nil {
		t.Fatalf("AddCommentByIssueID failed: %v", err)
	}
	if c2.Body != "Second comment" {
		t.Errorf("expected body 'Second comment', got %s", c2.Body)
	}

	// List comments
	comments, err := svc.ListIssueComments(ctx, "cmtuser/cmtrepo", issue.Number)
	if err != nil {
		t.Fatalf("ListIssueComments failed: %v", err)
	}
	if len(comments) != 2 {
		t.Errorf("expected 2 comments, got %d", len(comments))
	}

	// Get by ID
	got, err := svc.GetIssueCommentByID(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetIssueCommentByID failed: %v", err)
	}
	if got.Body != "First comment" {
		t.Errorf("expected 'First comment', got %s", got.Body)
	}

	// Update
	if err := svc.UpdateIssueComment(ctx, c.ID, "Updated comment"); err != nil {
		t.Fatalf("UpdateIssueComment failed: %v", err)
	}

	// Delete
	if err := svc.DeleteIssueComment(ctx, c.ID); err != nil {
		t.Fatalf("DeleteIssueComment failed: %v", err)
	}

	// Verify one comment remains
	remaining, _ := svc.ListIssueComments(ctx, "cmtuser/cmtrepo", issue.Number)
	if len(remaining) != 1 {
		t.Errorf("expected 1 comment after delete, got %d", len(remaining))
	}
}

func TestListIssueCommentsWithFilters(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "cmtfilteruser", "cmtfilterrepo")

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "cmtfilteruser/cmtfilterrepo",
		Title:        "Issue for comment filters",
		AuthorLogin:  "cmtfilteruser",
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	c1, err := svc.CreateIssueComment(ctx, "cmtfilteruser/cmtfilterrepo", issue.Number, "First comment", "cmtfilteruser", nil)
	if err != nil {
		t.Fatalf("CreateIssueComment 1 failed: %v", err)
	}
	c2, err := svc.CreateIssueComment(ctx, "cmtfilteruser/cmtfilterrepo", issue.Number, "Second comment", "cmtfilteruser", nil)
	if err != nil {
		t.Fatalf("CreateIssueComment 2 failed: %v", err)
	}

	base := time.Date(2024, 1, 2, 12, 0, 0, 0, time.UTC)
	c1Created := base.Add(0 * time.Minute)
	c2Created := base.Add(10 * time.Minute)
	c1Updated := base.Add(30 * time.Minute)
	c2Updated := base.Add(20 * time.Minute)

	if err := svc.DBForCtx(ctx).Model(&db.IssueComment{}).Where("id = ?", c1.ID).
		UpdateColumns(map[string]any{"created_at": c1Created, "updated_at": c1Updated}).Error; err != nil {
		t.Fatalf("update comment 1 timestamps: %v", err)
	}
	if err := svc.DBForCtx(ctx).Model(&db.IssueComment{}).Where("id = ?", c2.ID).
		UpdateColumns(map[string]any{"created_at": c2Created, "updated_at": c2Updated}).Error; err != nil {
		t.Fatalf("update comment 2 timestamps: %v", err)
	}

	createdAsc, err := svc.ListIssueCommentsWithFilters(ctx, "cmtfilteruser/cmtfilterrepo", issue.Number, "", "created", "asc")
	if err != nil {
		t.Fatalf("ListIssueCommentsWithFilters(created asc) failed: %v", err)
	}
	if len(createdAsc) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(createdAsc))
	}
	if createdAsc[0].ID != c1.ID || createdAsc[1].ID != c2.ID {
		t.Errorf("created asc order = [%d %d], want [%d %d]", createdAsc[0].ID, createdAsc[1].ID, c1.ID, c2.ID)
	}

	updatedAsc, err := svc.ListIssueCommentsWithFilters(ctx, "cmtfilteruser/cmtfilterrepo", issue.Number, "", "updated", "asc")
	if err != nil {
		t.Fatalf("ListIssueCommentsWithFilters(updated asc) failed: %v", err)
	}
	if len(updatedAsc) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(updatedAsc))
	}
	if updatedAsc[0].ID != c2.ID || updatedAsc[1].ID != c1.ID {
		t.Errorf("updated asc order = [%d %d], want [%d %d]", updatedAsc[0].ID, updatedAsc[1].ID, c2.ID, c1.ID)
	}

	since := base.Add(25 * time.Minute).Format(time.RFC3339Nano)
	sinceResults, err := svc.ListIssueCommentsWithFilters(ctx, "cmtfilteruser/cmtfilterrepo", issue.Number, since, "updated", "asc")
	if err != nil {
		t.Fatalf("ListIssueCommentsWithFilters(since) failed: %v", err)
	}
	if len(sinceResults) != 1 || sinceResults[0].ID != c1.ID {
		t.Fatalf("since filter expected comment %d only, got %#v", c1.ID, sinceResults)
	}
}

func TestPinIssueComment(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "pincommentuser", "pincommentrepo")

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "pincommentuser/pincommentrepo",
		Title:        "Issue for pinned comments",
		AuthorLogin:  "pincommentuser",
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	comment, err := svc.CreateIssueComment(ctx, "pincommentuser/pincommentrepo", issue.Number, "Important note", "pincommentuser", nil)
	if err != nil {
		t.Fatalf("CreateIssueComment failed: %v", err)
	}
	other, err := svc.CreateIssueComment(ctx, "pincommentuser/pincommentrepo", issue.Number, "Regular note", "pincommentuser", nil)
	if err != nil {
		t.Fatalf("CreateIssueComment other failed: %v", err)
	}

	if err := svc.PinIssueComment(ctx, comment.ID, true); err != nil {
		t.Fatalf("PinIssueComment(true) failed: %v", err)
	}

	pinned, err := svc.GetIssueCommentByID(ctx, comment.ID)
	if err != nil {
		t.Fatalf("GetIssueCommentByID pinned failed: %v", err)
	}
	if !pinned.IsPinned {
		t.Fatal("expected comment to be pinned")
	}
	if pinned.PinnedAt == nil {
		t.Fatal("expected pinned_at to be set")
	}

	pinnedComments, err := svc.GetPinnedComments(ctx, pinned.RepositoryID, issue.Number)
	if err != nil {
		t.Fatalf("GetPinnedComments failed: %v", err)
	}
	if len(pinnedComments) != 1 {
		t.Fatalf("expected 1 pinned comment, got %d", len(pinnedComments))
	}
	if pinnedComments[0].ID != comment.ID {
		t.Fatalf("expected pinned comment %d, got %d", comment.ID, pinnedComments[0].ID)
	}
	if pinnedComments[0].ID == other.ID {
		t.Fatalf("unexpected regular comment %d in pinned results", other.ID)
	}

	if err := svc.PinIssueComment(ctx, comment.ID, false); err != nil {
		t.Fatalf("PinIssueComment(false) failed: %v", err)
	}

	unpinned, err := svc.GetIssueCommentByID(ctx, comment.ID)
	if err != nil {
		t.Fatalf("GetIssueCommentByID unpinned failed: %v", err)
	}
	if unpinned.IsPinned {
		t.Fatal("expected comment to be unpinned")
	}
	if unpinned.PinnedAt != nil {
		t.Fatal("expected pinned_at to be cleared")
	}

	pinnedComments, err = svc.GetPinnedComments(ctx, unpinned.RepositoryID, issue.Number)
	if err != nil {
		t.Fatalf("GetPinnedComments after unpin failed: %v", err)
	}
	if len(pinnedComments) != 0 {
		t.Fatalf("expected 0 pinned comments after unpin, got %d", len(pinnedComments))
	}
}

func TestPinIssueComment_RequiresCommentAuthor(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "pincommentauthor", "pincommentrepo")
	writer := db.User{Login: "pincommentwriter", Name: "pincommentwriter", Type: db.TypeUser}
	if err := svc.DB.Create(&writer).Error; err != nil {
		t.Fatalf("create writer: %v", err)
	}

	repo, err := svc.GetRepo(ctx, "pincommentauthor/pincommentrepo")
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}
	if err := svc.AddCollaborator(ctx, repo.ID, writer.ID, "write"); err != nil {
		t.Fatalf("AddCollaborator failed: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "pincommentauthor/pincommentrepo",
		Title:        "Issue for pin auth",
		AuthorLogin:  "pincommentauthor",
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	comment, err := svc.CreateIssueComment(ctx, "pincommentauthor/pincommentrepo", issue.Number, "Author comment", "pincommentauthor", nil)
	if err != nil {
		t.Fatalf("CreateIssueComment failed: %v", err)
	}

	writerCtx := service.ContextWithUser(ctx, writer)
	if err := svc.PinIssueComment(writerCtx, comment.ID, true); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("PinIssueComment(non-author) error = %v, want ErrForbidden", err)
	}

	stored, err := svc.GetIssueCommentByID(ctx, comment.ID)
	if err != nil {
		t.Fatalf("GetIssueCommentByID after rejected pin failed: %v", err)
	}
	if stored.IsPinned {
		t.Fatal("expected comment to remain unpinned after rejected pin")
	}

	author, err := svc.GetUser(ctx, "pincommentauthor")
	if err != nil {
		t.Fatalf("GetUser author failed: %v", err)
	}
	authorCtx := service.ContextWithUser(ctx, author)
	if err := svc.PinIssueComment(authorCtx, comment.ID, true); err != nil {
		t.Fatalf("PinIssueComment(author) failed: %v", err)
	}

	if err := svc.PinIssueComment(writerCtx, comment.ID, false); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("PinIssueComment(non-author unpin) error = %v, want ErrForbidden", err)
	}

	stored, err = svc.GetIssueCommentByID(ctx, comment.ID)
	if err != nil {
		t.Fatalf("GetIssueCommentByID after rejected unpin failed: %v", err)
	}
	if !stored.IsPinned {
		t.Fatal("expected comment to remain pinned after rejected unpin")
	}
}

// TestAttachLabelsIdempotent_Issue verifies that AttachLabelsAndAssignees can
// attach the same label to an issue twice without error (covers comment.go
// issue_labels INSERT … WHERE NOT EXISTS path).
func TestAttachLabelsIdempotent_Issue(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "attachuser", "attachrepo")

	label, err := svc.CreateLabel(ctx, "attachuser/attachrepo", "attach-lbl", "112233", "")
	if err != nil {
		t.Fatalf("CreateLabel failed: %v", err)
	}
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "attachuser/attachrepo",
		Title:        "Attach test",
		AuthorLogin:  "attachuser",
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	labelGQLID := fmt.Sprintf("Label_%d", label.ID)
	issueID := issue.ID

	// First attach.
	if err := svc.AttachLabelsAndAssignees(ctx, &issueID, nil, []string{labelGQLID}, nil); err != nil {
		t.Fatalf("first AttachLabelsAndAssignees failed: %v", err)
	}

	// Duplicate attach must be idempotent.
	if err := svc.AttachLabelsAndAssignees(ctx, &issueID, nil, []string{labelGQLID}, nil); err != nil {
		t.Fatalf("duplicate AttachLabelsAndAssignees should be idempotent, got: %v", err)
	}

	// Verify only one label is attached.
	labels, err := svc.ListIssueLabels(ctx, "attachuser/attachrepo", issue.Number)
	if err != nil {
		t.Fatalf("ListIssueLabels failed: %v", err)
	}
	if len(labels) != 1 {
		t.Errorf("expected 1 label after duplicate attach, got %d", len(labels))
	}
}

// TestAttachLabelsIdempotent_PR verifies that AttachLabelsAndAssignees can
// attach the same label to a PR twice without error (covers comment.go
// pr_labels INSERT … WHERE NOT EXISTS path).
func TestAttachLabelsIdempotent_PR(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	pr, _, _ := setupPRWithRealBranches(t, svc, "attachpruser", "attachprrepo")

	label, err := svc.CreateLabel(ctx, "attachpruser/attachprrepo", "pr-attach-lbl", "445566", "")
	if err != nil {
		t.Fatalf("CreateLabel failed: %v", err)
	}

	labelGQLID := fmt.Sprintf("Label_%d", label.ID)
	prID := pr.ID

	// First attach.
	if err := svc.AttachLabelsAndAssignees(ctx, nil, &prID, []string{labelGQLID}, nil); err != nil {
		t.Fatalf("first AttachLabelsAndAssignees (PR) failed: %v", err)
	}

	// Duplicate attach must be idempotent.
	if err := svc.AttachLabelsAndAssignees(ctx, nil, &prID, []string{labelGQLID}, nil); err != nil {
		t.Fatalf("duplicate AttachLabelsAndAssignees (PR) should be idempotent, got: %v", err)
	}

	// Verify only one label is attached.
	labels, err := svc.ListPRLabels(ctx, "attachpruser/attachprrepo", pr.Number)
	if err != nil {
		t.Fatalf("ListPRLabels failed: %v", err)
	}
	if len(labels) != 1 {
		t.Errorf("expected 1 label after duplicate attach, got %d", len(labels))
	}
}

// TestAttachLabelsIdempotent_Issue_Concurrent verifies that concurrent goroutines
// calling AttachLabelsAndAssignees for the same issue+label all succeed and
// produce exactly one junction row.
func TestAttachLabelsIdempotent_Issue_Concurrent(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "concattuser", "concattrepo")

	label, err := svc.CreateLabel(ctx, "concattuser/concattrepo", "conc-attach-lbl", "112233", "")
	if err != nil {
		t.Fatalf("CreateLabel failed: %v", err)
	}
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "concattuser/concattrepo",
		Title:        "Concurrent attach test",
		AuthorLogin:  "concattuser",
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	labelGQLID := fmt.Sprintf("Label_%d", label.ID)
	issueID := issue.ID

	const goroutines = 10
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- svc.AttachLabelsAndAssignees(ctx, &issueID, nil, []string{labelGQLID}, nil)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("concurrent AttachLabelsAndAssignees (issue) returned error: %v", err)
		}
	}

	labels, err := svc.ListIssueLabels(ctx, "concattuser/concattrepo", issue.Number)
	if err != nil {
		t.Fatalf("ListIssueLabels failed: %v", err)
	}
	if len(labels) != 1 {
		t.Errorf("expected 1 label after concurrent attach, got %d", len(labels))
	}
}

// TestAttachLabelsIdempotent_PR_Concurrent verifies that concurrent goroutines
// calling AttachLabelsAndAssignees for the same PR+label all succeed and
// produce exactly one junction row.
func TestAttachLabelsIdempotent_PR_Concurrent(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	pr, _, _ := setupPRWithRealBranches(t, svc, "concattpruser", "concattprrepo")

	label, err := svc.CreateLabel(ctx, "concattpruser/concattprrepo", "conc-pr-attach-lbl", "445566", "")
	if err != nil {
		t.Fatalf("CreateLabel failed: %v", err)
	}

	labelGQLID := fmt.Sprintf("Label_%d", label.ID)
	prID := pr.ID

	const goroutines = 10
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- svc.AttachLabelsAndAssignees(ctx, nil, &prID, []string{labelGQLID}, nil)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("concurrent AttachLabelsAndAssignees (PR) returned error: %v", err)
		}
	}

	labels, err := svc.ListPRLabels(ctx, "concattpruser/concattprrepo", pr.Number)
	if err != nil {
		t.Fatalf("ListPRLabels failed: %v", err)
	}
	if len(labels) != 1 {
		t.Errorf("expected 1 label after concurrent attach, got %d", len(labels))
	}
}

func TestIssueCommentThreading(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "threaduser", "threadrepo")

	// Create issue
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "threaduser/threadrepo",
		Title:        "Issue for threaded comments",
		AuthorLogin:  "threaduser",
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Create top-level comment
	root, err := svc.CreateIssueComment(ctx, "threaduser/threadrepo", issue.Number, "Root comment", "threaduser", nil)
	if err != nil {
		t.Fatalf("CreateIssueComment failed: %v", err)
	}
	if root.InReplyToID != nil {
		t.Errorf("root comment should have nil InReplyToID, got %v", *root.InReplyToID)
	}
	if root.ThreadRootID != nil {
		t.Errorf("root comment should have nil ThreadRootID, got %v", *root.ThreadRootID)
	}

	// Create reply to root
	reply1, err := svc.CreateIssueComment(ctx, "threaduser/threadrepo", issue.Number, "Reply 1", "threaduser", &root.ID)
	if err != nil {
		t.Fatalf("CreateIssueComment (reply) failed: %v", err)
	}
	if reply1.InReplyToID == nil || *reply1.InReplyToID != root.ID {
		t.Errorf("reply1 InReplyToID should be %d, got %v", root.ID, reply1.InReplyToID)
	}
	if reply1.ThreadRootID == nil || *reply1.ThreadRootID != root.ID {
		t.Errorf("reply1 ThreadRootID should be %d, got %v", root.ID, reply1.ThreadRootID)
	}

	// Create nested reply (reply to reply)
	reply2, err := svc.CreateIssueComment(ctx, "threaduser/threadrepo", issue.Number, "Reply 2", "threaduser", &reply1.ID)
	if err != nil {
		t.Fatalf("CreateIssueComment (nested reply) failed: %v", err)
	}
	if reply2.InReplyToID == nil || *reply2.InReplyToID != reply1.ID {
		t.Errorf("reply2 InReplyToID should be %d, got %v", reply1.ID, reply2.InReplyToID)
	}
	if reply2.ThreadRootID == nil || *reply2.ThreadRootID != root.ID {
		t.Errorf("reply2 ThreadRootID should be %d (root), got %v", root.ID, reply2.ThreadRootID)
	}

	// Create another top-level comment
	root2, err := svc.CreateIssueComment(ctx, "threaduser/threadrepo", issue.Number, "Second root", "threaduser", nil)
	if err != nil {
		t.Fatalf("CreateIssueComment (root2) failed: %v", err)
	}
	if root2.InReplyToID != nil {
		t.Errorf("root2 should have nil InReplyToID")
	}
	if root2.ThreadRootID != nil {
		t.Errorf("root2 should have nil ThreadRootID")
	}

	// List comments threaded
	comments, err := svc.ListIssueCommentsThreaded(ctx, root.RepositoryID, issue.Number)
	if err != nil {
		t.Fatalf("ListIssueCommentsThreaded failed: %v", err)
	}
	if len(comments) != 4 {
		t.Errorf("expected 4 comments, got %d", len(comments))
	}
}

func TestGetIssueCommentThreadDepth(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "depthuser", "depthrepo")

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "depthuser/depthrepo",
		Title:        "Issue for depth test",
		AuthorLogin:  "depthuser",
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	root, err := svc.CreateIssueComment(ctx, "depthuser/depthrepo", issue.Number, "Root comment", "depthuser", nil)
	if err != nil {
		t.Fatalf("CreateIssueComment (root) failed: %v", err)
	}
	reply1, err := svc.CreateIssueComment(ctx, "depthuser/depthrepo", issue.Number, "Reply 1", "depthuser", &root.ID)
	if err != nil {
		t.Fatalf("CreateIssueComment (reply1) failed: %v", err)
	}
	reply2, err := svc.CreateIssueComment(ctx, "depthuser/depthrepo", issue.Number, "Reply 2", "depthuser", &reply1.ID)
	if err != nil {
		t.Fatalf("CreateIssueComment (reply2) failed: %v", err)
	}

	tests := []struct {
		name    string
		id      uint
		want    int
		wantErr error
	}{
		{name: "root", id: root.ID, want: 1},
		{name: "first_reply", id: reply1.ID, want: 2},
		{name: "second_reply", id: reply2.ID, want: 3},
		{name: "missing", id: 999999, wantErr: service.ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.GetIssueCommentThreadDepth(ctx, tt.id)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("GetIssueCommentThreadDepth error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetIssueCommentThreadDepth failed: %v", err)
			}
			if got != tt.want {
				t.Fatalf("GetIssueCommentThreadDepth = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestReplyToIssueCommentRejectsRepliesBeyondMaxDepth(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "replydepthuser", "replydepthrepo")

	repo, err := svc.GetRepo(ctx, "replydepthuser/replydepthrepo")
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: repo.FullName,
		Title:        "Issue for reply depth limit test",
		AuthorLogin:  "replydepthuser",
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	parent, err := svc.CreateIssueComment(ctx, repo.FullName, issue.Number, "Root comment", "replydepthuser", nil)
	if err != nil {
		t.Fatalf("CreateIssueComment (root) failed: %v", err)
	}

	for depth := 2; depth <= 5; depth++ {
		parent, err = svc.ReplyToIssueComment(ctx, repo.ID, issue.Number, parent.ID, fmt.Sprintf("Reply depth %d", depth), "replydepthuser")
		if err != nil {
			t.Fatalf("ReplyToIssueComment at depth %d failed: %v", depth, err)
		}
	}

	_, err = svc.ReplyToIssueComment(ctx, repo.ID, issue.Number, parent.ID, "Reply depth 6", "replydepthuser")
	if !errors.Is(err, service.ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
	if got, want := err.Error(), "validation failed: reply would exceed maximum issue comment thread depth of 5 levels"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestIssueCommentThreadingTransform(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "transformuser", "transformrepo")

	// Create issue
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "transformuser/transformrepo",
		Title:        "Issue for transform test",
		AuthorLogin:  "transformuser",
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Create comment with reply
	root, err := svc.CreateIssueComment(ctx, "transformuser/transformrepo", issue.Number, "Root", "transformuser", nil)
	if err != nil {
		t.Fatalf("CreateIssueComment failed: %v", err)
	}
	reply, err := svc.CreateIssueComment(ctx, "transformuser/transformrepo", issue.Number, "Reply", "transformuser", &root.ID)
	if err != nil {
		t.Fatalf("CreateIssueComment (reply) failed: %v", err)
	}

	// Get and transform
	got, err := svc.GetIssueCommentByID(ctx, reply.ID)
	if err != nil {
		t.Fatalf("GetIssueCommentByID failed: %v", err)
	}

	// Verify the model fields
	if got.InReplyToID == nil {
		t.Error("InReplyToID should not be nil")
	}
	if got.ThreadRootID == nil {
		t.Error("ThreadRootID should not be nil")
	}
}
