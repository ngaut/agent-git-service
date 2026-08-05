package rest

import (
	"encoding/json"
	"net/http"
	"strconv"
)

var extensionRestOpenAPISpec = mustBuildExtensionRestOpenAPISpec()
var githubCompatibleOpenAPISpec = mustBuildGitHubCompatibleOpenAPISpec()

const extensionOpenAPIPrefix = "/api/ext/v1"
const githubCompatibleOpenAPIPrefix = "/api/v3"

// GetGitHubCompatibleOpenAPI handles GET /api/v3/openapi.json.
func (d *Deps) GetGitHubCompatibleOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(githubCompatibleOpenAPISpec)
}

// GetOpenAPI handles GET /api/ext/v1/openapi.json.
func (d *Deps) GetOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(extensionRestOpenAPISpec)
}

func mustBuildExtensionRestOpenAPISpec() []byte {
	body := map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "agent-git-service Extension REST API",
			"version":     "1.0.0",
			"description": "Machine-readable contract for extension REST APIs under /api/ext/v1.",
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
			"api_surface": map[string]any{
				"extension_prefix":         extensionOpenAPIPrefix,
				"github_compatible_prefix": githubCompatibleOpenAPIPrefix,
			},
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
					"id":       "extension-canonical-prefix",
					"path":     "/api/ext/v1/openapi.json",
					"summary":  "Extension APIs are published only under /api/ext/v1; /api/v3 is reserved for GitHub-compatible routes.",
					"evidence": "internal/router/router.go registers extension routes on /api/ext/v1.",
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

func mustBuildGitHubCompatibleOpenAPISpec() []byte {
	body := map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "agent-git-service GitHub-Compatible REST API",
			"version":     "1.0.0",
			"description": "Machine-readable local compatibility contract for GitHub-shaped REST APIs under /api/v3. This document describes local compatibility behavior and is not a claim of strict GitHub.com parity.",
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
			"api_surface": map[string]any{
				"github_compatible_prefix": githubCompatibleOpenAPIPrefix,
				"extension_prefix":         extensionOpenAPIPrefix,
			},
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
			},
			"route_families": []string{
				"discovery",
				"current user",
				"repositories",
				"issues",
				"pull requests",
				"branches and commits",
				"contents and Git database",
				"organizations and teams",
				"releases",
				"notifications",
				"search",
			},
		},
		"paths": buildGitHubCompatibleOpenAPIPaths(),
	}
	out, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		panic(err)
	}
	return out
}

func buildGitHubCompatibleOpenAPIPaths() map[string]any {
	return map[string]any{
		"/api/v3": map[string]any{
			"get": operation("getGitHubCompatibleAPIDiscovery", "Get GitHub-compatible REST discovery links.", nil, nil, nil, response(200, "API discovery document")),
		},
		"/api/v3/": map[string]any{
			"get": operation("getGitHubCompatibleAPIDiscoveryTrailingSlash", "Get GitHub-compatible REST discovery links.", nil, nil, nil, response(200, "API discovery document")),
		},
		"/api/v3/openapi.json": map[string]any{
			"get": operation("getGitHubCompatibleOpenAPISpec", "Get the published OpenAPI compatibility contract for GitHub-shaped REST APIs.", nil, nil, nil, response(200, "OpenAPI document")),
		},
		"/api/v3/meta": map[string]any{
			"get": operation("getServerMeta", "Get GitHub-compatible server metadata.", nil, nil, nil, response(200, "Server metadata returned")),
		},
		"/api/v3/rate_limit": map[string]any{
			"get": operation("getRateLimit", "Get GitHub-compatible local rate-limit state.", nil, nil, nil, response(200, "Rate-limit state returned")),
		},
		"/api/v3/user": map[string]any{
			"get": operation("getAuthenticatedUser", "Get the authenticated user.", auth(), nil, nil, response(200, "Authenticated user returned")),
		},
		"/api/v3/user/orgs": map[string]any{
			"get": operation("listUserOrgs", "List organizations for the authenticated user.", auth(), nil, nil, response(200, "Organization list returned")),
		},
		"/api/v3/user/repos": map[string]any{
			"get":  operation("listUserRepos", "List repositories for the authenticated user.", auth(), nil, nil, response(200, "Repository list returned")),
			"post": operation("createUserRepo", "Create a repository for the authenticated user.", auth(), nil, nil, response(201, "Repository created")),
		},
		"/api/v3/repos/{owner}/{repo}": map[string]any{
			"get":    operation("getRepo", "Get a repository.", nil, nil, pathParams(param("owner", "string"), param("repo", "string")), response(200, "Repository returned")),
			"patch":  operation("updateRepo", "Update a repository.", auth(), nil, pathParams(param("owner", "string"), param("repo", "string")), response(200, "Repository updated")),
			"delete": operation("deleteRepo", "Delete a repository.", auth(), nil, pathParams(param("owner", "string"), param("repo", "string")), response(204, "Repository deleted")),
		},
		"/api/v3/repos/{owner}/{repo}/issues": map[string]any{
			"get":  operation("listIssues", "List repository issues.", nil, nil, pathParams(param("owner", "string"), param("repo", "string")), response(200, "Issue list returned")),
			"post": operation("createIssue", "Create an issue.", auth(), nil, pathParams(param("owner", "string"), param("repo", "string")), response(201, "Issue created")),
		},
		"/api/v3/repos/{owner}/{repo}/issues/{number}": map[string]any{
			"get":   operation("getIssue", "Get an issue.", nil, nil, pathParams(param("owner", "string"), param("repo", "string"), param("number", "integer")), response(200, "Issue returned")),
			"patch": operation("updateIssue", "Update an issue.", auth(), nil, pathParams(param("owner", "string"), param("repo", "string"), param("number", "integer")), response(200, "Issue updated")),
		},
		"/api/v3/repos/{owner}/{repo}/issues/{number}/comments": map[string]any{
			"get":  operation("listIssueComments", "List issue comments.", nil, nil, pathParams(param("owner", "string"), param("repo", "string"), param("number", "integer")), response(200, "Issue comments returned")),
			"post": operation("createIssueComment", "Create an issue comment.", auth(), nil, pathParams(param("owner", "string"), param("repo", "string"), param("number", "integer")), response(201, "Issue comment created")),
		},
		"/api/v3/repos/{owner}/{repo}/pulls": map[string]any{
			"get":  operation("listPullRequests", "List pull requests.", nil, nil, pathParams(param("owner", "string"), param("repo", "string")), response(200, "Pull request list returned")),
			"post": operation("createPullRequest", "Create a pull request.", auth(), nil, pathParams(param("owner", "string"), param("repo", "string")), response(201, "Pull request created")),
		},
		"/api/v3/repos/{owner}/{repo}/pulls/{number}": map[string]any{
			"get":   operation("getPullRequest", "Get a pull request.", nil, nil, pathParams(param("owner", "string"), param("repo", "string"), param("number", "integer")), response(200, "Pull request returned")),
			"patch": operation("updatePullRequest", "Update a pull request.", auth(), nil, pathParams(param("owner", "string"), param("repo", "string"), param("number", "integer")), response(200, "Pull request updated")),
		},
	}
}

func buildRESTOpenAPIPaths() map[string]any {
	return map[string]any{
		"/api/ext/v1": map[string]any{
			"get": operation("getExtensionAPIDiscovery", "Get extension API discovery links.", nil, nil, nil, response(200, "extension API discovery document")),
		},
		"/api/ext/v1/": map[string]any{
			"get": operation("getExtensionAPIDiscoveryTrailingSlash", "Get extension API discovery links.", nil, nil, nil, response(200, "extension API discovery document")),
		},
		"/api/ext/v1/openapi.json": map[string]any{
			"get": operation("getRESTOpenAPISpec", "Get the published OpenAPI contract for extension REST APIs.", nil, nil, nil, response(200, "OpenAPI document")),
		},
		"/api/ext/v1/agents": map[string]any{
			"post": operation("createAgent", "Register a new agent identity.", nil, jsonBody(true, map[string]any{
				"prefix_login":      stringSchema("Optional login prefix for the created agent account."),
				"default_repo_name": stringSchema("Optional default repository name for the created agent."),
			}, nil), nil, response(201, "Agent created")),
		},
		"/api/ext/v1/agent-invites": map[string]any{
			"post": operation("createAgentInvite", "Create an invite token used to bind an agent to a user.", auth(), jsonBody(false, map[string]any{
				"repo_grants": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"repo_full_name": stringSchema("Repository full name to grant during bind."), "permission": stringSchema("Requested permission (read/write/admin).")}}},
				"team_grants": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"org": stringSchema("Organization login for a team grant."), "team_slug": stringSchema("Team slug to grant during bind."), "role": stringSchema("Team role (member/maintainer).")}}},
			}, nil), nil, response(201, "Agent invite created")),
		},
		"/api/ext/v1/agent-bindings/confirm": map[string]any{
			"post": operation("confirmAgentBinding", "Confirm an agent binding using an invite token.", auth(), jsonBody(true, map[string]any{
				"invite_token": stringSchema("Invite token issued by POST /api/ext/v1/agent-invites."),
			}, []string{"invite_token"}), nil, response(201, "Binding confirmed")),
		},
		"/api/ext/v1/agent-bindings/{agent_login}": map[string]any{
			"patch": operation("renameBoundAgent", "Rename a bound agent's display name.", auth(), jsonBody(true, map[string]any{
				"name": stringSchema("New display name for the bound agent."),
			}, []string{"name"}), pathParams(param("agent_login", "string")), response(200, "Agent renamed")),
		},
		"/api/ext/v1/agent-bindings/{agent_login}/reset-token": map[string]any{
			"post": operation("resetAgentToken", "Rotate the token for a bound agent login.", auth(), nil, pathParams(param("agent_login", "string")), response(200, "Token rotated")),
		},
		"/api/ext/v1/agent-bindings/{agent_login}/switch-session": map[string]any{
			"post": operation("switchAgentSession", "Create a temporary console session for a bound agent without rotating its existing tokens.", auth(), nil, pathParams(param("agent_login", "string")), response(200, "Switch session created")),
		},
		"/api/ext/v1/agent-bindings/{agent_login}/refresh-session": map[string]any{
			"post": operation("refreshAgentSwitchSession", "Refresh an active bound-agent switch session before it expires.", auth(), nil, pathParams(param("agent_login", "string")), response(200, "Switch session refreshed")),
		},
		"/api/ext/v1/oidc/device/code": map[string]any{
			"post": operation("createOIDCDeviceCode", "Start a generic OIDC device-code login flow.", nil, nil, nil, response(200, "Device code issued")),
		},
		"/api/ext/v1/oidc/session": map[string]any{
			"post": operation("exchangeOIDCSession", "Exchange generic OIDC session data for a local session.", nil, jsonBody(true, map[string]any{
				"device_code": stringSchema("OIDC device code previously issued to the client."),
			}, []string{"device_code"}), nil, response(200, "Session established")),
		},
		"/api/ext/v1/oidc/callback": map[string]any{
			"post": operation("handleOIDCCallback", "Handle the generic OIDC callback payload.", nil, jsonBody(true, map[string]any{
				"id_token": stringSchema("OIDC ID token returned from the login redirect flow."),
			}, []string{"id_token"}), nil, response(200, "Callback processed")),
		},
		"/api/ext/v1/oidc/lookup": map[string]any{
			"post": operation("lookupOIDCIdentity", "Resolve a generic OIDC identity to a local user.", nil, jsonBody(true, map[string]any{
				"id_token": stringSchema("OIDC ID token to validate and map to a local user."),
			}, []string{"id_token"}), nil, response(200, "Identity resolved")),
		},
		"/api/ext/v1/oauth/device/approve": map[string]any{
			"post": operation("approveOAuthDeviceCode", "Approve an OAuth device code for the authenticated human user.", auth(), jsonAndFormBody(true, map[string]any{
				"user_code": stringSchema("User code displayed to the human during the OAuth device flow."),
			}, []string{"user_code"}), nil, response(200, "Device code approved")),
		},
		"/api/ext/v1/oauth/device/reject": map[string]any{
			"post": operation("rejectOAuthDeviceCode", "Reject an OAuth device code for the authenticated human user.", auth(), jsonAndFormBody(true, map[string]any{
				"user_code": stringSchema("User code displayed to the human during the OAuth device flow."),
				"reason":    stringSchema("Optional rejection reason recorded in the device-code audit log."),
			}, []string{"user_code"}), nil, response(200, "Device code rejected")),
		},
		"/auth/connected/login": map[string]any{
			"get": operation("startConnectedLogin", "Redirect the browser to the configured connected login provider.", nil, nil, nil, response(302, "Redirect to connected login")),
		},
		"/auth/connected/callback": map[string]any{
			"get": operation("handleConnectedCallback", "Exchange a connected login authorization code for a local session.", nil, nil, queryParams(
				param("code", "string"),
				param("error", "string"),
				param("state", "string"),
			), map[string]any{
				"200": map[string]any{"description": "Direct agent callback without browser state returns durable token JSON; browser callback without console redirect returns a one-time local authorization code JSON."},
				"302": map[string]any{"description": "Browser callback redirects to the console with a one-time local authorization code and PKCE verifier cookie."},
			}),
		},
		"/api/ext/v1/user/agents": map[string]any{
			"get": operation("listBoundAgents", "List agents bound to the authenticated user.", auth(), nil, nil, response(200, "Bound agents returned")),
		},
		"/api/ext/v1/viewer/summary": map[string]any{
			"get": operation("getViewerSummary", "Get the authenticated viewer's console summary.", auth(), nil, nil, response(200, "Viewer summary returned")),
		},
		"/api/ext/v1/user/orgs": map[string]any{
			"post": operation("createUserOrg", "Create a local organization for the authenticated user.", auth(), jsonBody(true, map[string]any{
				"login":                         stringSchema("Organization login."),
				"name":                          stringSchema("Optional organization display name."),
				"default_repository_permission": stringSchema("Default base repository permission for organization members."),
			}, []string{"login"}), nil, response(201, "Organization created")),
		},
		"/api/ext/v1/user/tokens": map[string]any{
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
		"/api/ext/v1/notifications/summary": map[string]any{
			"get": operation("getNotificationsSummary", "Get the authenticated viewer's notification summary.", auth(), nil, nil, response(200, "Notifications summary returned")),
		},
		"/api/ext/v1/orgs/{org}/management-summary": map[string]any{
			"get": operation("getOrgManagementSummary", "Get a management summary for an organization.", auth(), nil, pathParams(
				param("org", "string"),
			), response(200, "Organization management summary returned")),
		},
		"/api/ext/v1/repos/{owner}/{repo}/summary": map[string]any{
			"get": operation("getRepoSummary", "Get a console summary for a repository.", auth(), nil, pathParams(
				param("owner", "string"),
				param("repo", "string"),
			), response(200, "Repository summary returned")),
		},
		"/api/ext/v1/repos/{owner}/{repo}/issues/{number}/thread": map[string]any{
			"get": operation("getIssueThread", "Get an issue thread aggregate.", auth(), nil, append(pathParams(
				param("owner", "string"),
				param("repo", "string"),
				param("number", "integer"),
			), queryParams(
				param("include", "string"),
				param("comments_page", "integer"),
				param("comments_per_page", "integer"),
			)...), response(200, "Issue thread aggregate returned")),
		},
		"/api/ext/v1/repos/{owner}/{repo}/team-sharing/enable": map[string]any{
			"post": operation("enableRepoTeamSharing", "Enable team-based sharing for a repository.", auth(), nil, pathParams(
				param("owner", "string"),
				param("repo", "string"),
			), response(200, "Team sharing enabled")),
		},
		"/api/ext/v1/repos/{owner}/{repo}/wiki/pages": map[string]any{
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
		"/api/ext/v1/repos/{owner}/{repo}/wiki/search": map[string]any{
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
		"/api/ext/v1/repos/{owner}/{repo}/wiki/catalog": map[string]any{
			"get": operation("getWikiCatalog", "Get an aggregate wiki catalog for a repository.", nil, nil, append(pathParams(
				param("owner", "string"),
				param("repo", "string"),
			), queryParams(
				param("include", "string"),
				param("path", "string"),
				param("recursive", "boolean"),
				param("labels", "string"),
				param("exclude_labels", "string"),
			)...), response(200, "Wiki catalog returned")),
		},
		"/api/ext/v1/repos/{owner}/{repo}/wiki/pages/batch": map[string]any{
			"post": operation("batchGetWikiPages", "Get multiple wiki pages in one extension aggregate request.", auth(), jsonBody(true, map[string]any{
				"slugs":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Wiki page slugs to fetch."},
				"include":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional fields to include, such as body, labels, backlinks, or backlink_count."},
				"body_limit": map[string]any{"type": "integer", "minimum": 0, "description": "Maximum body characters to return per page."},
				"ref":        stringSchema("Optional full commit SHA from wiki history."),
			}, []string{"slugs"}), pathParams(
				param("owner", "string"),
				param("repo", "string"),
			), response(200, "Wiki page batch returned")),
		},
		"/api/ext/v1/repos/{owner}/{repo}/wiki/tree": map[string]any{
			"get": operation("listWikiTree", "List one directory view from the authoritative wiki tree.", nil, nil, append(pathParams(
				param("owner", "string"),
				param("repo", "string"),
			), queryParams(
				param("path", "string"),
				param("ref", "string"),
			)...), response(200, "Wiki tree returned")),
		},
		"/api/ext/v1/repos/{owner}/{repo}/wiki/state": map[string]any{
			"get": operation("getWikiState", "Get the authoritative wiki derived-index state for a repository.", auth(), nil, pathParams(
				param("owner", "string"),
				param("repo", "string"),
			), response(200, "Current wiki state")),
		},
		"/api/ext/v1/repos/{owner}/{repo}/wiki/reconcile/request": map[string]any{
			"post": operation("requestWikiReconcile", "Request a wiki reconcile without running it synchronously.", auth(), nil, pathParams(
				param("owner", "string"),
				param("repo", "string"),
			), response(202, "Reconcile request recorded")),
		},
		"/api/ext/v1/repos/{owner}/{repo}/wiki/reconcile": map[string]any{
			"post": operation("reconcileWiki", "Run the authoritative wiki reconcile synchronously and return the persisted result.", auth(), nil, pathParams(
				param("owner", "string"),
				param("repo", "string"),
			), response(200, "Reconcile completed")),
		},
		"/api/ext/v1/repos/{owner}/{repo}/wiki/move": map[string]any{
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
		"/api/ext/v1/repos/{owner}/{repo}/wiki/pages/{slug}": map[string]any{
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
		"/api/ext/v1/repos/{owner}/{repo}/wiki/pages/{slug}/move": map[string]any{
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
		"/api/ext/v1/repos/{owner}/{repo}/wiki/pages/{slug}/labels": map[string]any{
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
		"/api/ext/v1/repos/{owner}/{repo}/wiki/pages/{slug}/labels/{name}": map[string]any{
			"delete": operation("removeWikiPageLabel", "Remove one label from a wiki page.", auth(), nil, pathParams(
				param("owner", "string"),
				param("repo", "string"),
				wikiSlugParamSpec(),
				param("name", "string"),
			), response(200, "Remaining wiki page labels returned")),
		},
		"/api/ext/v1/repos/{owner}/{repo}/wiki/pages/{slug}/history": map[string]any{
			"get": operation("listWikiPageHistory", "List paginated revision history for a wiki page slug.", nil, nil, append(pathParams(
				param("owner", "string"),
				param("repo", "string"),
				wikiSlugParamSpec(),
			), queryParams(
				param("page", "integer"),
				param("per_page", "integer"),
			)...), response(200, "Wiki page history returned")),
		},
		"/api/ext/v1/repos/{owner}/{repo}/wiki/compact": map[string]any{
			"post": operation("compactWikiHistory", "Temporarily disabled while the wiki catalog corruption incident is being contained and repaired.", auth(), jsonBody(false, map[string]any{
				"before": stringSchema("Reserved for future bounded compaction support. Currently rejected when non-empty."),
			}, nil), pathParams(
				param("owner", "string"),
				param("repo", "string"),
			), response(409, "Wiki history compaction is temporarily disabled")),
		},
		"/api/ext/v1/repos/{owner}/{repo}/wiki/compact/{jobID}": map[string]any{
			"get": operation("getWikiCompactionJob", "Get the current status for an async wiki history compaction job.", auth(), nil, pathParams(
				param("owner", "string"),
				param("repo", "string"),
				param("jobID", "string"),
			), response(200, "Wiki history compaction job returned")),
		},
		"/api/ext/v1/admin/wiki/repos/{owner}/{repo}/repair-locks": map[string]any{
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
		"/api/ext/v1/repos/{owner}/{repo}/wiki/pages/{slug}/backlinks": map[string]any{
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

func jsonAndFormBody(required bool, properties map[string]any, requiredProps []string) map[string]any {
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
			"application/x-www-form-urlencoded": map[string]any{
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
