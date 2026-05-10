package service_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

const reactionListLimit = 1000

func createReactionTestUser(t *testing.T, svc *service.Service, login string) db.User {
	t.Helper()
	user := db.User{Login: login, Name: login, Type: db.TypeUser}
	if err := svc.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user %q failed: %v", login, err)
	}
	return user
}

func createTestRepo(t *testing.T, svc *service.Service, owner db.User, name string) db.Repository {
	t.Helper()
	repo := db.Repository{
		OwnerID:       owner.ID,
		Owner:         owner,
		Name:          name,
		FullName:      owner.Login + "/" + name,
		DefaultBranch: "main",
	}
	if err := svc.DB.Create(&repo).Error; err != nil {
		t.Fatalf("create repo %q failed: %v", repo.FullName, err)
	}
	return repo
}

func createTestIssue(t *testing.T, svc *service.Service, repo db.Repository, author db.User, number int) db.Issue {
	t.Helper()
	issue := db.Issue{
		RepositoryID: repo.ID,
		Number:       number,
		Title:        fmt.Sprintf("Issue %d", number),
		Body:         "body",
		AuthorID:     author.ID,
	}
	if err := svc.DB.Create(&issue).Error; err != nil {
		t.Fatalf("create issue failed: %v", err)
	}
	return issue
}

func createTestComment(t *testing.T, svc *service.Service, repo db.Repository, issue db.Issue, author db.User) db.IssueComment {
	t.Helper()
	comment := db.IssueComment{
		RepositoryID: repo.ID,
		IssueNumber:  issue.Number,
		Body:         "comment",
		AuthorID:     author.ID,
	}
	if err := svc.DB.Create(&comment).Error; err != nil {
		t.Fatalf("create comment failed: %v", err)
	}
	return comment
}

func createTestReaction(t *testing.T, svc *service.Service, issueID *uint, commentID *uint, user db.User, content string) db.Reaction {
	t.Helper()
	reaction := db.Reaction{
		IssueID:   issueID,
		CommentID: commentID,
		UserID:    user.ID,
		Content:   content,
	}
	if err := svc.DB.Create(&reaction).Error; err != nil {
		t.Fatalf("create reaction failed: %v", err)
	}
	return reaction
}

func TestListIssueReactions(t *testing.T) {
	t.Run("normal case", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()
		ctx := context.Background()

		owner := createReactionTestUser(t, svc, "issue-owner")
		user1 := createReactionTestUser(t, svc, "issue-reactor-1")
		user2 := createReactionTestUser(t, svc, "issue-reactor-2")
		repo := createTestRepo(t, svc, owner, "issue-repo")
		issue := createTestIssue(t, svc, repo, owner, 1)

		createTestReaction(t, svc, &issue.ID, nil, user1, "+1")
		createTestReaction(t, svc, &issue.ID, nil, user2, "heart")

		reactions, err := svc.ListIssueReactions(ctx, int64(issue.ID))
		if err != nil {
			t.Fatalf("ListIssueReactions failed: %v", err)
		}
		if len(reactions) != 2 {
			t.Fatalf("expected 2 reactions, got %d", len(reactions))
		}
	})

	t.Run("limit behavior", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()
		ctx := context.Background()

		owner := createReactionTestUser(t, svc, "limit-owner")
		reactor := createReactionTestUser(t, svc, "limit-reactor")
		repo := createTestRepo(t, svc, owner, "limit-repo")
		issue := createTestIssue(t, svc, repo, owner, 1)

		for i := 0; i < reactionListLimit+10; i++ {
			content := fmt.Sprintf("c%04d", i)
			createTestReaction(t, svc, &issue.ID, nil, reactor, content)
		}

		reactions, err := svc.ListIssueReactions(ctx, int64(issue.ID))
		if err != nil {
			t.Fatalf("ListIssueReactions failed: %v", err)
		}
		if len(reactions) != reactionListLimit {
			t.Fatalf("expected %d reactions, got %d", reactionListLimit, len(reactions))
		}
	})
}

func TestListCommentReactions(t *testing.T) {
	t.Run("normal case", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()
		ctx := context.Background()

		owner := createReactionTestUser(t, svc, "comment-owner")
		user1 := createReactionTestUser(t, svc, "comment-reactor-1")
		user2 := createReactionTestUser(t, svc, "comment-reactor-2")
		repo := createTestRepo(t, svc, owner, "comment-repo")
		issue := createTestIssue(t, svc, repo, owner, 1)
		comment := createTestComment(t, svc, repo, issue, owner)

		createTestReaction(t, svc, nil, &comment.ID, user1, "+1")
		createTestReaction(t, svc, nil, &comment.ID, user2, "heart")

		reactions, err := svc.ListCommentReactions(ctx, int64(comment.ID))
		if err != nil {
			t.Fatalf("ListCommentReactions failed: %v", err)
		}
		if len(reactions) != 2 {
			t.Fatalf("expected 2 reactions, got %d", len(reactions))
		}
	})

	t.Run("limit behavior", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()
		ctx := context.Background()

		owner := createReactionTestUser(t, svc, "comment-limit-owner")
		reactor := createReactionTestUser(t, svc, "comment-limit-reactor")
		repo := createTestRepo(t, svc, owner, "comment-limit-repo")
		issue := createTestIssue(t, svc, repo, owner, 1)
		comment := createTestComment(t, svc, repo, issue, owner)

		for i := 0; i < reactionListLimit+10; i++ {
			content := fmt.Sprintf("c%04d", i)
			createTestReaction(t, svc, nil, &comment.ID, reactor, content)
		}

		reactions, err := svc.ListCommentReactions(ctx, int64(comment.ID))
		if err != nil {
			t.Fatalf("ListCommentReactions failed: %v", err)
		}
		if len(reactions) != reactionListLimit {
			t.Fatalf("expected %d reactions, got %d", reactionListLimit, len(reactions))
		}
	})
}

func TestCountReactions(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	owner := createReactionTestUser(t, svc, "count-owner")
	user1 := createReactionTestUser(t, svc, "count-reactor-1")
	user2 := createReactionTestUser(t, svc, "count-reactor-2")
	repo := createTestRepo(t, svc, owner, "count-repo")
	issue := createTestIssue(t, svc, repo, owner, 1)
	comment := createTestComment(t, svc, repo, issue, owner)

	createTestReaction(t, svc, &issue.ID, nil, user1, "+1")
	createTestReaction(t, svc, &issue.ID, nil, user2, "+1")
	createTestReaction(t, svc, &issue.ID, nil, user1, "heart")
	createTestReaction(t, svc, &issue.ID, nil, user1, "custom")

	createTestReaction(t, svc, nil, &comment.ID, user1, "laugh")
	createTestReaction(t, svc, nil, &comment.ID, user2, "laugh")
	createTestReaction(t, svc, nil, &comment.ID, user2, "eyes")

	issueCounts, err := svc.CountReactions(ctx, issue.ID, 0)
	if err != nil {
		t.Fatalf("CountReactions(issue) failed: %v", err)
	}
	if issueCounts["+1"] != 2 {
		t.Fatalf("expected +1 count 2, got %d", issueCounts["+1"])
	}
	if issueCounts["heart"] != 1 {
		t.Fatalf("expected heart count 1, got %d", issueCounts["heart"])
	}
	if issueCounts["custom"] != 1 {
		t.Fatalf("expected custom count 1, got %d", issueCounts["custom"])
	}
	if _, ok := issueCounts["laugh"]; ok {
		t.Fatalf("unexpected laugh count for issue: %d", issueCounts["laugh"])
	}

	commentCounts, err := svc.CountReactions(ctx, 0, comment.ID)
	if err != nil {
		t.Fatalf("CountReactions(comment) failed: %v", err)
	}
	if commentCounts["laugh"] != 2 {
		t.Fatalf("expected laugh count 2, got %d", commentCounts["laugh"])
	}
	if commentCounts["eyes"] != 1 {
		t.Fatalf("expected eyes count 1, got %d", commentCounts["eyes"])
	}
	if _, ok := commentCounts["+1"]; ok {
		t.Fatalf("unexpected +1 count for comment: %d", commentCounts["+1"])
	}

	if _, err := svc.CountReactions(ctx, 0, 0); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := svc.CountReactions(ctx, issue.ID, comment.ID); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGetReaction(t *testing.T) {
	t.Run("issue reaction normal", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()
		ctx := context.Background()

		owner := createReactionTestUser(t, svc, "get-issue-owner")
		reactor := createReactionTestUser(t, svc, "get-issue-reactor")
		repo := createTestRepo(t, svc, owner, "get-issue-repo")
		issue := createTestIssue(t, svc, repo, owner, 1)
		reaction := createTestReaction(t, svc, &issue.ID, nil, reactor, "+1")

		got, err := svc.GetReaction(ctx, int64(reaction.ID))
		if err != nil {
			t.Fatalf("GetReaction failed: %v", err)
		}
		if got.ID != reaction.ID {
			t.Fatalf("expected reaction ID %d, got %d", reaction.ID, got.ID)
		}
		if got.IssueID == nil || *got.IssueID != issue.ID {
			t.Fatalf("expected issue ID %d, got %v", issue.ID, got.IssueID)
		}
	})

	t.Run("comment reaction normal", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()
		ctx := context.Background()

		owner := createReactionTestUser(t, svc, "get-comment-owner")
		reactor := createReactionTestUser(t, svc, "get-comment-reactor")
		repo := createTestRepo(t, svc, owner, "get-comment-repo")
		issue := createTestIssue(t, svc, repo, owner, 1)
		comment := createTestComment(t, svc, repo, issue, owner)
		reaction := createTestReaction(t, svc, nil, &comment.ID, reactor, "+1")

		got, err := svc.GetReaction(ctx, int64(reaction.ID))
		if err != nil {
			t.Fatalf("GetReaction failed: %v", err)
		}
		if got.ID != reaction.ID {
			t.Fatalf("expected reaction ID %d, got %d", reaction.ID, got.ID)
		}
		if got.CommentID == nil || *got.CommentID != comment.ID {
			t.Fatalf("expected comment ID %d, got %v", comment.ID, got.CommentID)
		}
	})

	t.Run("not found reaction id", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()
		ctx := context.Background()

		if _, err := svc.GetReaction(ctx, 9999); !errors.Is(err, service.ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("not found missing reference", func(t *testing.T) {
		svc, cleanup := setupTestService(t)
		defer cleanup()
		ctx := context.Background()

		user := createReactionTestUser(t, svc, "missing-ref-user")
		reaction := createTestReaction(t, svc, nil, nil, user, "+1")

		if _, err := svc.GetReaction(ctx, int64(reaction.ID)); !errors.Is(err, service.ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})
}
