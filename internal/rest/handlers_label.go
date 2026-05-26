package rest

import (
	"errors"
	"net/http"

	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/service"
)

// --- Labels ---

// ListLabels handles GET /api/v3/repos/{owner}/{repo}/labels
func (d *Deps) ListLabels(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	page, perPage := parsePagination(r)
	labels, err := d.Svc.ListLabels(r.Context(), full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(labels))
	for i, l := range labels {
		out[i] = transform.Label(l)
	}
	respond.JSON(w, 200, paginate(w, r, d.Svc.BaseURL, out, page, perPage))
}

// CreateLabel handles POST /api/v3/repos/{owner}/{repo}/labels
func (d *Deps) CreateLabel(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	var body struct {
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	label, err := d.Svc.CreateLabel(r.Context(), full, body.Name, body.Color, body.Description)
	// GitHub API returns 422 for creation failures — intentional compatibility.
	if err != nil {
		respond.ValidationFailed(w, err.Error())
		return
	}
	respond.JSON(w, 201, transform.Label(label))
}

// DeleteLabel handles DELETE /api/v3/repos/{owner}/{repo}/labels/{name}
func (d *Deps) DeleteLabel(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	name := pathParam(r, "name")
	err := d.Svc.DeleteLabel(r.Context(), full, name)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// EditLabel handles PATCH /api/v3/repos/{owner}/{repo}/labels/{name}
func (d *Deps) EditLabel(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	oldName := pathParam(r, "name")
	var body struct {
		NewName     *string `json:"new_name"`
		Color       *string `json:"color"`
		Description *string `json:"description"`
	}
	decodeBody(r, &body)
	label, err := d.Svc.EditLabel(r.Context(), full, oldName, service.EditLabelInput{
		NewName:     body.NewName,
		Color:       body.Color,
		Description: body.Description,
	})
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.Label(label))
}

// RemoveIssueLabel handles DELETE /api/v3/repos/{owner}/{repo}/issues/{issue_number}/labels/{name}
func (d *Deps) RemoveIssueLabel(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	issueNum, ok := mustIntParamAny(w, r, "issue_number", "number")
	if !ok {
		return
	}
	name := pathParam(r, "name")
	var err error
	remaining, err := d.Svc.RemoveIssueLabel(r.Context(), full, issueNum, name)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			remaining, err = d.Svc.RemovePRLabel(r.Context(), full, issueNum, name)
			if err != nil {
				respond.ServiceErrorRequest(r, w, err)
				return
			}
		} else {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
	}
	out := make([]any, len(remaining))
	for i, l := range remaining {
		out[i] = transform.Label(l)
	}
	respond.JSON(w, 200, out)
}

// GetLabel handles GET /api/v3/repos/{owner}/{repo}/labels/{name}
func (d *Deps) GetLabel(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	name := pathParam(r, "name")
	label, err := d.Svc.GetLabel(r.Context(), full, name)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.Label(label))
}

// ListIssueLabels handles GET /api/v3/repos/{owner}/{repo}/issues/{issue_number}/labels
func (d *Deps) ListIssueLabels(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	issueNum, ok := mustIntParamAny(w, r, "issue_number", "number")
	if !ok {
		return
	}
	labels, err := d.Svc.ListIssueLabels(r.Context(), full, issueNum)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			labels, err = d.Svc.ListPRLabels(r.Context(), full, issueNum)
			if err != nil {
				respond.ServiceErrorRequest(r, w, err)
				return
			}
		} else {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
	}
	out := make([]any, len(labels))
	for i, l := range labels {
		out[i] = transform.Label(l)
	}
	respond.JSON(w, 200, out)
}

// AddIssueLabels handles POST /api/v3/repos/{owner}/{repo}/issues/{issue_number}/labels
func (d *Deps) AddIssueLabels(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	issueNum, ok := mustIntParamAny(w, r, "issue_number", "number")
	if !ok {
		return
	}
	var body struct {
		Labels []string `json:"labels"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	labels, err := d.Svc.AddIssueLabels(r.Context(), full, issueNum, body.Labels)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			labels, err = d.Svc.AddPRLabels(r.Context(), full, issueNum, body.Labels)
			if err != nil {
				respond.ServiceErrorRequest(r, w, err)
				return
			}
		} else {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
	}
	out := make([]any, len(labels))
	for i, l := range labels {
		out[i] = transform.Label(l)
	}
	respond.JSON(w, 200, out)
}

// SetIssueLabels handles PUT /api/v3/repos/{owner}/{repo}/issues/{issue_number}/labels
func (d *Deps) SetIssueLabels(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	issueNum, ok := mustIntParamAny(w, r, "issue_number", "number")
	if !ok {
		return
	}
	var body struct {
		Labels []string `json:"labels"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	labels, err := d.Svc.SetIssueLabels(r.Context(), full, issueNum, body.Labels)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			labels, err = d.Svc.SetPRLabels(r.Context(), full, issueNum, body.Labels)
			if err != nil {
				respond.ServiceErrorRequest(r, w, err)
				return
			}
		} else {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
	}
	out := make([]any, len(labels))
	for i, l := range labels {
		out[i] = transform.Label(l)
	}
	respond.JSON(w, 200, out)
}

// RemoveAllIssueLabels handles DELETE /api/v3/repos/{owner}/{repo}/issues/{issue_number}/labels
func (d *Deps) RemoveAllIssueLabels(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	issueNum, ok := mustIntParamAny(w, r, "issue_number", "number")
	if !ok {
		return
	}
	if err := d.Svc.RemoveAllIssueLabels(r.Context(), full, issueNum); err != nil {
		if errors.Is(err, service.ErrNotFound) {
			if err := d.Svc.RemoveAllPRLabels(r.Context(), full, issueNum); err != nil {
				respond.ServiceErrorRequest(r, w, err)
				return
			}
		} else {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
	}
	respond.NoContent(w)
}
