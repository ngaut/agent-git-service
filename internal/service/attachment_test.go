package service_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func TestIssueAttachmentFlow(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "attachuser", "attachrepo")
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "attachuser/attachrepo",
		Title:        "Attachment flow",
		AuthorLogin:  "attachuser",
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	user, err := svc.GetUser(ctx, "attachuser")
	if err != nil {
		t.Fatalf("GetUser failed: %v", err)
	}

	attachment := writeAttachmentFixture(t, svc, issue.ID, user, "notes.txt", "hello attachment")
	if attachment.IssueID == nil || *attachment.IssueID != issue.ID {
		t.Fatalf("attachment issue_id = %v, want %d", attachment.IssueID, issue.ID)
	}
	if attachment.RepositoryID != issue.RepositoryID {
		t.Fatalf("attachment repository_id = %d, want %d", attachment.RepositoryID, issue.RepositoryID)
	}
	if attachment.UploaderID != user.ID {
		t.Fatalf("attachment uploader_id = %d, want %d", attachment.UploaderID, user.ID)
	}
	repoDir := filepath.ToSlash(".attachments/repos/" + strconv.Itoa(int(issue.RepositoryID)) + "/")
	if !strings.HasPrefix(filepath.ToSlash(attachment.StoredPath), repoDir) {
		t.Fatalf("attachment stored_path = %q, want %s...", attachment.StoredPath, repoDir)
	}

	absPath := attachmentAbsPath(svc, attachment.StoredPath)
	if _, err := os.Stat(absPath); err != nil {
		t.Fatalf("attachment file stat: %v", err)
	}

	listed, err := svc.ListIssueAttachments(ctx, issue.ID)
	if err != nil {
		t.Fatalf("ListIssueAttachments failed: %v", err)
	}
	if len(listed) != 1 || listed[0].UUID != attachment.UUID {
		t.Fatalf("ListIssueAttachments = %#v, want %s", listed, attachment.UUID)
	}

	meta, file, err := svc.OpenIssueAttachment(ctx, attachment.UUID)
	if err != nil {
		t.Fatalf("OpenIssueAttachment failed: %v", err)
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if meta.UUID != attachment.UUID {
		t.Fatalf("opened attachment uuid = %q, want %q", meta.UUID, attachment.UUID)
	}
	if string(content) != "hello attachment" {
		t.Fatalf("attachment content = %q, want %q", string(content), "hello attachment")
	}

	if err := svc.DeleteIssueAttachment(ctx, attachment.UUID); err != nil {
		t.Fatalf("DeleteIssueAttachment failed: %v", err)
	}
	if _, err := os.Stat(absPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected attachment file to be removed, got %v", err)
	}
	if _, err := svc.GetIssueAttachmentByUUID(ctx, attachment.UUID); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("GetIssueAttachmentByUUID after delete = %v, want ErrNotFound", err)
	}
}

func TestRepoAttachmentFlow(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "repoattach", "attachrepo")
	repo, err := svc.GetRepo(ctx, "repoattach/attachrepo")
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}
	user, err := svc.GetUser(ctx, "repoattach")
	if err != nil {
		t.Fatalf("GetUser failed: %v", err)
	}

	attachment, err := svc.UploadRepoAttachment(
		service.ContextWithUser(ctx, user),
		repo.ID,
		"notes.txt",
		"text/plain",
		bytes.NewBufferString("draft attachment"),
	)
	if err != nil {
		t.Fatalf("UploadRepoAttachment failed: %v", err)
	}
	if attachment.IssueID != nil {
		t.Fatalf("attachment issue_id = %v, want nil", attachment.IssueID)
	}
	if attachment.RepositoryID != repo.ID {
		t.Fatalf("attachment repository_id = %d, want %d", attachment.RepositoryID, repo.ID)
	}
	repoDir := filepath.ToSlash(".attachments/repos/" + strconv.Itoa(int(repo.ID)) + "/")
	if !strings.HasPrefix(filepath.ToSlash(attachment.StoredPath), repoDir) {
		t.Fatalf("attachment stored_path = %q, want %s...", attachment.StoredPath, repoDir)
	}
}

func TestIssueAttachmentValidation(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	setupRepoForTest(t, svc, "attachvalidation", "attachrepo")
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "attachvalidation/attachrepo",
		Title:        "Attachment validation",
		AuthorLogin:  "attachvalidation",
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	user, err := svc.GetUser(ctx, "attachvalidation")
	if err != nil {
		t.Fatalf("GetUser failed: %v", err)
	}
	userCtx := service.ContextWithUser(ctx, user)

	if _, err := svc.UploadIssueAttachment(userCtx, issue.ID, "payload.exe", "application/octet-stream", bytes.NewReader([]byte("MZ"))); !errors.Is(err, service.ErrValidation) {
		t.Fatalf("UploadIssueAttachment(.exe) error = %v, want ErrValidation", err)
	}

	tooLarge := bytes.Repeat([]byte("a"), int(service.IssueAttachmentMaxSizeBytes)+1)
	if _, err := svc.UploadIssueAttachment(userCtx, issue.ID, "too-large.txt", "text/plain", bytes.NewReader(tooLarge)); !errors.Is(err, service.ErrValidation) {
		t.Fatalf("UploadIssueAttachment(too large) error = %v, want ErrValidation", err)
	}

	var count int64
	if err := svc.DB.Model(&db.Attachment{}).Count(&count).Error; err != nil {
		t.Fatalf("count attachments: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no persisted attachments after validation failures, got %d", count)
	}
}

func writeAttachmentFixture(t *testing.T, svc *service.Service, issueID uint, user db.User, name, body string) db.Attachment {
	t.Helper()

	attachment, err := svc.UploadIssueAttachment(
		service.ContextWithUser(context.Background(), user),
		issueID,
		name,
		"text/plain",
		bytes.NewBufferString(body),
	)
	if err != nil {
		t.Fatalf("UploadIssueAttachment failed: %v", err)
	}
	return attachment
}

func attachmentAbsPath(svc *service.Service, storedPath string) string {
	return filepath.Join(svc.AttachmentRoot, storedPath)
}
