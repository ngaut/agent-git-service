package rest

import (
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"gh-server/internal/db"
	"gh-server/internal/rest/respond"
	"gh-server/internal/rest/transform"
	"gh-server/internal/service"
)

const attachmentMultipartRequestLimit = service.IssueAttachmentMaxSizeBytes + (1 << 20)

func parseAttachmentUpload(w http.ResponseWriter, r *http.Request) (multipart.File, *multipart.FileHeader, func(), bool) {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type"))), "multipart/form-data") {
		respond.ValidationFailed(w, "Content-Type must be multipart/form-data")
		return nil, nil, nil, false
	}

	r.Body = http.MaxBytesReader(w, r.Body, attachmentMultipartRequestLimit)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "too large") {
			respond.ValidationFailed(w, "attachment exceeds 10MB limit")
			return nil, nil, nil, false
		}
		respond.ValidationFailed(w, "invalid multipart body")
		return nil, nil, nil, false
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		respond.ValidationFailed(w, "file is required")
		return nil, nil, nil, false
	}
	cleanup := func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}
	return file, header, cleanup, true
}

// UploadIssueAttachment handles POST /api/v3/issues/{id}/attachments.
func (d *Deps) UploadIssueAttachment(w http.ResponseWriter, r *http.Request) {
	id, ok := mustIntParam(w, r, "id")
	if !ok {
		return
	}
	issue, err := d.Svc.GetIssueByID(r.Context(), uint(id))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if !d.requireRepoPermission(w, r, issue.RepositoryID, service.RepoPermissionWrite) {
		return
	}
	file, header, cleanup, ok := parseAttachmentUpload(w, r)
	if !ok {
		return
	}
	defer file.Close()
	defer cleanup()

	attachment, err := d.Svc.UploadIssueAttachment(r.Context(), issue.ID, header.Filename, header.Header.Get("Content-Type"), file)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusCreated, transform.Attachment(attachment))
}

// UploadRepoAttachment handles POST /api/v3/repos/{owner}/{repo}/attachments.
func (d *Deps) UploadRepoAttachment(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	d.uploadRepoAttachment(w, r, *repo)
}

// UploadRepoAttachmentByID handles POST /api/v3/repositories/{repo_id}/attachments.
func (d *Deps) UploadRepoAttachmentByID(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepoByID(w, r)
	if repo == nil {
		return
	}
	d.uploadRepoAttachment(w, r, *repo)
}

func (d *Deps) uploadRepoAttachment(w http.ResponseWriter, r *http.Request, repo db.Repository) {
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}
	file, header, cleanup, ok := parseAttachmentUpload(w, r)
	if !ok {
		return
	}
	defer file.Close()
	defer cleanup()

	attachment, err := d.Svc.UploadRepoAttachment(r.Context(), repo.ID, header.Filename, header.Header.Get("Content-Type"), file)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusCreated, transform.Attachment(attachment))
}

// ListIssueAttachments handles GET /api/v3/issues/{id}/attachments.
func (d *Deps) ListIssueAttachments(w http.ResponseWriter, r *http.Request) {
	id, ok := mustIntParam(w, r, "id")
	if !ok {
		return
	}
	if _, err := d.Svc.GetIssueByID(r.Context(), uint(id)); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	attachments, err := d.Svc.ListIssueAttachments(r.Context(), uint(id))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(attachments))
	for i, attachment := range attachments {
		out[i] = transform.Attachment(attachment)
	}
	respond.JSON(w, http.StatusOK, out)
}

// DownloadIssueAttachment handles GET /api/v3/attachments/{uuid}.
func (d *Deps) DownloadIssueAttachment(w http.ResponseWriter, r *http.Request) {
	uuid := strings.TrimSpace(chi.URLParam(r, "uuid"))
	if uuid == "" {
		respond.NotFound(w)
		return
	}
	attachment, file, err := d.Svc.OpenIssueAttachment(r.Context(), uuid)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	defer file.Close()

	contentType := attachment.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	disposition := "attachment"
	if attachment.IsImage {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", disposition+`; filename="`+strings.ReplaceAll(attachment.OriginalName, `"`, `\"`)+`"`)
	http.ServeContent(w, r, attachment.OriginalName, attachment.UpdatedAt, file)
}

// DeleteIssueAttachment handles DELETE /api/v3/attachments/{uuid}.
func (d *Deps) DeleteIssueAttachment(w http.ResponseWriter, r *http.Request) {
	uuid := strings.TrimSpace(chi.URLParam(r, "uuid"))
	if uuid == "" {
		respond.NotFound(w)
		return
	}
	attachment, err := d.Svc.GetIssueAttachmentByUUID(r.Context(), uuid)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if !d.requireRepoPermission(w, r, attachment.RepositoryID, service.RepoPermissionWrite) {
		return
	}
	if err := d.Svc.DeleteIssueAttachment(r.Context(), uuid); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}
