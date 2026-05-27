package rest

import (
	"encoding/json"
	"net/http"
	"strconv"
)

var restOpenAPISpec = mustBuildRESTOpenAPISpec()

// OpenAPISpecBytes returns the published REST extension OpenAPI artifact.
func OpenAPISpecBytes() []byte {
	return append([]byte(nil), restOpenAPISpec...)
}

// GetOpenAPI handles GET /api/v3/openapi.json.
func (d *Deps) GetOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(restOpenAPISpec)
}

func mustBuildRESTOpenAPISpec() []byte {
	body := map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "agent-git-service REST Extensions",
			"version":     "1.0.0",
			"description": "Machine-readable contract for local REST extensions and major GitHub-compatibility deltas implemented by agent-git-service.",
		},
		"servers": []map[string]any{{"url": "/"}},
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"tokenAuth": map[string]any{
					"type":        "apiKey",
					"in":          "header",
					"name":        "Authorization",
					"description": "Use the GitHub-compatible Authorization header format: `token <access-token>`.",
				},
			},
		},
		"x-agent-git-service": map[string]any{
			"compatibility_deltas": []map[string]any{
				{
					"id":       "issues-list-omits-body",
					"path":     "/api/v3/repos/{owner}/{repo}/issues",
					"summary":  "List issues omits body content in REST list responses.",
					"evidence": "internal/service/issue.go: ListIssuesForREST returns issues for the REST list endpoint while omitting the body payload.",
				},
				{
					"id":       "branch-protection-monolithic",
					"path":     "/api/v3/repos/{owner}/{repo}/branches/{branch}/protection",
					"summary":  "Branch protection supports a monolithic protection document plus selected GitHub-style subresources, but not the full branch-protection subresource tree.",
					"evidence": "internal/router/router.go routes branch protection through wildcard branch handlers; internal/rest/handlers_branch.go dispatches selected subresources.",
				},
				{
					"id":       "api-root-advertises-openapi",
					"path":     "/api/v3",
					"summary":  "The API root includes an openapi_url pointer so clients can discover the extension contract.",
					"evidence": "internal/rest/handlers.go GetMeta adds openapi_url.",
				},
			},
		},
		"paths": buildRESTOpenAPIPaths(),
	}
	out, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		panic(err)
	}
	return out
}

func buildRESTOpenAPIPaths() map[string]any {
	return map[string]any{
		"/api/v3/openapi.json": map[string]any{
			"get": operation("getRESTOpenAPISpec", "Get the published OpenAPI contract for local REST extensions.", nil, nil, nil, response(200, "OpenAPI document")),
		},
		"/api/v3/agents": map[string]any{
			"post": operation("createAgent", "Register a new agent identity.", nil, jsonBody(true, map[string]any{
				"prefix_login":      stringSchema("Optional login prefix for the created agent account."),
				"default_repo_name": stringSchema("Optional default repository name for the created agent."),
			}, nil), nil, response(201, "Agent created")),
		},
		"/api/v3/agent-invites": map[string]any{
			"post": operation("createAgentInvite", "Create an invite token used to bind an agent to a user.", auth(), jsonBody(false, map[string]any{
				"repo_grants": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"repo_full_name": stringSchema("Repository full name to grant during bind."), "permission": stringSchema("Requested permission (read/write/admin).")}}},
				"team_grants": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"org": stringSchema("Organization login for a team grant."), "team_slug": stringSchema("Team slug to grant during bind."), "role": stringSchema("Team role (member/maintainer).")}}},
			}, nil), nil, response(201, "Agent invite created")),
		},
		"/api/v3/agent-bindings/confirm": map[string]any{
			"post": operation("confirmAgentBinding", "Confirm an agent binding using an invite token.", auth(), jsonBody(true, map[string]any{
				"invite_token": stringSchema("Invite token issued by POST /api/v3/agent-invites."),
			}, []string{"invite_token"}), nil, response(200, "Binding confirmed")),
		},
		"/api/v3/agent-bindings/{agent_login}": map[string]any{
			"patch": operation("renameBoundAgent", "Rename a bound agent's display name.", auth(), jsonBody(true, map[string]any{
				"name": stringSchema("New display name for the bound agent."),
			}, []string{"name"}), pathParams(param("agent_login", "string")), response(200, "Agent renamed")),
		},
		"/api/v3/agent-bindings/{agent_login}/reset-token": map[string]any{
			"post": operation("resetAgentToken", "Rotate the token for a bound agent login.", auth(), nil, pathParams(param("agent_login", "string")), response(200, "Token rotated")),
		},
		"/api/v3/agent-bindings/{agent_login}/switch-session": map[string]any{
			"post": operation("switchAgentSession", "Create a temporary console session for a bound agent without rotating its existing tokens.", auth(), nil, pathParams(param("agent_login", "string")), response(200, "Switch session created")),
		},
		"/api/v3/agent-bindings/{agent_login}/refresh-session": map[string]any{
			"post": operation("refreshAgentSwitchSession", "Refresh an active bound-agent switch session before it expires.", auth(), nil, pathParams(param("agent_login", "string")), response(200, "Switch session refreshed")),
		},
		"/api/v3/auth0/device/code": map[string]any{
			"post": operation("createAuth0DeviceCode", "Start an Auth0 device-code login flow.", nil, nil, nil, response(200, "Device code issued")),
		},
		"/api/v3/oidc/device/code": map[string]any{
			"post": operation("createOIDCDeviceCode", "Start a generic OIDC device-code login flow.", nil, nil, nil, response(200, "Device code issued")),
		},
		"/api/v3/auth0/session": map[string]any{
			"post": operation("exchangeAuth0Session", "Exchange Auth0 session data for a local session.", nil, jsonBody(true, map[string]any{
				"device_code": stringSchema("Auth0 device code previously issued to the client."),
			}, []string{"device_code"}), nil, response(200, "Session established")),
		},
		"/api/v3/oidc/session": map[string]any{
			"post": operation("exchangeOIDCSession", "Exchange generic OIDC session data for a local session.", nil, jsonBody(true, map[string]any{
				"device_code": stringSchema("OIDC device code previously issued to the client."),
			}, []string{"device_code"}), nil, response(200, "Session established")),
		},
		"/api/v3/auth0/callback": map[string]any{
			"post": operation("handleAuth0Callback", "Handle the Auth0 callback payload.", nil, jsonBody(true, map[string]any{
				"id_token": stringSchema("Auth0 ID token returned from the login redirect flow."),
			}, []string{"id_token"}), nil, response(200, "Callback processed")),
		},
		"/api/v3/oidc/callback": map[string]any{
			"post": operation("handleOIDCCallback", "Handle the generic OIDC callback payload.", nil, jsonBody(true, map[string]any{
				"id_token": stringSchema("OIDC ID token returned from the login redirect flow."),
			}, []string{"id_token"}), nil, response(200, "Callback processed")),
		},
		"/api/v3/auth0/lookup": map[string]any{
			"post": operation("lookupAuth0Identity", "Resolve an Auth0 identity to a local user.", nil, jsonBody(true, map[string]any{
				"id_token": stringSchema("Auth0 ID token to validate and map to a local user."),
			}, []string{"id_token"}), nil, response(200, "Identity resolved")),
		},
		"/api/v3/oidc/lookup": map[string]any{
			"post": operation("lookupOIDCIdentity", "Resolve a generic OIDC identity to a local user.", nil, jsonBody(true, map[string]any{
				"id_token": stringSchema("OIDC ID token to validate and map to a local user."),
			}, []string{"id_token"}), nil, response(200, "Identity resolved")),
		},
		"/api/v3/presence/heartbeat": map[string]any{
			"post": operation("postPresenceHeartbeat", "Publish a presence heartbeat for the authenticated user.", auth(), jsonBody(true, map[string]any{
				"issue_id": map[string]any{"type": "integer", "minimum": 1},
			}, []string{"issue_id"}), nil, response(204, "Heartbeat accepted")),
		},
		"/api/v3/issues/{id}/typing": map[string]any{
			"get":  operation("subscribeIssueTyping", "Subscribe to issue typing events.", auth(), nil, pathParams(param("id", "integer")), response(200, "Typing stream opened")),
			"post": operation("signalIssueTyping", "Publish typing state for an issue.", auth(), nil, pathParams(param("id", "integer")), response(204, "Typing state accepted")),
		},
		"/api/v3/issues/{id}/attachments": map[string]any{
			"get": operation("listIssueAttachments", "List issue attachments by issue id.", nil, nil, pathParams(param("id", "integer")), response(200, "Attachment list returned")),
			"post": operation("uploadIssueAttachment", "Upload a new attachment to an issue.", auth(), multipartBody(true, map[string]any{
				"file": map[string]any{"type": "string", "format": "binary"},
			}, []string{"file"}), pathParams(param("id", "integer")), response(201, "Attachment uploaded")),
		},
		"/api/v3/repos/{owner}/{repo}/attachments": map[string]any{
			"post": operation("uploadRepoAttachment", "Upload a new repository-scoped attachment.", auth(), multipartBody(true, map[string]any{
				"file": map[string]any{"type": "string", "format": "binary"},
			}, []string{"file"}), pathParams(param("owner", "string"), param("repo", "string")), response(201, "Attachment uploaded")),
		},
		"/api/v3/repositories/{repo_id}/attachments": map[string]any{
			"post": operation("uploadRepoAttachmentByID", "Upload a new repository-scoped attachment by repository id.", auth(), multipartBody(true, map[string]any{
				"file": map[string]any{"type": "string", "format": "binary"},
			}, []string{"file"}), pathParams(param("repo_id", "integer")), response(201, "Attachment uploaded")),
		},
		"/api/v3/issues/{issue_id}/presence": map[string]any{
			"get": operation("getIssuePresence", "Get presence state for an issue.", auth(), nil, pathParams(param("issue_id", "integer")), response(200, "Presence returned")),
		},
		"/api/v3/attachments/{uuid}": map[string]any{
			"get":    operation("downloadIssueAttachment", "Download an issue attachment by uuid.", nil, nil, pathParams(param("uuid", "string")), response(200, "Attachment content returned")),
			"delete": operation("deleteIssueAttachment", "Delete an issue attachment by uuid.", auth(), nil, pathParams(param("uuid", "string")), response(204, "Attachment deleted")),
		},
		"/api/v3/users/{user_id}/last-seen": map[string]any{
			"get": operation("getUserLastSeen", "Get the last-seen timestamp for a user.", auth(), nil, pathParams(param("user_id", "integer")), response(200, "Last-seen state returned")),
		},
		"/api/v3/user/agents": map[string]any{
			"get": operation("listBoundAgents", "List agents bound to the authenticated user.", auth(), nil, nil, response(200, "Bound agents returned")),
		},
		"/api/v3/user/presence/privacy": map[string]any{
			"get": operation("getPresencePrivacy", "Get the authenticated user's presence privacy setting.", auth(), nil, nil, response(200, "Privacy returned")),
			"put": operation("putPresencePrivacy", "Update the authenticated user's presence privacy setting.", auth(), jsonBody(true, map[string]any{
				"hide": map[string]any{"type": "boolean"},
			}, []string{"hide"}), nil, response(200, "Privacy updated")),
		},
		"/api/v3/user/tokens": map[string]any{
			"get": operation("listTokens", "List local user tokens.", auth(), nil, nil, response(200, "Tokens returned")),
			"post": operation("createToken", "Create a local user token.", auth(), jsonBody(true, map[string]any{
				"name":       stringSchema("Optional token display name."),
				"expires_at": map[string]any{"type": "string", "format": "date-time", "description": "Optional RFC3339 expiration timestamp."},
			}, nil), nil, response(201, "Token created")),
			"delete": operation("deleteToken", "Delete a local user token.", auth(), jsonBody(true, map[string]any{
				"id":       map[string]any{"type": "integer", "minimum": 1},
				"token_id": map[string]any{"type": "integer", "minimum": 1},
				"token":    stringSchema("Raw token value when deleting by value instead of numeric id."),
			}, nil), nil, response(204, "Token deleted")),
		},
		"/api/v3/repos/{owner}/{repo}/team-sharing/enable": map[string]any{
			"post": operation("enableRepoTeamSharing", "Enable team-based sharing for a repository.", auth(), nil, pathParams(
				param("owner", "string"),
				param("repo", "string"),
			), response(200, "Team sharing enabled")),
		},
		"/api/v3/repos/{owner}/{repo}/wiki/pages": map[string]any{
			"get": operation("listWikiPages", "List wiki pages for a repository.", nil, nil, append(pathParams(
				param("owner", "string"),
				param("repo", "string"),
			), queryParams(
				param("path", "string"),
				param("recursive", "boolean"),
				param("label", "string"),
				param("labels", "string"),
				param("exclude_label", "string"),
				param("exclude_labels", "string"),
			)...), response(200, "Wiki pages returned")),
		},
		"/api/v3/repos/{owner}/{repo}/wiki/search": map[string]any{
			"get": operation("searchWikiPages", "Search wiki pages for a repository, with optional label filters.", nil, nil, append(pathParams(
				param("owner", "string"),
				param("repo", "string"),
			), queryParams(
				param("q", "string"),
				param("limit", "integer"),
				param("offset", "integer"),
				param("label", "string"),
				param("labels", "string"),
				param("exclude_label", "string"),
				param("exclude_labels", "string"),
			)...), response(200, "Wiki search results returned")),
		},
		"/api/v3/repos/{owner}/{repo}/wiki-v2/state": map[string]any{
			"get": operation("getWikiV2State", "Get the provisional Wiki V2 derived-index state for a repository.", auth(), nil, pathParams(
				param("owner", "string"),
				param("repo", "string"),
			), response(200, "Current Wiki V2 state")),
		},
		"/api/v3/repos/{owner}/{repo}/wiki-v2/reconcile/request": map[string]any{
			"post": operation("requestWikiV2Reconcile", "Request a provisional Wiki V2 reconcile without running it synchronously.", auth(), nil, pathParams(
				param("owner", "string"),
				param("repo", "string"),
			), response(202, "Reconcile request recorded")),
		},
		"/api/v3/repos/{owner}/{repo}/wiki-v2/reconcile": map[string]any{
			"post": operation("reconcileWikiV2", "Run the provisional Wiki V2 reconcile synchronously and return the persisted result.", auth(), nil, pathParams(
				param("owner", "string"),
				param("repo", "string"),
			), response(200, "Reconcile completed")),
		},
		"/api/v3/repos/{owner}/{repo}/wiki/move": map[string]any{
			"post": operation("moveWikiPagePrefix", "Atomically move all wiki pages under one slug prefix to another prefix.", auth(), jsonBody(true, map[string]any{
				"from":     stringSchema("Source wiki slug prefix to move."),
				"to":       stringSchema("Destination wiki slug prefix."),
				"message":  stringSchema("Optional commit message recorded for the wiki move."),
				"if_match": mapSchema("Latest blob SHAs keyed by source wiki slug."),
			}, []string{"from", "to", "if_match"}), pathParams(
				param("owner", "string"),
				param("repo", "string"),
			), response(200, "Wiki pages moved")),
		},
		"/api/v3/repos/{owner}/{repo}/wiki/pages/{slug}": map[string]any{
			"get": operation("getWikiPage", "Get a wiki page by slug, optionally at a full commit SHA from that page's history.", nil, nil, append(pathParams(
				param("owner", "string"),
				param("repo", "string"),
				wikiSlugParamSpec(),
			), queryParams(
				map[string]any{
					"name":        "ref",
					"in":          "query",
					"schema":      map[string]any{"type": "string", "pattern": "^[0-9a-fA-F]{40}$"},
					"description": "Optional full commit SHA from the requested page's history.",
				},
			)...), response(200, "Wiki page returned")),
			"put": operation("putWikiPage", "Create or replace a wiki page by slug.", auth(), jsonBody(true, map[string]any{
				"body":    stringSchema("Markdown body for the wiki page."),
				"message": stringSchema("Optional commit message recorded for the wiki update."),
				"sha":     stringSchema("Optional blob SHA precondition for optimistic concurrency control."),
			}, []string{"body"}), pathParams(
				param("owner", "string"),
				param("repo", "string"),
				wikiSlugParamSpec(),
			), response(200, "Wiki page written")),
			"delete": operation("deleteWikiPage", "Delete a wiki page by slug.", auth(), jsonBody(false, map[string]any{
				"message": stringSchema("Optional commit message recorded for the wiki deletion."),
			}, nil), pathParams(
				param("owner", "string"),
				param("repo", "string"),
				wikiSlugParamSpec(),
			), response(204, "Wiki page deleted")),
		},
		"/api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/move": map[string]any{
			"post": operation("moveWikiPage", "Move a wiki page to a new slug and rewrite eligible inbound wiki references in the same commit.", auth(), jsonBody(true, map[string]any{
				"new_slug": stringSchema("Destination slug for the wiki page."),
				"message":  stringSchema("Optional commit message recorded for the wiki move."),
				"if_match": stringSchema("Latest source page commit SHA observed by the client."),
			}, []string{"new_slug", "if_match"}), pathParams(
				param("owner", "string"),
				param("repo", "string"),
				wikiSlugParamSpec(),
			), response(200, "Wiki page moved and inbound references rewritten")),
		},
		"/api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/labels": map[string]any{
			"get": operation("listWikiPageLabels", "List labels attached to a wiki page.", nil, nil, pathParams(
				param("owner", "string"),
				param("repo", "string"),
				wikiSlugParamSpec(),
			), response(200, "Wiki page labels returned")),
			"post": operation("addWikiPageLabels", "Add labels to a wiki page.", auth(), jsonBody(true, map[string]any{
				"labels": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Repository label names to attach."},
			}, []string{"labels"}), pathParams(
				param("owner", "string"),
				param("repo", "string"),
				wikiSlugParamSpec(),
			), response(200, "Wiki page labels returned")),
			"put": operation("setWikiPageLabels", "Replace labels attached to a wiki page.", auth(), jsonBody(true, map[string]any{
				"labels": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Repository label names that should remain attached."},
			}, []string{"labels"}), pathParams(
				param("owner", "string"),
				param("repo", "string"),
				wikiSlugParamSpec(),
			), response(200, "Wiki page labels returned")),
			"delete": operation("removeAllWikiPageLabels", "Remove all labels from a wiki page.", auth(), nil, pathParams(
				param("owner", "string"),
				param("repo", "string"),
				wikiSlugParamSpec(),
			), response(204, "Wiki page labels removed")),
		},
		"/api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/labels/{name}": map[string]any{
			"delete": operation("removeWikiPageLabel", "Remove one label from a wiki page.", auth(), nil, pathParams(
				param("owner", "string"),
				param("repo", "string"),
				wikiSlugParamSpec(),
				param("name", "string"),
			), response(200, "Remaining wiki page labels returned")),
		},
		"/api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/history": map[string]any{
			"get": operation("listWikiPageHistory", "List paginated revision history for a wiki page slug.", nil, nil, append(pathParams(
				param("owner", "string"),
				param("repo", "string"),
				wikiSlugParamSpec(),
			), queryParams(
				param("page", "integer"),
				param("per_page", "integer"),
			)...), response(200, "Wiki page history returned")),
		},
		"/api/v3/repos/{owner}/{repo}/wiki/compact": map[string]any{
			"post": operation("compactWikiHistory", "Temporarily disabled while the wiki catalog corruption incident is being contained and repaired.", auth(), jsonBody(false, map[string]any{
				"before": stringSchema("Reserved for future bounded compaction support. Currently rejected when non-empty."),
			}, nil), pathParams(
				param("owner", "string"),
				param("repo", "string"),
			), response(409, "Wiki history compaction is temporarily disabled")),
		},
		"/api/v3/repos/{owner}/{repo}/wiki/compact/{jobID}": map[string]any{
			"get": operation("getWikiCompactionJob", "Get the current status for an async wiki history compaction job.", auth(), nil, pathParams(
				param("owner", "string"),
				param("repo", "string"),
				param("jobID", "string"),
			), response(200, "Wiki history compaction job returned")),
		},
		"/api/v3/admin/wiki/repos/{owner}/{repo}/repair-locks": map[string]any{
			"post": operation("repairWikiLocks", "Inspect and clear stale wiki branch lock files for one repository.", auth(), jsonBody(false, map[string]any{
				"force": map[string]any{
					"type":        "boolean",
					"description": "When true, clear the lock even if it is still fresh.",
				},
			}, nil), pathParams(
				param("owner", "string"),
				param("repo", "string"),
			), response(200, "Wiki lock repair result returned")),
		},
		"/api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/backlinks": map[string]any{
			"get": operation("listWikiBacklinks", "List inbound wiki links for a page slug.", nil, nil, pathParams(
				param("owner", "string"),
				param("repo", "string"),
				wikiSlugParamSpec(),
			), response(200, "Wiki backlinks returned")),
		},
	}
}

func operation(id, summary string, security []map[string][]string, requestBody map[string]any, params []map[string]any, responses map[string]any) map[string]any {
	op := map[string]any{
		"operationId": id,
		"summary":     summary,
		"responses":   responses,
	}
	if len(security) > 0 {
		op["security"] = security
	}
	if requestBody != nil {
		op["requestBody"] = requestBody
	}
	if len(params) > 0 {
		op["parameters"] = params
	}
	return op
}

func auth() []map[string][]string {
	return []map[string][]string{{"tokenAuth": []string{}}}
}

func pathParams(params ...map[string]any) []map[string]any {
	for _, param := range params {
		param["in"] = "path"
		param["required"] = true
	}
	return params
}

func queryParams(params ...map[string]any) []map[string]any {
	for _, param := range params {
		param["in"] = "query"
	}
	return params
}

func param(name, typ string) map[string]any {
	schema := map[string]any{"type": typ}
	if typ == "integer" {
		schema["minimum"] = 1
	}
	return map[string]any{
		"name":   name,
		"schema": schema,
	}
}

func wikiSlugParamSpec() map[string]any {
	p := param("slug", "string")
	p["description"] = "Wiki page slug as one path parameter. Encode nested slug separators as %2F, for example guides%2Fsetup."
	return p
}

func stringSchema(description string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": description,
	}
}

func mapSchema(description string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"description":          description,
		"additionalProperties": map[string]any{"type": "string"},
	}
}

func jsonBody(required bool, properties map[string]any, requiredProps []string) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(requiredProps) > 0 {
		schema["required"] = requiredProps
	}
	return map[string]any{
		"required": required,
		"content": map[string]any{
			"application/json": map[string]any{
				"schema": schema,
			},
		},
	}
}

func multipartBody(required bool, properties map[string]any, requiredProps []string) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(requiredProps) > 0 {
		schema["required"] = requiredProps
	}
	return map[string]any{
		"required": required,
		"content": map[string]any{
			"multipart/form-data": map[string]any{
				"schema": schema,
			},
		},
	}
}

func response(code int, description string) map[string]any {
	return map[string]any{
		strconv.Itoa(code): map[string]any{
			"description": description,
		},
	}
}
