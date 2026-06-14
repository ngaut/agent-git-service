package rest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/service"
)

const branchProtectionPathSegment = "/protection"

var branchProtectionResources = map[string]struct{}{
	"":                                {},
	"required_status_checks":          {},
	"required_status_checks/contexts": {},
	"enforce_admins":                  {},
	"required_signatures":             {},
	"required_pull_request_reviews":   {},
	"restrictions":                    {},
	"restrictions/users":              {},
	"restrictions/teams":              {},
	"restrictions/apps":               {},
}

func branchProtectionJSON(bp db.BranchProtection) map[string]any {
	return map[string]any{
		"url":                           branchProtectionBaseURL(bp),
		"required_status_checks":        branchProtectionRequiredStatusChecksJSON(bp),
		"enforce_admins":                branchProtectionEnforceAdminsJSON(bp),
		"required_signatures":           branchProtectionRequiredSignaturesJSON(bp),
		"required_pull_request_reviews": branchProtectionRequiredPullRequestReviewsJSON(bp),
		"restrictions":                  branchProtectionRestrictionsJSON(bp),
	}
}

func branchProtectionBaseURL(bp db.BranchProtection) string {
	return fmt.Sprintf("%s/repos/%s/branches/%s/protection", transform.APIBase(), bp.Repository.FullName, bp.BranchName)
}

func branchProtectionRequiredStatusChecksJSON(bp db.BranchProtection) map[string]any {
	reqStatus := branchProtectionDecodeMap(bp.RequiredStatusChecksJSON, "RequiredStatusChecksJSON", bp.BranchName)
	if reqStatus == nil {
		return nil
	}
	base := branchProtectionBaseURL(bp)
	reqStatus["url"] = base + "/required_status_checks"
	reqStatus["contexts_url"] = base + "/required_status_checks/contexts"
	if _, ok := reqStatus["contexts"]; !ok {
		reqStatus["contexts"] = []string{}
	}
	return reqStatus
}

func branchProtectionEnforceAdminsJSON(bp db.BranchProtection) map[string]any {
	return map[string]any{
		"enabled": bp.EnforceAdmins,
		"url":     branchProtectionBaseURL(bp) + "/enforce_admins",
	}
}

func branchProtectionRequiredSignaturesJSON(bp db.BranchProtection) map[string]any {
	return map[string]any{
		"enabled": bp.RequiredSignatures,
		"url":     branchProtectionBaseURL(bp) + "/required_signatures",
	}
}

func branchProtectionRequiredPullRequestReviewsJSON(bp db.BranchProtection) map[string]any {
	reqPR := branchProtectionDecodeMap(bp.RequiredPullRequestJSON, "RequiredPullRequestJSON", bp.BranchName)
	if reqPR == nil {
		return nil
	}
	reqPR["url"] = branchProtectionBaseURL(bp) + "/required_pull_request_reviews"
	return reqPR
}

func branchProtectionRestrictionsJSON(bp db.BranchProtection) map[string]any {
	restrict := branchProtectionDecodeMap(bp.RestrictionsJSON, "RestrictionsJSON", bp.BranchName)
	if restrict == nil {
		return nil
	}
	base := branchProtectionBaseURL(bp) + "/restrictions"
	restrict["url"] = base
	restrict["users_url"] = base + "/users"
	restrict["teams_url"] = base + "/teams"
	restrict["apps_url"] = base + "/apps"
	return restrict
}

func branchProtectionDecodeMap(raw, field, branch string) map[string]any {
	if raw == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		slog.Warn("failed to unmarshal branch protection JSON", "field", field, "error", err, "branch", branch)
		return nil
	}
	return out
}

func parseBranchProtectionPath(branchPath string) (branch, resource string, ok bool) {
	idx := strings.LastIndex(branchPath, branchProtectionPathSegment)
	if idx < 0 {
		return "", "", false
	}
	end := idx + len(branchProtectionPathSegment)
	if end != len(branchPath) && branchPath[end] != '/' {
		return "", "", false
	}
	branch = strings.TrimSuffix(branchPath[:idx], "/")
	if branch == "" {
		return "", "", false
	}
	resource = strings.TrimPrefix(branchPath[end:], "/")
	if _, ok := branchProtectionResources[resource]; !ok {
		return "", "", false
	}
	return branch, resource, true
}

func (d *Deps) resolveBranchProtectionPath(ctx context.Context, full, branchPath string) (branch, resource string, ok bool) {
	branch, resource, ok = parseBranchProtectionPath(branchPath)
	if !ok {
		return "", "", false
	}

	// Preserve the pre-existing ".../protection" ambiguity, but when the full
	// wildcard already names a real branch we must not reinterpret recognized
	// suffixes like ".../required_status_checks" as protection subresources.
	if resource == "" {
		return branch, resource, true
	}
	if _, err := d.Svc.Git.HeadSHA(ctx, full, branchPath); err == nil {
		return "", "", false
	}
	return branch, resource, true
}

// UpdateBranchProtection handles PUT /api/v3/repos/{owner}/{repo}/branches/{branch}/protection
func (d *Deps) UpdateBranchProtection(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	// Wildcard captures everything after /branches/, need to strip /protection suffix
	branchPath := pathParam(r, "*")
	branch, resource, ok := d.resolveBranchProtectionPath(r.Context(), repo.FullName, branchPath)
	if !ok {
		respond.NotFound(w)
		return
	}
	if resource != "" {
		d.putBranchProtectionResource(w, r, *repo, branch, resource)
		return
	}

	// Because the structure is deeply nested and we mostly want to echo it back
	// or persist it verbatim to simulate API compliance, we decode into raw maps.
	var body struct {
		RequiredStatusChecks   interface{} `json:"required_status_checks"`
		EnforceAdmins          interface{} `json:"enforce_admins"`
		RequiredSignatures     interface{} `json:"required_signatures"`
		RequiredPullRequestRev interface{} `json:"required_pull_request_reviews"`
		Restrictions           interface{} `json:"restrictions"`
	}
	if err := decodeBodyStrictOptional(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}

	bp := &db.BranchProtection{
		RepositoryID: repo.ID,
		Repository:   *repo,
		BranchName:   branch,
	}

	if body.RequiredStatusChecks != nil {
		b, _ := json.Marshal(body.RequiredStatusChecks)
		bp.RequiredStatusChecksJSON = string(b)
	}
	if body.RequiredPullRequestRev != nil {
		if err := validateBranchProtectionBypassAllowances(body.RequiredPullRequestRev); err != nil {
			respond.ValidationFailed(w, err.Error())
			return
		}
		b, _ := json.Marshal(body.RequiredPullRequestRev)
		bp.RequiredPullRequestJSON = string(b)
	}
	if body.Restrictions != nil {
		b, _ := json.Marshal(body.Restrictions)
		bp.RestrictionsJSON = string(b)
	}

	// Enforce admins can be a boolean or an object. We simplify to boolean enabled.
	if body.EnforceAdmins != nil {
		switch v := body.EnforceAdmins.(type) {
		case bool:
			bp.EnforceAdmins = v
		case map[string]interface{}:
			if b, ok := v["enabled"].(bool); ok {
				bp.EnforceAdmins = b
			}
		}
	}
	if body.RequiredSignatures != nil {
		switch v := body.RequiredSignatures.(type) {
		case bool:
			bp.RequiredSignatures = v
		case map[string]interface{}:
			if b, ok := v["enabled"].(bool); ok {
				bp.RequiredSignatures = b
			}
		}
	}

	if err := d.Svc.UpdateBranchProtection(r.Context(), bp); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	bp.Repository = *repo
	respond.JSON(w, 200, branchProtectionJSON(*bp))
}

// PostBranchProtection handles POST subresources under
// /api/v3/repos/{owner}/{repo}/branches/{branch}/protection.
func (d *Deps) PostBranchProtection(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	branch, resource, ok := d.resolveBranchProtectionPath(r.Context(), repo.FullName, pathParam(r, "*"))
	if !ok || resource == "" {
		respond.NotFound(w)
		return
	}
	d.postBranchProtectionResource(w, r, *repo, branch, resource)
}

// PatchBranchProtection handles PATCH subresources under
// /api/v3/repos/{owner}/{repo}/branches/{branch}/protection.
func (d *Deps) PatchBranchProtection(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	branch, resource, ok := d.resolveBranchProtectionPath(r.Context(), repo.FullName, pathParam(r, "*"))
	if !ok || resource == "" {
		respond.NotFound(w)
		return
	}
	d.patchBranchProtectionResource(w, r, *repo, branch, resource)
}

// DeleteBranchProtection handles DELETE /api/v3/repos/{owner}/{repo}/branches/{branch}/protection
func (d *Deps) DeleteBranchProtection(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	// Wildcard captures everything after /branches/, need to strip /protection suffix
	branchPath := pathParam(r, "*")
	branch, resource, ok := d.resolveBranchProtectionPath(r.Context(), repo.FullName, branchPath)
	if !ok {
		respond.NotFound(w)
		return
	}
	if resource != "" {
		d.deleteBranchProtectionResource(w, r, *repo, branch, resource)
		return
	}

	if err := d.Svc.DeleteBranchProtection(r.Context(), repo.ID, branch); err != nil {
		respond.NotFound(w) // Already deleted or doesn't exist
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *Deps) getBranchProtectionResource(w http.ResponseWriter, r *http.Request, repo db.Repository, branch, resource string) {
	bp, err := d.Svc.GetBranchProtection(r.Context(), repo.ID, branch)
	if err != nil {
		respond.NotFound(w)
		return
	}
	bp.Repository = repo

	switch resource {
	case "":
		respond.JSON(w, http.StatusOK, branchProtectionJSON(*bp))
	case "required_status_checks":
		item := branchProtectionRequiredStatusChecksJSON(*bp)
		if item == nil {
			respond.NotFound(w)
			return
		}
		respond.JSON(w, http.StatusOK, item)
	case "required_status_checks/contexts":
		status := branchProtectionRequiredStatusChecksJSON(*bp)
		if status == nil {
			respond.NotFound(w)
			return
		}
		respond.JSON(w, http.StatusOK, stringSliceFromAny(status["contexts"]))
	case "enforce_admins":
		respond.JSON(w, http.StatusOK, branchProtectionEnforceAdminsJSON(*bp))
	case "required_signatures":
		respond.JSON(w, http.StatusOK, branchProtectionRequiredSignaturesJSON(*bp))
	case "required_pull_request_reviews":
		item := branchProtectionRequiredPullRequestReviewsJSON(*bp)
		if item == nil {
			respond.NotFound(w)
			return
		}
		respond.JSON(w, http.StatusOK, item)
	case "restrictions":
		item := branchProtectionRestrictionsJSON(*bp)
		if item == nil {
			respond.NotFound(w)
			return
		}
		respond.JSON(w, http.StatusOK, item)
	case "restrictions/users", "restrictions/teams", "restrictions/apps":
		restrict := branchProtectionRestrictionsJSON(*bp)
		if restrict == nil {
			respond.NotFound(w)
			return
		}
		key := strings.TrimPrefix(resource, "restrictions/")
		respond.JSON(w, http.StatusOK, anySliceFromAny(restrict[key]))
	default:
		respond.NotFound(w)
	}
}

func (d *Deps) postBranchProtectionResource(w http.ResponseWriter, r *http.Request, repo db.Repository, branch, resource string) {
	if key, ok := branchProtectionRestrictionResourceKey(resource); ok {
		d.mutateBranchProtectionRestrictionActors(w, r, repo, branch, key, branchProtectionRestrictionAdd)
		return
	}

	switch resource {
	case "required_signatures":
		bp, ok := d.loadBranchProtectionForMutation(w, r, repo, branch, false)
		if !ok {
			return
		}
		bp.RequiredSignatures = true
		if !d.saveBranchProtection(w, r, &bp) {
			return
		}
		respond.JSON(w, http.StatusOK, branchProtectionRequiredSignaturesJSON(bp))
	default:
		respond.NotFound(w)
	}
}

func (d *Deps) putBranchProtectionResource(w http.ResponseWriter, r *http.Request, repo db.Repository, branch, resource string) {
	if key, ok := branchProtectionRestrictionResourceKey(resource); ok {
		d.mutateBranchProtectionRestrictionActors(w, r, repo, branch, key, branchProtectionRestrictionSet)
		return
	}

	switch resource {
	case "enforce_admins":
		bp, ok := d.loadBranchProtectionForMutation(w, r, repo, branch, true)
		if !ok {
			return
		}
		bp.EnforceAdmins = true
		if !d.saveBranchProtection(w, r, &bp) {
			return
		}
		respond.JSON(w, http.StatusOK, branchProtectionEnforceAdminsJSON(bp))
	case "required_status_checks/contexts":
		var body struct {
			Contexts []string `json:"contexts"`
		}
		if err := decodeBodyStrictOptional(r, &body); err != nil {
			respond.ValidationFailed(w, "invalid body")
			return
		}
		bp, ok := d.loadBranchProtectionForMutation(w, r, repo, branch, true)
		if !ok {
			return
		}
		status := branchProtectionDecodeMap(bp.RequiredStatusChecksJSON, "RequiredStatusChecksJSON", branch)
		if status == nil {
			status = map[string]any{}
		}
		status["contexts"] = body.Contexts
		bp.RequiredStatusChecksJSON = mustMarshalBranchProtectionMap(status)
		if !d.saveBranchProtection(w, r, &bp) {
			return
		}
		respond.JSON(w, http.StatusOK, body.Contexts)
	default:
		respond.NotFound(w)
	}
}

func (d *Deps) patchBranchProtectionResource(w http.ResponseWriter, r *http.Request, repo db.Repository, branch, resource string) {
	var body map[string]any
	if err := decodeBodyStrictOptional(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}

	switch resource {
	case "required_status_checks":
		bp, ok := d.loadBranchProtectionForMutation(w, r, repo, branch, true)
		if !ok {
			return
		}
		status := branchProtectionDecodeMap(bp.RequiredStatusChecksJSON, "RequiredStatusChecksJSON", branch)
		if status == nil {
			status = map[string]any{}
		}
		mergeBranchProtectionMap(status, body)
		bp.RequiredStatusChecksJSON = mustMarshalBranchProtectionMap(status)
		if !d.saveBranchProtection(w, r, &bp) {
			return
		}
		respond.JSON(w, http.StatusOK, branchProtectionRequiredStatusChecksJSON(bp))
	case "required_pull_request_reviews":
		if err := validateBranchProtectionBypassAllowances(body); err != nil {
			respond.ValidationFailed(w, err.Error())
			return
		}
		bp, ok := d.loadBranchProtectionForMutation(w, r, repo, branch, true)
		if !ok {
			return
		}
		reviews := branchProtectionDecodeMap(bp.RequiredPullRequestJSON, "RequiredPullRequestJSON", branch)
		if reviews == nil {
			reviews = map[string]any{}
		}
		mergeBranchProtectionMap(reviews, body)
		bp.RequiredPullRequestJSON = mustMarshalBranchProtectionMap(reviews)
		if !d.saveBranchProtection(w, r, &bp) {
			return
		}
		respond.JSON(w, http.StatusOK, branchProtectionRequiredPullRequestReviewsJSON(bp))
	default:
		respond.NotFound(w)
	}
}

func (d *Deps) deleteBranchProtectionResource(w http.ResponseWriter, r *http.Request, repo db.Repository, branch, resource string) {
	bp, ok := d.loadBranchProtectionForMutation(w, r, repo, branch, false)
	if !ok {
		return
	}

	switch resource {
	case "required_status_checks":
		bp.RequiredStatusChecksJSON = ""
		if !d.saveBranchProtection(w, r, &bp) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "required_status_checks/contexts":
		status := branchProtectionDecodeMap(bp.RequiredStatusChecksJSON, "RequiredStatusChecksJSON", branch)
		if status == nil {
			respond.NotFound(w)
			return
		}
		remaining, ok := deleteBranchProtectionContexts(r, stringSliceFromAny(status["contexts"]))
		if !ok {
			respond.ValidationFailed(w, "invalid body")
			return
		}
		status["contexts"] = remaining
		bp.RequiredStatusChecksJSON = mustMarshalBranchProtectionMap(status)
		if !d.saveBranchProtection(w, r, &bp) {
			return
		}
		respond.JSON(w, http.StatusOK, remaining)
	case "enforce_admins":
		bp.EnforceAdmins = false
		if !d.saveBranchProtection(w, r, &bp) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "required_signatures":
		bp.RequiredSignatures = false
		if !d.saveBranchProtection(w, r, &bp) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "required_pull_request_reviews":
		bp.RequiredPullRequestJSON = ""
		if !d.saveBranchProtection(w, r, &bp) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "restrictions":
		bp.RestrictionsJSON = ""
		if !d.saveBranchProtection(w, r, &bp) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "restrictions/users", "restrictions/teams", "restrictions/apps":
		key, _ := branchProtectionRestrictionResourceKey(resource)
		d.mutateLoadedBranchProtectionRestrictionActors(w, r, &bp, branch, key, branchProtectionRestrictionRemove)
	default:
		respond.NotFound(w)
	}
}

type branchProtectionRestrictionMutation int

const (
	branchProtectionRestrictionAdd branchProtectionRestrictionMutation = iota
	branchProtectionRestrictionSet
	branchProtectionRestrictionRemove
)

func branchProtectionRestrictionResourceKey(resource string) (string, bool) {
	switch resource {
	case "restrictions/users":
		return "users", true
	case "restrictions/teams":
		return "teams", true
	case "restrictions/apps":
		return "apps", true
	default:
		return "", false
	}
}

func (d *Deps) mutateBranchProtectionRestrictionActors(w http.ResponseWriter, r *http.Request, repo db.Repository, branch, key string, mutation branchProtectionRestrictionMutation) {
	bp, ok := d.loadBranchProtectionForMutation(w, r, repo, branch, false)
	if !ok {
		return
	}
	d.mutateLoadedBranchProtectionRestrictionActors(w, r, &bp, branch, key, mutation)
}

func (d *Deps) mutateLoadedBranchProtectionRestrictionActors(w http.ResponseWriter, r *http.Request, bp *db.BranchProtection, branch, key string, mutation branchProtectionRestrictionMutation) {
	actors, ok := decodeBranchProtectionRestrictionActors(r, key)
	if !ok {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	restrict := branchProtectionDecodeMap(bp.RestrictionsJSON, "RestrictionsJSON", branch)
	if restrict == nil {
		restrict = map[string]any{}
	}
	existing := stringSliceFromAny(restrict[key])
	var updated []string
	switch mutation {
	case branchProtectionRestrictionAdd:
		updated = appendUniqueBranchProtectionStrings(existing, actors)
	case branchProtectionRestrictionSet:
		updated = uniqueBranchProtectionStrings(actors)
	case branchProtectionRestrictionRemove:
		updated = removeBranchProtectionStrings(existing, actors)
	default:
		respond.NotFound(w)
		return
	}
	restrict[key] = updated
	bp.RestrictionsJSON = mustMarshalBranchProtectionMap(restrict)
	if !d.saveBranchProtection(w, r, bp) {
		return
	}
	respond.JSON(w, http.StatusOK, updated)
}

func (d *Deps) loadBranchProtectionForMutation(w http.ResponseWriter, r *http.Request, repo db.Repository, branch string, createIfMissing bool) (db.BranchProtection, bool) {
	bp, err := d.Svc.GetBranchProtection(r.Context(), repo.ID, branch)
	if err == nil {
		bp.Repository = repo
		return *bp, true
	}
	if errors.Is(err, service.ErrNotFound) && createIfMissing {
		return db.BranchProtection{RepositoryID: repo.ID, Repository: repo, BranchName: branch}, true
	}
	respond.NotFound(w)
	return db.BranchProtection{}, false
}

func (d *Deps) saveBranchProtection(w http.ResponseWriter, r *http.Request, bp *db.BranchProtection) bool {
	if err := d.Svc.UpdateBranchProtection(r.Context(), bp); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return false
	}
	return true
}

func mergeBranchProtectionMap(dst, src map[string]any) {
	for key, value := range src {
		dst[key] = value
	}
}

func mustMarshalBranchProtectionMap(value map[string]any) string {
	if len(value) == 0 {
		return ""
	}
	out, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(out)
}

func stringSliceFromAny(raw any) []string {
	items := anySliceFromAny(raw)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok {
			out = append(out, value)
		}
	}
	return out
}

func anySliceFromAny(raw any) []any {
	switch values := raw.(type) {
	case []any:
		return values
	case []string:
		out := make([]any, 0, len(values))
		for _, value := range values {
			out = append(out, value)
		}
		return out
	default:
		return []any{}
	}
}

func deleteBranchProtectionContexts(r *http.Request, existing []string) ([]string, bool) {
	var body struct {
		Contexts []string `json:"contexts"`
	}
	if err := decodeBodyStrictOptional(r, &body); err != nil {
		return nil, false
	}
	if len(body.Contexts) == 0 {
		return []string{}, true
	}
	remove := make(map[string]struct{}, len(body.Contexts))
	for _, context := range body.Contexts {
		remove[context] = struct{}{}
	}
	remaining := make([]string, 0, len(existing))
	for _, context := range existing {
		if _, ok := remove[context]; !ok {
			remaining = append(remaining, context)
		}
	}
	return remaining, true
}

func decodeBranchProtectionRestrictionActors(r *http.Request, key string) ([]string, bool) {
	var body map[string][]string
	if err := decodeBodyStrictOptional(r, &body); err != nil {
		return nil, false
	}
	if body == nil {
		return nil, false
	}
	actors, ok := body[key]
	if !ok {
		return nil, false
	}
	return actors, true
}

func appendUniqueBranchProtectionStrings(existing, add []string) []string {
	out := uniqueBranchProtectionStrings(existing)
	seen := make(map[string]struct{}, len(out)+len(add))
	for _, value := range out {
		seen[value] = struct{}{}
	}
	for _, value := range add {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func uniqueBranchProtectionStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func removeBranchProtectionStrings(existing, remove []string) []string {
	if len(remove) == 0 {
		return uniqueBranchProtectionStrings(existing)
	}
	removeSet := make(map[string]struct{}, len(remove))
	for _, value := range remove {
		removeSet[value] = struct{}{}
	}
	out := make([]string, 0, len(existing))
	for _, value := range existing {
		if _, ok := removeSet[value]; ok {
			continue
		}
		out = append(out, value)
	}
	return out
}

func validateBranchProtectionBypassAllowances(raw any) error {
	reqPR, ok := raw.(map[string]any)
	if !ok || reqPR == nil {
		return nil
	}
	bypass, ok := reqPR["bypass_pull_request_allowances"]
	if !ok || bypass == nil {
		return nil
	}
	bypassMap, ok := bypass.(map[string]any)
	if !ok {
		return errors.New("required_pull_request_reviews.bypass_pull_request_allowances must be an object")
	}
	if hasNonEmptyJSONArray(bypassMap["teams"]) {
		return errors.New("required_pull_request_reviews.bypass_pull_request_allowances.teams is not supported")
	}
	if hasNonEmptyJSONArray(bypassMap["apps"]) {
		return errors.New("required_pull_request_reviews.bypass_pull_request_allowances.apps is not supported")
	}
	return nil
}

func hasNonEmptyJSONArray(raw any) bool {
	values, ok := raw.([]any)
	return ok && len(values) > 0
}
