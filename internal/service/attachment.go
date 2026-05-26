package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/randutil"
)

const (
	// IssueAttachmentMaxSizeBytes is the per-file attachment size limit.
	IssueAttachmentMaxSizeBytes int64 = 10 << 20
)

type attachmentTypeRule struct {
	contentType  string
	extensions   []string
	sniffAliases map[string]struct{}
	isImage      bool
}

var attachmentTypeRules = []attachmentTypeRule{
	newAttachmentTypeRule("image/png", true, ".png"),
	newAttachmentTypeRule("image/jpeg", true, ".jpg", ".jpeg"),
	newAttachmentTypeRule("image/gif", true, ".gif"),
	newAttachmentTypeRule("image/webp", true, ".webp"),
	newAttachmentTypeRule("application/pdf", false, ".pdf"),
	newAttachmentTypeRuleWithAliases("text/plain", false, []string{".txt", ".log", ".md", ".csv"}, "text/csv"),
	newAttachmentTypeRuleWithAliases("application/json", false, []string{".json"}, "text/plain"),
	newAttachmentTypeRule("application/zip", false, ".zip"),
	newAttachmentTypeRuleWithAliases("application/gzip", false, []string{".gz", ".tgz"}, "application/x-gzip"),
	newAttachmentTypeRuleWithAliases("application/x-tar", false, []string{".tar"}, "application/octet-stream"),
}

var (
	attachmentRulesByExt  map[string]attachmentTypeRule
	attachmentRulesByMIME map[string]attachmentTypeRule
)

func init() {
	attachmentRulesByExt = make(map[string]attachmentTypeRule)
	attachmentRulesByMIME = make(map[string]attachmentTypeRule)
	for _, rule := range attachmentTypeRules {
		attachmentRulesByMIME[rule.contentType] = rule
		for _, ext := range rule.extensions {
			attachmentRulesByExt[ext] = rule
		}
	}
}

func newAttachmentTypeRule(contentType string, isImage bool, extensions ...string) attachmentTypeRule {
	return newAttachmentTypeRuleWithAliases(contentType, isImage, extensions)
}

func newAttachmentTypeRuleWithAliases(contentType string, isImage bool, extensions []string, aliases ...string) attachmentTypeRule {
	sniffAliases := map[string]struct{}{
		contentType: {},
	}
	for _, alias := range aliases {
		sniffAliases[normalizeAttachmentMediaType(alias)] = struct{}{}
	}
	if strings.HasPrefix(contentType, "text/") {
		sniffAliases["text/plain"] = struct{}{}
	}
	return attachmentTypeRule{
		contentType:  contentType,
		extensions:   extensions,
		sniffAliases: sniffAliases,
		isImage:      isImage,
	}
}

func (r attachmentTypeRule) accepts(sniffed string) bool {
	_, ok := r.sniffAliases[sniffed]
	return ok
}

func (r attachmentTypeRule) canonicalExtension() string {
	if len(r.extensions) == 0 {
		return ""
	}
	return r.extensions[0]
}

func normalizeAttachmentMediaType(raw string) string {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(raw))
	if err == nil {
		return strings.ToLower(strings.TrimSpace(mediaType))
	}
	return strings.ToLower(strings.TrimSpace(strings.SplitN(raw, ";", 2)[0]))
}

func sanitizeAttachmentFilename(raw string) string {
	name := strings.TrimSpace(filepath.Base(raw))
	name = strings.ReplaceAll(name, "\x00", "")
	if name == "" || name == "." || name == string(filepath.Separator) {
		return ""
	}
	return name
}

func classifyAttachment(name string, content []byte) (attachmentTypeRule, string, error) {
	head := content
	if len(head) > 512 {
		head = head[:512]
	}
	sniffed := normalizeAttachmentMediaType(http.DetectContentType(head))
	ext := strings.ToLower(filepath.Ext(name))
	if ext != "" {
		rule, ok := attachmentRulesByExt[ext]
		if !ok {
			return attachmentTypeRule{}, "", fmt.Errorf("%w: unsupported attachment type", ErrValidation)
		}
		if !rule.accepts(sniffed) {
			return attachmentTypeRule{}, "", fmt.Errorf("%w: unsupported attachment type", ErrValidation)
		}
		return rule, ext, nil
	}
	if rule, ok := attachmentRulesByMIME[sniffed]; ok {
		return rule, rule.canonicalExtension(), nil
	}
	return attachmentTypeRule{}, "", fmt.Errorf("%w: unsupported attachment type", ErrValidation)
}

func generateAttachmentUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		raw[0:4],
		raw[4:6],
		raw[6:8],
		raw[8:10],
		raw[10:16],
	), nil
}

func (s *Service) attachmentRootDir() string {
	root := strings.TrimSpace(s.AttachmentRoot)
	if root != "" {
		return root
	}
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		return cwd
	}
	return "."
}

func attachmentStoredPath(repoID uint, uuid, ext string) string {
	filename := uuid + ext
	return filepath.Join(".attachments", "repos", strconv.FormatUint(uint64(repoID), 10), filename)
}

func attachmentRepoDir(repoID uint) string {
	return filepath.Join(".attachments", "repos", strconv.FormatUint(uint64(repoID), 10))
}

func (s *Service) attachmentAbsolutePath(storedPath string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(storedPath))
	if clean == "" || clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") || strings.Contains(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: invalid attachment path", ErrValidation)
	}
	return filepath.Join(s.attachmentRootDir(), clean), nil
}

func (s *Service) cleanupAttachmentPaths(paths []string) {
	seenDirs := map[string]struct{}{}
	for _, storedPath := range paths {
		absPath, err := s.attachmentAbsolutePath(storedPath)
		if err != nil {
			slog.Warn("attachment cleanup: invalid path", "path", storedPath, "error", err)
			continue
		}
		if err := os.Remove(absPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("attachment cleanup: remove file", "path", absPath, "error", err)
		}
		seenDirs[filepath.Dir(absPath)] = struct{}{}
	}
	for dir := range seenDirs {
		if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			slog.Debug("attachment cleanup: keep directory", "dir", dir, "error", err)
		}
	}
}

func (s *Service) removeRepoAttachmentDir(repoID uint) error {
	dir := filepath.Join(s.attachmentRootDir(), attachmentRepoDir(repoID))
	return os.RemoveAll(dir)
}

func (s *Service) AttachmentMarkdown(a db.Attachment) string {
	downloadURL := fmt.Sprintf("%s/api/v3/attachments/%s", s.BaseURL, a.UUID)
	label := strings.NewReplacer("[", "", "]", "").Replace(strings.TrimSpace(a.OriginalName))
	if label == "" {
		label = "attachment"
	}
	if a.IsImage {
		return fmt.Sprintf("![%s](%s)", label, downloadURL)
	}
	return fmt.Sprintf("[%s](%s)", label, downloadURL)
}

// UploadIssueAttachment stores an issue attachment on disk and in the database.
func (s *Service) UploadIssueAttachment(ctx context.Context, issueID uint, originalName, declaredContentType string, body io.Reader) (db.Attachment, error) {
	_ = normalizeAttachmentMediaType(declaredContentType)

	issue, err := s.GetIssueByID(ctx, issueID)
	if err != nil {
		return db.Attachment{}, err
	}
	return s.uploadAttachment(ctx, issue.RepositoryID, &issue.ID, originalName, body)
}

// UploadRepoAttachment stores a repository-scoped attachment on disk and in the database.
func (s *Service) UploadRepoAttachment(ctx context.Context, repoID uint, originalName, declaredContentType string, body io.Reader) (db.Attachment, error) {
	_ = normalizeAttachmentMediaType(declaredContentType)
	return s.uploadAttachment(ctx, repoID, nil, originalName, body)
}

func (s *Service) uploadAttachment(ctx context.Context, repoID uint, issueID *uint, originalName string, body io.Reader) (db.Attachment, error) {
	name := sanitizeAttachmentFilename(originalName)
	if name == "" {
		return db.Attachment{}, fmt.Errorf("%w: attachment filename is required", ErrValidation)
	}
	content, err := io.ReadAll(io.LimitReader(body, IssueAttachmentMaxSizeBytes+1))
	if err != nil {
		return db.Attachment{}, err
	}
	if int64(len(content)) > IssueAttachmentMaxSizeBytes {
		return db.Attachment{}, fmt.Errorf("%w: attachment exceeds 10MB limit", ErrValidation)
	}
	if len(content) == 0 {
		return db.Attachment{}, fmt.Errorf("%w: attachment is empty", ErrValidation)
	}

	rule, ext, err := classifyAttachment(name, content)
	if err != nil {
		return db.Attachment{}, err
	}

	if s.AttachmentScanner != nil {
		if err := s.AttachmentScanner(ctx, name, rule.contentType, content); err != nil {
			return db.Attachment{}, err
		}
	}

	uuid, err := generateAttachmentUUID()
	if err != nil {
		return db.Attachment{}, err
	}
	storedPath := attachmentStoredPath(repoID, uuid, ext)
	absPath, err := s.attachmentAbsolutePath(storedPath)
	if err != nil {
		return db.Attachment{}, err
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o750); err != nil {
		return db.Attachment{}, err
	}
	tmpPath := absPath + ".tmp-" + randutil.Hex(8)
	if err := os.WriteFile(tmpPath, content, 0o640); err != nil {
		return db.Attachment{}, err
	}
	if err := os.Rename(tmpPath, absPath); err != nil {
		_ = os.Remove(tmpPath)
		return db.Attachment{}, err
	}

	var uploaderID uint
	if viewer, ok := UserFromContext(ctx); ok {
		uploaderID = viewer.ID
	}
	attachment := db.Attachment{
		UUID:         uuid,
		IssueID:      issueID,
		RepositoryID: repoID,
		UploaderID:   uploaderID,
		OriginalName: name,
		StoredPath:   storedPath,
		ContentType:  rule.contentType,
		Extension:    strings.TrimPrefix(ext, "."),
		Size:         int64(len(content)),
		IsImage:      rule.isImage,
	}
	if err := s.DBForCtx(ctx).Create(&attachment).Error; err != nil {
		s.cleanupAttachmentPaths([]string{storedPath})
		return db.Attachment{}, err
	}
	if err := s.DBForCtx(ctx).Preload("Uploader").First(&attachment, attachment.ID).Error; err != nil {
		s.cleanupAttachmentPaths([]string{storedPath})
		_ = deleteByID[db.Attachment](s, ctx, attachment.ID)
		return db.Attachment{}, wrapErr(err)
	}
	return attachment, nil
}

// ListIssueAttachments returns all attachments for an issue.
func (s *Service) ListIssueAttachments(ctx context.Context, issueID uint) ([]db.Attachment, error) {
	var attachments []db.Attachment
	if err := s.DBForCtx(ctx).
		Preload("Uploader").
		Where("issue_id = ?", issueID).
		Order("created_at ASC").
		Find(&attachments).Error; err != nil {
		return nil, err
	}
	return attachments, nil
}

// GetIssueAttachmentByUUID returns attachment metadata by UUID.
func (s *Service) GetIssueAttachmentByUUID(ctx context.Context, uuid string) (db.Attachment, error) {
	var attachment db.Attachment
	if err := s.DBForCtx(ctx).
		Preload("Uploader").
		First(&attachment, "uuid = ?", strings.TrimSpace(uuid)).Error; err != nil {
		return attachment, wrapErr(err)
	}
	return attachment, nil
}

// OpenIssueAttachment returns metadata and an open file handle for an attachment.
func (s *Service) OpenIssueAttachment(ctx context.Context, uuid string) (db.Attachment, *os.File, error) {
	attachment, err := s.GetIssueAttachmentByUUID(ctx, uuid)
	if err != nil {
		return attachment, nil, err
	}
	absPath, err := s.attachmentAbsolutePath(attachment.StoredPath)
	if err != nil {
		return attachment, nil, err
	}
	file, err := os.Open(absPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return attachment, nil, ErrNotFound
		}
		return attachment, nil, err
	}
	return attachment, file, nil
}

// DeleteIssueAttachment removes attachment metadata and deletes the stored file.
func (s *Service) DeleteIssueAttachment(ctx context.Context, uuid string) error {
	attachment, err := s.GetIssueAttachmentByUUID(ctx, uuid)
	if err != nil {
		return err
	}
	if err := checkAffected(s.DBForCtx(ctx).Where("id = ?", attachment.ID).Delete(&db.Attachment{})); err != nil {
		return err
	}
	s.cleanupAttachmentPaths([]string{attachment.StoredPath})
	return nil
}
