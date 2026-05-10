// Package router wires all HTTP routes onto the chi router.
// It is separated from the handler package (rest) so that handler code
// does not need to import unrelated packages like graphql, oauth, etc.
package router

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"gh-server/internal/controlplane"
	"gh-server/internal/githttp"
	"gh-server/internal/graphql"
	srvmiddleware "gh-server/internal/middleware"
	"gh-server/internal/oauth"
	"gh-server/internal/rest"
)

const defaultNonGitBodyLimitBytes int64 = 50 << 20

// RegisterRoutes wires all routes onto the router and returns the host-aware
// mux that handles api.github.localhost path rewriting.
// dbRouter is optional: when non-nil, tokens are resolved through the control
// plane for multi-agent DB routing. When nil, current single-DB behavior is used.
func RegisterRoutes(r chi.Router, handlers *rest.Deps, gitHandler *githttp.Handler, gqlSrv *graphql.Server, oauthHandler *oauth.Handler, dbRouter *controlplane.DBRouter, consoleBaseURL string) http.Handler {
	// Keep the default 50 MB cap for API traffic, but let git-receive-pack
	// enforce its own GitHub-style push limit in internal/githttp.
	r.Use(srvmiddleware.MaxBodySizeUnless(defaultNonGitBodyLimitBytes, func(r *http.Request) bool {
		return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git-receive-pack")
	}))
	r.Use(corsMiddleware(consoleBaseURL))
	r.Use(srvmiddleware.ConditionalETag())

	if handlers != nil && handlers.Router == nil && dbRouter != nil {
		handlers.Router = dbRouter
	}

	rateLimitMw := srvmiddleware.APIRateLimitHeaders()

	registerOAuthRoutes(r, oauthHandler, dbRouter)
	registerPublicAuthRoutes(r, handlers, rateLimitMw)
	registerAgentPublicRoutes(r, handlers, rateLimitMw)
	registerGitHTTPRoutes(r, gitHandler, handlers, dbRouter, consoleBaseURL)
	registerAPIDiscoveryRoutes(r, handlers, rateLimitMw)
	registerPublicUserLookupRoutes(r, handlers, dbRouter, rateLimitMw)
	registerPublicRepoRoutes(r, handlers, dbRouter, rateLimitMw)
	registerAuthenticatedRoutes(r, handlers, gqlSrv, dbRouter, rateLimitMw)
	registerNotFoundHandler(r)

	return registerHostMux(r)
}

func corsMiddleware(consoleBaseURL string) func(http.Handler) http.Handler {
	allowedOrigins := allowedCORSOrigins(consoleBaseURL)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			if origin != "" {
				if _, ok := allowedOrigins[origin]; ok {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Vary", "Origin")
					if r.Method == http.MethodOptions {
						reqHeaders := strings.TrimSpace(r.Header.Get("Access-Control-Request-Headers"))
						if reqHeaders == "" {
							reqHeaders = "Authorization, Content-Type"
						}
						w.Header().Set("Access-Control-Allow-Headers", reqHeaders)
						w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
						w.Header().Set("Access-Control-Max-Age", "600")
						w.WriteHeader(http.StatusNoContent)
						return
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func allowedCORSOrigins(consoleBaseURL string) map[string]struct{} {
	if configuredOrigins := strings.TrimSpace(os.Getenv("CORS_ALLOWED_ORIGINS")); configuredOrigins != "" {
		origins := make(map[string]struct{})
		for _, rawOrigin := range strings.Split(configuredOrigins, ",") {
			origin, _, _, _ := normalizeOrigin(rawOrigin)
			if origin != "" {
				origins[origin] = struct{}{}
			}
		}
		return origins
	}

	origins := make(map[string]struct{})
	baseOrigin, host, scheme, port := normalizeOrigin(consoleBaseURL)
	if baseOrigin != "" {
		origins[baseOrigin] = struct{}{}
	}
	if host == "localhost" || host == "127.0.0.1" {
		altHost := "localhost"
		if host == "localhost" {
			altHost = "127.0.0.1"
		}
		altOrigin := buildOrigin(scheme, altHost, port)
		if altOrigin != "" {
			origins[altOrigin] = struct{}{}
		}
	}
	return origins
}

func normalizeOrigin(raw string) (origin, host, scheme, port string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", "", ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", "", "", ""
	}
	scheme = parsed.Scheme
	host = parsed.Hostname()
	port = parsed.Port()
	origin = buildOrigin(scheme, host, port)
	return origin, host, scheme, port
}

func buildOrigin(scheme, host, port string) string {
	if scheme == "" || host == "" {
		return ""
	}
	if port != "" {
		return scheme + "://" + host + ":" + port
	}
	return scheme + "://" + host
}

func registerOAuthRoutes(r chi.Router, oauthHandler *oauth.Handler, dbRouter *controlplane.DBRouter) {
	// Public OAuth endpoints used by the device and auth-code bootstrap flow.
	r.Post("/login/device/code", oauthHandler.RequestDeviceCode)
	r.Post("/login/oauth/access_token", oauthHandler.AccessToken)
	r.Get("/login/oauth/authorize", oauthHandler.Authorize)
	// Device code approval requires an authenticated user; the handler also checks
	// context directly so direct unit tests cannot bypass the contract.
	authMW := srvmiddleware.TokenAuth(oauthHandler.Svc, dbRouter)
	deviceVerificationRateLimit := srvmiddleware.RateLimit(5, time.Minute)
	r.With(deviceVerificationRateLimit, authMW).Get("/login/device", oauthHandler.DeviceCodeVerification)
	r.With(deviceVerificationRateLimit, authMW).Post("/login/device", oauthHandler.DeviceCodeVerification)
}

func registerPublicAuthRoutes(r chi.Router, handlers *rest.Deps, rateLimitMw func(http.Handler) http.Handler) {
	r.Group(func(r chi.Router) {
		r.Use(rateLimitMw)
		// Auth0 device flow (no auth required)
		r.Post("/api/v3/auth0/device/code", handlers.Auth0DeviceCode)
		r.Post("/api/v3/auth0/session", handlers.Auth0Session)
		r.Post("/api/v3/auth0/callback", handlers.Auth0Callback)
		r.Post("/api/v3/auth0/lookup", handlers.Auth0Lookup)
	})
}

func registerAgentPublicRoutes(r chi.Router, handlers *rest.Deps, rateLimitMw func(http.Handler) http.Handler) {
	r.Group(func(r chi.Router) {
		r.Use(rateLimitMw)
		// Agent registration (no auth required)
		r.Post("/api/v3/agents", handlers.CreateAgent)
	})
}

func registerGitHTTPRoutes(r chi.Router, gitHandler *githttp.Handler, handlers *rest.Deps, dbRouter *controlplane.DBRouter, consoleBaseURL string) {
	// Git Smart HTTP
	// In control-plane mode, require auth so unauthenticated requests are blocked.
	// In single-DB mode, preserve existing behavior and allow optional auth.
	r.Route("/{owner}/{repo}", func(r chi.Router) {
		if strings.TrimSpace(consoleBaseURL) != "" {
			r.Get("/", consoleRedirectHandler(consoleBaseURL, false))
			r.Head("/", consoleRedirectHandler(consoleBaseURL, false))
			r.Get("/issues/{issue_id}", consoleRedirectHandler(consoleBaseURL, true))
			r.Head("/issues/{issue_id}", consoleRedirectHandler(consoleBaseURL, true))
		}

		var authMw func(http.Handler) http.Handler
		if dbRouter != nil {
			authMw = srvmiddleware.TokenAuth(handlers.Svc, dbRouter)
		} else {
			authMw = srvmiddleware.OptionalTokenAuth(handlers.Svc, dbRouter)
		}

		r.With(authMw).Get("/info/refs", gitHandler.InfoRefs)
		r.With(authMw).Post("/git-upload-pack", gitHandler.UploadPack)
		r.With(authMw).Post("/git-receive-pack", gitHandler.ReceivePack)
	})
}

func consoleRedirectHandler(consoleBaseURL string, isIssue bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner := chi.URLParam(r, "owner")
		repo := strings.TrimSuffix(chi.URLParam(r, "repo"), ".git")
		if owner == "" || repo == "" {
			http.NotFound(w, r)
			return
		}
		base := strings.TrimRight(consoleBaseURL, "/")
		target := base + "/vault/" + owner + "/" + repo
		if isIssue {
			issueID := chi.URLParam(r, "issue_id")
			if issueID != "" {
				target += "/memories/" + issueID
			}
		}
		if raw := strings.TrimSpace(r.URL.RawQuery); raw != "" {
			target += "?" + raw
		}
		http.Redirect(w, r, target, http.StatusFound)
	}
}

func registerAPIDiscoveryRoutes(r chi.Router, handlers *rest.Deps, rateLimitMw func(http.Handler) http.Handler) {
	// API with optional auth
	// Allow unauthenticated access for API discovery, but return 401
	// if an Authorization header is present with an empty/invalid token.
	r.Group(func(r chi.Router) {
		r.Use(srvmiddleware.OptionalTokenAuth(handlers.Svc, handlers.Router))
		r.Use(rateLimitMw)
		r.Get("/api/v3", handlers.GetMeta)  // without trailing slash
		r.Get("/api/v3/", handlers.GetMeta) // with trailing slash
		r.Get("/api/v3/openapi.json", handlers.GetOpenAPI)
		r.Get("/api/v3/meta", handlers.GetServerMeta)
		r.Get("/api/v3/rate_limit", handlers.GetRateLimit)
	})
	// Avatar serving
	r.Get("/avatars/{login}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://avatars.githubusercontent.com/u/1?v=4", http.StatusFound)
	})
}

// registerPublicRepoRoutes registers repo-scoped routes under OptionalTokenAuth
// so that public repositories are readable without authentication.
// Write methods (POST/PUT/PATCH/DELETE) still require a valid token.
func registerPublicRepoRoutes(r chi.Router, handlers *rest.Deps, dbRouter *controlplane.DBRouter, rateLimitMw func(http.Handler) http.Handler) {
	r.Group(func(r chi.Router) {
		r.Use(srvmiddleware.OptionalTokenAuth(handlers.Svc, dbRouter))
		r.Use(rateLimitMw)
		r.Use(srvmiddleware.RequireAuthForWrites(handlers.Svc))

		registerRepoRoutes(r, handlers)
		registerIssueAttachmentRoutes(r, handlers)
	})
}

func registerPublicUserLookupRoutes(r chi.Router, handlers *rest.Deps, dbRouter *controlplane.DBRouter, rateLimitMw func(http.Handler) http.Handler) {
	r.Group(func(r chi.Router) {
		r.Use(srvmiddleware.OptionalTokenAuth(handlers.Svc, dbRouter))
		r.Use(rateLimitMw)

		r.Get("/api/v3/users/{username}/starred", handlers.ListUserStarredRepos)
	})
}

func registerAuthenticatedRoutes(r chi.Router, handlers *rest.Deps, gqlSrv *graphql.Server, dbRouter *controlplane.DBRouter, rateLimitMw func(http.Handler) http.Handler) {
	r.Group(func(r chi.Router) {
		r.Use(srvmiddleware.TokenAuth(handlers.Svc, dbRouter))
		r.Use(rateLimitMw)

		registerGraphQLRoutes(r, gqlSrv)
		registerRealtimeIssueRoutes(r, handlers)
		registerPresenceRoutes(r, handlers)
		registerUserScopedRoutes(r, handlers)
		registerAgentBindingRoutes(r, handlers)
		registerUserLookupRoutes(r, handlers)
		registerOrgRoutes(r, handlers)
		registerActionsRoutes(r, handlers)
		registerRulesetRoutes(r, handlers)
		registerLicenseRoutes(r, handlers)
		registerSearchRoutes(r, handlers)
		registerAppRoutes(r, handlers)
		registerEnvByRepoIDRoutes(r, handlers)
	})
}

func registerGraphQLRoutes(r chi.Router, gqlSrv *graphql.Server) {
	// GraphQL
	r.Post("/api/graphql", gqlSrv.Handler)
	r.Post("/graphql", gqlSrv.Handler)
}

func registerRealtimeIssueRoutes(r chi.Router, handlers *rest.Deps) {
	r.Get("/api/v3/issues/{id}/typing", handlers.SubscribeIssueTyping)
	r.Post("/api/v3/issues/{id}/typing", handlers.SignalIssueTyping)
}

func registerIssueAttachmentRoutes(r chi.Router, handlers *rest.Deps) {
	r.Post("/api/v3/repos/{owner}/{repo}/attachments", handlers.UploadRepoAttachment)
	r.Post("/api/v3/repositories/{repo_id}/attachments", handlers.UploadRepoAttachmentByID)
	r.Post("/api/v3/issues/{id}/attachments", handlers.UploadIssueAttachment)
	r.Get("/api/v3/issues/{id}/attachments", handlers.ListIssueAttachments)
	r.Get("/api/v3/attachments/{uuid}", handlers.DownloadIssueAttachment)
	r.Delete("/api/v3/attachments/{uuid}", handlers.DeleteIssueAttachment)
}

func registerPresenceRoutes(r chi.Router, handlers *rest.Deps) {
	// Presence API (requires authentication)
	r.Post("/api/v3/presence/heartbeat", handlers.Presence.PostPresenceHeartbeat)
	r.Get("/api/v3/issues/{issue_id}/presence", handlers.Presence.GetIssuePresence)
	r.Get("/api/v3/users/{user_id}/last-seen", handlers.Presence.GetUserLastSeen)
	r.Get("/api/v3/user/presence/privacy", handlers.Presence.GetPresencePrivacy)
	r.Put("/api/v3/user/presence/privacy", handlers.Presence.PutPresencePrivacy)
}

func registerAgentBindingRoutes(r chi.Router, handlers *rest.Deps) {
	r.Post("/api/v3/agent-invites", handlers.CreateAgentInvite)
	r.Post("/api/v3/agent-bindings/confirm", handlers.ConfirmAgentBinding)
	r.Post("/api/v3/agent-bindings/{agent_login}/reset-token", handlers.ResetAgentToken)
}

func registerUserScopedRoutes(r chi.Router, handlers *rest.Deps) {
	// Current user
	r.Get("/api/v3/user", handlers.GetAuthenticatedUser)
	r.Post("/api/v3/user/repos", handlers.CreateUserRepo)
	r.Get("/api/v3/user/repos", handlers.ListUserRepos)
	r.Get("/api/v3/user/orgs", handlers.ListUserOrgs)
	r.Post("/api/v3/user/orgs", handlers.CreateUserOrg)
	r.Get("/api/v3/user/agents", handlers.ListBoundAgents)
	r.Get("/api/v3/user/starred", handlers.ListStarredRepos)
	r.Get("/api/v3/user/starred/{owner}/{repo}", handlers.IsRepoStarred)
	r.Put("/api/v3/user/starred/{owner}/{repo}", handlers.StarRepo)
	r.Delete("/api/v3/user/starred/{owner}/{repo}", handlers.UnstarRepo)

	// Tokens
	r.Get("/api/v3/user/tokens", handlers.ListTokens)
	r.Post("/api/v3/user/tokens", handlers.CreateToken)
	r.Delete("/api/v3/user/tokens", handlers.DeleteToken)

	// SSH keys
	r.Get("/api/v3/user/keys", handlers.ListSSHKeys)
	r.Post("/api/v3/user/keys", handlers.CreateSSHKey)
	r.Get("/api/v3/user/keys/{key_id}", handlers.GetSSHKey)
	r.Delete("/api/v3/user/keys/{key_id}", handlers.DeleteSSHKey)

	// SSH signing keys
	r.Get("/api/v3/user/ssh_signing_keys", handlers.ListSSHSigningKeys)
	r.Post("/api/v3/user/ssh_signing_keys", handlers.CreateSSHSigningKey)
	r.Get("/api/v3/user/ssh_signing_keys/{ssh_signing_key_id}", handlers.GetSSHSigningKey)
	r.Delete("/api/v3/user/ssh_signing_keys/{ssh_signing_key_id}", handlers.DeleteSSHSigningKey)

	// GPG keys
	r.Get("/api/v3/user/gpg_keys", handlers.ListGPGKeys)
	r.Post("/api/v3/user/gpg_keys", handlers.CreateGPGKey)
	r.Delete("/api/v3/user/gpg_keys/{gpg_key_id}", handlers.DeleteGPGKey)

	// Gists
	r.Get("/api/v3/gists", handlers.ListGists)
	r.Post("/api/v3/gists", handlers.CreateGist)
	r.Get("/api/v3/gists/{gist_id}", handlers.GetGist)
	r.Patch("/api/v3/gists/{gist_id}", handlers.UpdateGist)
	r.Post("/api/v3/gists/{gist_id}", handlers.UpdateGist) // gh gist rename sends POST
	r.Delete("/api/v3/gists/{gist_id}", handlers.DeleteGist)

	// Notifications
	r.Get("/api/v3/notifications", handlers.ListNotifications)
	r.Put("/api/v3/notifications", handlers.MarkNotificationsRead)

	// Repository Invitations (user-specific)
	r.Get("/api/v3/user/repository_invitations", handlers.ListUserInvitations)
	r.Patch("/api/v3/user/repository_invitations/{invitation_id}", handlers.AcceptInvitation)
	r.Delete("/api/v3/user/repository_invitations/{invitation_id}", handlers.DeclineInvitation)
	r.Get("/api/v3/user/organization_invitations", handlers.ListUserOrganizationInvitations)
	r.Patch("/api/v3/user/organization_invitations/{invitation_id}", handlers.AcceptOrganizationInvitation)
	r.Delete("/api/v3/user/organization_invitations/{invitation_id}", handlers.DeclineOrganizationInvitation)
}

func registerUserLookupRoutes(r chi.Router, handlers *rest.Deps) {
	// Users
	r.Get("/api/v3/users/{username}", handlers.GetUser)
	r.Get("/api/v3/users/{username}/repos", handlers.ListUserRepos)
	r.Get("/api/v3/users/{username}/received_events", handlers.ListUserReceivedEvents)
	r.Get("/api/v3/users/{username}/events", handlers.ListUserEvents)
	r.Get("/api/v3/users/{username}/keys", handlers.ListUserPublicKeys)
	r.Get("/api/v3/users/{username}/ssh_signing_keys", handlers.ListUserSigningKeys)
	r.Get("/api/v3/users/{username}/gpg_keys", handlers.ListUserGPGKeys)
}

func registerOrgRoutes(r chi.Router, handlers *rest.Deps) {
	// Orgs
	r.Get("/api/v3/orgs/{org}", handlers.GetOrg)
	r.Get("/api/v3/orgs/{org}/members", handlers.ListOrgMembers)
	r.Delete("/api/v3/orgs/{org}/members/{username}", handlers.DeleteOrgMember)
	r.Put("/api/v3/orgs/{org}/memberships/{username}", handlers.SetOrgMembership)
	r.Get("/api/v3/orgs/{org}/memberships/{username}", handlers.GetOrgMembership)
	r.Delete("/api/v3/orgs/{org}/memberships/{username}", handlers.DeleteOrgMembership)
	r.Get("/api/v3/orgs/{org}/repos", handlers.ListOrgRepos)
	r.Post("/api/v3/orgs/{org}/repos", handlers.CreateOrgRepo)
	r.Get("/api/v3/orgs/{org}/audit-log", handlers.ListOrgAuditLog)
	r.Get("/api/v3/orgs/{org}/outside_collaborators", handlers.ListOutsideCollaborators)
	r.Get("/api/v3/orgs/{org}/invitations", handlers.ListOrganizationInvitations)
	r.Post("/api/v3/orgs/{org}/invitations", handlers.CreateOrganizationInvitation)
	r.Delete("/api/v3/orgs/{org}/invitations/{invitation_id}", handlers.RevokeOrganizationInvitation)
	r.Get("/api/v3/orgs/{org}/teams", handlers.ListOrgTeams)
	r.Post("/api/v3/orgs/{org}/teams", handlers.CreateTeam)
	r.Get("/api/v3/orgs/{org}/teams/{team_slug}", handlers.GetTeam)
	r.Patch("/api/v3/orgs/{org}/teams/{team_slug}", handlers.UpdateTeam)
	r.Delete("/api/v3/orgs/{org}/teams/{team_slug}", handlers.DeleteTeam)
	r.Get("/api/v3/orgs/{org}/teams/{team_slug}/members", handlers.ListTeamMembers)
	r.Get("/api/v3/orgs/{org}/teams/{team_slug}/invitations", handlers.ListPendingTeamInvitations)
	r.Put("/api/v3/orgs/{org}/teams/{team_slug}/memberships/{username}", handlers.AddTeamMember)
	r.Get("/api/v3/orgs/{org}/teams/{team_slug}/memberships/{username}", handlers.GetTeamMembership)
	r.Delete("/api/v3/orgs/{org}/teams/{team_slug}/memberships/{username}", handlers.RemoveTeamMember)
	r.Put("/api/v3/orgs/{org}/teams/{team_slug}/repos/{owner}/{repo}", handlers.AddTeamRepo)
	r.Get("/api/v3/orgs/{org}/teams/{team_slug}/repos", handlers.ListTeamRepos)
	r.Delete("/api/v3/orgs/{org}/teams/{team_slug}/repos/{owner}/{repo}", handlers.RemoveTeamRepo)
}

func registerRepoRoutes(r chi.Router, handlers *rest.Deps) {
	registerRepoCoreRoutes(r, handlers)
	registerRepoBranchCommitRoutes(r, handlers)
	registerIssueRoutes(r, handlers)
	registerPullRequestRoutes(r, handlers)
	registerMergeUpstreamRoutes(r, handlers)
	registerAutolinkRoutes(r, handlers)
	registerCheckRoutes(r, handlers)
	registerWebhookRoutes(r, handlers)
	registerDeploymentRoutes(r, handlers)
	registerBranchProtectionRoutes(r, handlers)
	registerDependabotAlertRoutes(r, handlers)
	registerRepoInvitationRoutes(r, handlers)
	registerRepoLabelRoutes(r, handlers)
	registerRepoMilestoneRoutes(r, handlers)
	registerRepoDeployKeyRoutes(r, handlers)
	registerRepoReleaseRoutes(r, handlers)
	registerRepoPagesRoutes(r, handlers)
	registerRepoWikiRoutes(r, handlers)
}

func registerRepoPagesRoutes(r chi.Router, handlers *rest.Deps) {
	r.Get("/api/v3/repos/{owner}/{repo}/pages", handlers.GetPages)
	r.Post("/api/v3/repos/{owner}/{repo}/pages", handlers.EnablePages)
	r.Put("/api/v3/repos/{owner}/{repo}/pages", handlers.UpdatePages)
	r.Delete("/api/v3/repos/{owner}/{repo}/pages", handlers.DisablePages)
	r.Get("/api/v3/repos/{owner}/{repo}/pages/builds", handlers.ListPagesBuilds)
	r.Post("/api/v3/repos/{owner}/{repo}/pages/builds", handlers.CreatePagesBuild)
}

func registerRepoWikiRoutes(r chi.Router, handlers *rest.Deps) {
	r.Post("/api/v3/repos/{owner}/{repo}/wiki/move", handlers.MoveWikiPagePrefix)
	r.Get("/api/v3/repos/{owner}/{repo}/wiki/pages", handlers.ListWikiPages)
	r.Get("/api/v3/repos/{owner}/{repo}/wiki/search", handlers.SearchWikiPages)
	r.Get("/api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/labels", handlers.ListWikiPageLabels)
	r.Post("/api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/labels", handlers.AddWikiPageLabels)
	r.Put("/api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/labels", handlers.SetWikiPageLabels)
	r.Delete("/api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/labels/{name}", handlers.RemoveWikiPageLabel)
	r.Delete("/api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/labels", handlers.RemoveAllWikiPageLabels)
	r.Get("/api/v3/repos/{owner}/{repo}/wiki/pages/{slug}", handlers.GetWikiPage)
	r.Get("/api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/history", handlers.ListWikiPageHistory)
	r.Get("/api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/backlinks", handlers.ListWikiBacklinks)
	r.Post("/api/v3/repos/{owner}/{repo}/wiki/pages/{slug}/move", handlers.MoveWikiPage)
	r.Put("/api/v3/repos/{owner}/{repo}/wiki/pages/{slug}", handlers.PutWikiPage)
	r.Delete("/api/v3/repos/{owner}/{repo}/wiki/pages/{slug}", handlers.DeleteWikiPage)
	r.Get("/api/v3/repos/{owner}/{repo}/wiki/pages/*", handlers.GetWikiPage)
	r.Post("/api/v3/repos/{owner}/{repo}/wiki/pages/*", handlers.MoveWikiPage)
	r.Put("/api/v3/repos/{owner}/{repo}/wiki/pages/*", handlers.PutWikiPage)
	r.Delete("/api/v3/repos/{owner}/{repo}/wiki/pages/*", handlers.DeleteWikiPage)
}

func registerRepoCoreRoutes(r chi.Router, handlers *rest.Deps) {
	// Repos
	r.Get("/api/v3/repos/{owner}/{repo}", handlers.GetRepo)
	r.Patch("/api/v3/repos/{owner}/{repo}", handlers.UpdateRepo)
	r.Delete("/api/v3/repos/{owner}/{repo}", handlers.DeleteRepo)
	r.Post("/api/v3/repos/{owner}/{repo}/transfer", handlers.TransferRepo)
	r.Post("/api/v3/repos/{owner}/{repo}/team-sharing/enable", handlers.EnableRepoTeamSharing)
	r.Post("/api/v3/repos/{owner}/{repo}/forks", handlers.ForkRepo)
	r.Get("/api/v3/repos/{owner}/{repo}/forks", handlers.ListRepoForks)
	r.Get("/api/v3/repos/{owner}/{repo}/topics", handlers.GetRepoTopics)
	r.Put("/api/v3/repos/{owner}/{repo}/topics", handlers.ReplaceRepoTopics)
	r.Get("/api/v3/repos/{owner}/{repo}/languages", handlers.GetRepoLanguages)
	r.Get("/api/v3/repos/{owner}/{repo}/collaborators", handlers.ListCollaborators)
	r.Get("/api/v3/repos/{owner}/{repo}/assignees", handlers.ListAssignees)
}

func registerRepoBranchCommitRoutes(r chi.Router, handlers *rest.Deps) {
	// Branches & commits
	r.Get("/api/v3/repos/{owner}/{repo}/branches", handlers.ListBranches)
	// Use wildcard to capture branch names with slashes (e.g., feature/meta)
	// The wildcard captures everything after /branches/ including any slashes
	r.Get("/api/v3/repos/{owner}/{repo}/branches/*", handlers.GetBranch)
	r.Get("/api/v3/repos/{owner}/{repo}/commits", handlers.ListCommits)
	r.Get("/api/v3/repos/{owner}/{repo}/commits/{sha}", handlers.GetCommit)
	r.Get("/api/v3/repos/{owner}/{repo}/compare/*", handlers.CompareCommitsReal)
	r.Get("/api/v3/repos/{owner}/{repo}/readme", handlers.GetReadme)
	r.Get("/api/v3/repos/{owner}/{repo}/tags", handlers.ListTags)
	r.Post("/api/v3/repos/{owner}/{repo}/tags", handlers.CreateTag)
	r.Get("/api/v3/repos/{owner}/{repo}/contributors", handlers.GetContributors)
	r.Post("/api/v3/repos/{owner}/{repo}/git/commits", handlers.CreateGitCommit)
	r.Get("/api/v3/repos/{owner}/{repo}/git/commits/{sha}", handlers.GetGitCommit)
	r.Get("/api/v3/repos/{owner}/{repo}/git/trees/{sha}", handlers.GetGitTree)
	r.Get("/api/v3/repos/{owner}/{repo}/git/blobs/{sha}", handlers.GetGitBlob)
	r.Get("/api/v3/repos/{owner}/{repo}/git/tags/{sha}", handlers.GetGitTag)
	r.Get("/api/v3/repos/{owner}/{repo}/git/refs/heads/*", handlers.GetGitRef)
	r.Get("/api/v3/repos/{owner}/{repo}/git/ref/heads/*", handlers.GetGitRef) // singular form used by CLI
	r.Patch("/api/v3/repos/{owner}/{repo}/git/refs/heads/*", handlers.UpdateGitRef)
	r.Delete("/api/v3/repos/{owner}/{repo}/git/refs/heads/*", handlers.DeleteGitRef)
	r.Get("/api/v3/repos/{owner}/{repo}/git/refs/tags/*", handlers.GetGitTagRef)
	r.Get("/api/v3/repos/{owner}/{repo}/git/ref/tags/*", handlers.GetGitTagRef) // singular form used by CLI
	r.Delete("/api/v3/repos/{owner}/{repo}/git/refs/tags/*", handlers.DeleteGitTagRef)
	r.Post("/api/v3/repos/{owner}/{repo}/git/refs", handlers.CreateGitRef)
	r.Post("/api/v3/repos/{owner}/{repo}/git/blobs", handlers.CreateGitBlob)
	r.Post("/api/v3/repos/{owner}/{repo}/git/trees", handlers.CreateGitTree)
	r.Post("/api/v3/repos/{owner}/{repo}/git/tags", handlers.CreateGitTag)
	// Generic ref endpoints — cover namespaces outside heads/tags (custom
	// refs like refs/locks/*, refs/experiment/*). Registered AFTER the
	// heads/ and tags/ routes so chi dispatches those first and only falls
	// through here for other namespaces.
	r.Get("/api/v3/repos/{owner}/{repo}/git/refs/*", handlers.GetGitRefGeneric)
	r.Get("/api/v3/repos/{owner}/{repo}/git/ref/*", handlers.GetGitRefGeneric) // singular form used by CLI
	r.Patch("/api/v3/repos/{owner}/{repo}/git/refs/*", handlers.UpdateGitRefGeneric)
	r.Patch("/api/v3/repos/{owner}/{repo}/git/ref/*", handlers.UpdateGitRefGeneric) // singular form parity
	r.Delete("/api/v3/repos/{owner}/{repo}/git/refs/*", handlers.DeleteGitRefGeneric)
	r.Get("/api/v3/repos/{owner}/{repo}/git/matching-refs/*", handlers.ListMatchingRefs)
	r.Get("/api/v3/repos/{owner}/{repo}/git/matching-refs", handlers.ListMatchingRefs)
}

func registerIssueRoutes(r chi.Router, handlers *rest.Deps) {
	// Issues
	r.Get("/api/v3/repos/{owner}/{repo}/issues", handlers.ListIssues)
	r.Post("/api/v3/repos/{owner}/{repo}/issues", handlers.CreateIssue)
	r.Get("/api/v3/repos/{owner}/{repo}/issues/comments", handlers.ListRepoIssueComments)
	r.Get("/api/v3/repos/{owner}/{repo}/issues/comments/{comment_id}", handlers.GetIssueComment)
	r.Get("/api/v3/repos/{owner}/{repo}/issues/{number}", handlers.GetIssue)
	r.Patch("/api/v3/repos/{owner}/{repo}/issues/{number}", handlers.UpdateIssue)
	r.Get("/api/v3/repos/{owner}/{repo}/issues/{number}/comments", handlers.ListIssueComments)
	r.Post("/api/v3/repos/{owner}/{repo}/issues/{number}/comments", handlers.CreateIssueComment)
	r.Patch("/api/v3/repos/{owner}/{repo}/issues/comments/{comment_id}", handlers.UpdateIssueComment)
	r.Delete("/api/v3/repos/{owner}/{repo}/issues/comments/{comment_id}", handlers.DeleteIssueComment)
	r.Put("/api/v3/repos/{owner}/{repo}/issues/comments/{comment_id}/pin", handlers.PinIssueComment)
	r.Delete("/api/v3/repos/{owner}/{repo}/issues/comments/{comment_id}/pin", handlers.UnpinIssueComment)
	r.Put("/api/v3/repos/{owner}/{repo}/issues/{number}/lock", handlers.LockIssue)
	r.Delete("/api/v3/repos/{owner}/{repo}/issues/{number}/lock", handlers.UnlockIssue)
	r.Post("/api/v3/repos/{owner}/{repo}/issues/{number}/assignees", handlers.AddIssueAssignees)
	r.Delete("/api/v3/repos/{owner}/{repo}/issues/{number}/assignees", handlers.RemoveIssueAssignees)
	r.Get("/api/v3/repos/{owner}/{repo}/issues/{number}/timeline", handlers.GetIssueTimeline)
	r.Get("/api/v3/repos/{owner}/{repo}/issues/{number}/events", handlers.ListIssueEvents)
	r.Post("/api/v3/repos/{owner}/{repo}/issues/{number}/labels", handlers.AddIssueLabels)
	r.Get("/api/v3/repos/{owner}/{repo}/issues/{number}/reactions", handlers.ListIssueReactions)
	r.Post("/api/v3/repos/{owner}/{repo}/issues/{number}/reactions", handlers.CreateIssueReaction)
	r.Get("/api/v3/repos/{owner}/{repo}/issues/comments/{comment_id}/reactions", handlers.ListIssueReactions)
	r.Post("/api/v3/repos/{owner}/{repo}/issues/comments/{comment_id}/reactions", handlers.CreateIssueReaction)
	r.Delete("/api/v3/repos/{owner}/{repo}/issues/{number}/reactions/{reaction_id}", handlers.DeleteIssueReaction)
	r.Delete("/api/v3/repos/{owner}/{repo}/issues/comments/{comment_id}/reactions/{reaction_id}", handlers.DeleteIssueReaction)
	// Read receipts
	r.Post("/api/v3/repos/{owner}/{repo}/issues/{number}/read", handlers.MarkIssueRead)
	r.Get("/api/v3/repos/{owner}/{repo}/issues/{number}/read-state", handlers.GetIssueReadState)
	r.Get("/api/v3/repos/{owner}/{repo}/issues/{number}/participants/read-state", handlers.GetIssueParticipantsReadState)
	r.Get("/api/v3/repos/{owner}/{repo}/issues/{number}/unread-count", handlers.GetIssueUnreadCount)
}

func registerPullRequestRoutes(r chi.Router, handlers *rest.Deps) {
	// Pull Requests
	r.Get("/api/v3/repos/{owner}/{repo}/pulls", handlers.ListPRs)
	r.Post("/api/v3/repos/{owner}/{repo}/pulls", handlers.CreatePR)
	r.Get("/api/v3/repos/{owner}/{repo}/pulls/{number}", handlers.GetPR)
	r.Patch("/api/v3/repos/{owner}/{repo}/pulls/{number}", handlers.UpdatePR)
	r.Put("/api/v3/repos/{owner}/{repo}/pulls/{number}/update-branch", handlers.UpdatePRBranch)
	r.Put("/api/v3/repos/{owner}/{repo}/pulls/{number}/merge", handlers.MergePR)
	r.Get("/api/v3/repos/{owner}/{repo}/pulls/{number}/merge", handlers.GetPRMerged)
	r.Get("/api/v3/repos/{owner}/{repo}/pulls/comments/{comment_id}", handlers.GetPRComment)
	r.Get("/api/v3/repos/{owner}/{repo}/pulls/{number}/commits", handlers.ListPRCommits)
	r.Get("/api/v3/repos/{owner}/{repo}/pulls/{number}/files", handlers.ListPRFiles)
	r.Post("/api/v3/repos/{owner}/{repo}/pulls/{number}/requested_reviewers", handlers.AddRequestedReviewers)
	r.Delete("/api/v3/repos/{owner}/{repo}/pulls/{number}/requested_reviewers", handlers.RemoveRequestedReviewers)
	r.Get("/api/v3/repos/{owner}/{repo}/pulls/{number}/requested_reviewers", handlers.ListReviewRequests)
	// Compatibility: some clients call /repos/... without /api/v3 on the base host.
	r.Post("/repos/{owner}/{repo}/pulls/{number}/requested_reviewers", handlers.AddRequestedReviewers)
	r.Delete("/repos/{owner}/{repo}/pulls/{number}/requested_reviewers", handlers.RemoveRequestedReviewers)
	r.Get("/repos/{owner}/{repo}/pulls/{number}/requested_reviewers", handlers.ListReviewRequests)
	r.Get("/api/v3/repos/{owner}/{repo}/pulls/{number}/reviews", handlers.ListPRReviews)
	r.Post("/api/v3/repos/{owner}/{repo}/pulls/{number}/reviews", handlers.CreatePRReview)
	r.Post("/api/v3/repos/{owner}/{repo}/pulls/{number}/reviews/{review_id}/events", handlers.SubmitPRReview)
	r.Get("/api/v3/repos/{owner}/{repo}/pulls/{number}/comments", handlers.ListPRReviewComments)
	r.Post("/api/v3/repos/{owner}/{repo}/pulls/{number}/comments", handlers.CreatePRReviewComment)
	r.Post("/api/v3/repos/{owner}/{repo}/pulls/{number}/comments/{comment_id}/replies", handlers.ReplyToPRReviewComment)
	r.Patch("/api/v3/repos/{owner}/{repo}/pulls/comments/{comment_id}", handlers.UpdatePRReviewComment)
	r.Delete("/api/v3/repos/{owner}/{repo}/pulls/comments/{comment_id}", handlers.DeletePRReviewComment)
	r.Put("/api/v3/repos/{owner}/{repo}/pulls/{number}/comments/{comment_id}/resolve", handlers.ResolvePRReviewComment)
	r.Put("/api/v3/repos/{owner}/{repo}/pulls/{number}/comments/{comment_id}/unresolve", handlers.UnresolvePRReviewComment)
	r.Put("/api/v3/repos/{owner}/{repo}/pulls/{number}/ready_for_review", handlers.MarkPRReadyForReview)
	r.Get("/api/v3/repos/{owner}/{repo}/pulls/{number}/reviews/{review_id}", handlers.GetPRReview)
	r.Put("/api/v3/repos/{owner}/{repo}/pulls/{number}/reviews/{review_id}", handlers.UpdatePRReview)
	r.Get("/api/v3/repos/{owner}/{repo}/pulls/{number}/reviews/{review_id}/comments", handlers.ListReviewCommentsForReview)
	r.Put("/api/v3/repos/{owner}/{repo}/pulls/{number}/reviews/{review_id}/dismissals", handlers.DismissPRReview)
	r.Delete("/api/v3/repos/{owner}/{repo}/pulls/{number}/reviews/{review_id}", handlers.DeletePRReview)
}

func registerMergeUpstreamRoutes(r chi.Router, handlers *rest.Deps) {
	// Merge upstream (repo sync)
	r.Post("/api/v3/repos/{owner}/{repo}/merge-upstream", handlers.MergeUpstream)
}

func registerAutolinkRoutes(r chi.Router, handlers *rest.Deps) {
	// Autolinks
	r.Get("/api/v3/repos/{owner}/{repo}/autolinks", handlers.ListAutolinks)
	r.Post("/api/v3/repos/{owner}/{repo}/autolinks", handlers.CreateAutolink)
	r.Get("/api/v3/repos/{owner}/{repo}/autolinks/{autolink_id}", handlers.GetAutolink)
	r.Delete("/api/v3/repos/{owner}/{repo}/autolinks/{autolink_id}", handlers.DeleteAutolink)
}

func registerCheckRoutes(r chi.Router, handlers *rest.Deps) {
	// Check runs / check suites / status
	r.Get("/api/v3/repos/{owner}/{repo}/check-runs/{check_run_id}", handlers.GetCheckRun)
	r.Get("/api/v3/repos/{owner}/{repo}/check-runs/{check_run_id}/annotations", handlers.ListCheckRunAnnotations)
	r.Get("/api/v3/repos/{owner}/{repo}/commits/{ref}/check-runs", handlers.ListCheckRunsForRef)
	r.Get("/api/v3/repos/{owner}/{repo}/commits/{ref}/check-suites", handlers.ListCheckSuitesForRef)
	r.Post("/api/v3/repos/{owner}/{repo}/statuses/{sha}", handlers.CreateCommitStatus)
	r.Get("/api/v3/repos/{owner}/{repo}/commits/{ref}/statuses", handlers.ListCommitStatuses)
	r.Get("/api/v3/repos/{owner}/{repo}/commits/{ref}/status", handlers.CombinedStatus)
}

func registerWebhookRoutes(r chi.Router, handlers *rest.Deps) {
	// Webhooks
	r.Post("/api/v3/repos/{owner}/{repo}/hooks", handlers.CreateWebhook)
	r.Get("/api/v3/repos/{owner}/{repo}/hooks", handlers.ListWebhooks)
	r.Get("/api/v3/repos/{owner}/{repo}/hooks/{hook_id}", handlers.GetWebhook)
	r.Patch("/api/v3/repos/{owner}/{repo}/hooks/{hook_id}", handlers.UpdateWebhook)
	r.Delete("/api/v3/repos/{owner}/{repo}/hooks/{hook_id}", handlers.DeleteWebhook)
	r.Get("/api/v3/repos/{owner}/{repo}/hooks/{hook_id}/deliveries", handlers.ListWebhookDeliveries)
	r.Get("/api/v3/repos/{owner}/{repo}/hooks/{hook_id}/deliveries/{delivery_id}", handlers.GetWebhookDelivery)
	r.Post("/api/v3/repos/{owner}/{repo}/hooks/{hook_id}/deliveries/{delivery_id}/attempts", handlers.RedeliverWebhook)
}

func registerDeploymentRoutes(r chi.Router, handlers *rest.Deps) {
	// Deployments
	r.Post("/api/v3/repos/{owner}/{repo}/deployments", handlers.CreateDeployment)
	r.Get("/api/v3/repos/{owner}/{repo}/deployments", handlers.ListDeployments)
	r.Post("/api/v3/repos/{owner}/{repo}/deployments/{deployment_id}/statuses", handlers.CreateDeploymentStatus)
	r.Get("/api/v3/repos/{owner}/{repo}/deployments/{deployment_id}/statuses", handlers.ListDeploymentStatuses)
}

func registerBranchProtectionRoutes(r chi.Router, handlers *rest.Deps) {
	// Branch Protections
	// Use wildcard to capture branch names with slashes
	// Handler will parse the path to extract branch name and detect /protection suffix
	r.Post("/api/v3/repos/{owner}/{repo}/branches/*", handlers.PostBranchProtection)
	r.Put("/api/v3/repos/{owner}/{repo}/branches/*", handlers.UpdateBranchProtection)
	r.Patch("/api/v3/repos/{owner}/{repo}/branches/*", handlers.PatchBranchProtection)
	r.Delete("/api/v3/repos/{owner}/{repo}/branches/*", handlers.DeleteBranchProtection)
}

func registerDependabotAlertRoutes(r chi.Router, handlers *rest.Deps) {
	// Dependabot Alerts
	r.Get("/api/v3/repos/{owner}/{repo}/dependabot/alerts", handlers.ListDependabotAlerts)
	r.Get("/api/v3/repos/{owner}/{repo}/dependabot/alerts/{number}", handlers.GetDependabotAlert)
	r.Patch("/api/v3/repos/{owner}/{repo}/dependabot/alerts/{number}", handlers.UpdateDependabotAlert)
}

func registerRepoInvitationRoutes(r chi.Router, handlers *rest.Deps) {
	// Repository Invitations
	r.Get("/api/v3/repos/{owner}/{repo}/invitations", handlers.GetRepoInvitations)
	r.Put("/api/v3/repos/{owner}/{repo}/collaborators/{username}", handlers.AddCollaborator)
	r.Delete("/api/v3/repos/{owner}/{repo}/collaborators/{username}", handlers.RemoveCollaborator)
}

func registerRepoLabelRoutes(r chi.Router, handlers *rest.Deps) {
	// Labels
	r.Get("/api/v3/repos/{owner}/{repo}/labels", handlers.ListLabels)
	r.Post("/api/v3/repos/{owner}/{repo}/labels", handlers.CreateLabel)
	r.Get("/api/v3/repos/{owner}/{repo}/labels/{name}", handlers.GetLabel)
	r.Patch("/api/v3/repos/{owner}/{repo}/labels/{name}", handlers.EditLabel)
	r.Delete("/api/v3/repos/{owner}/{repo}/labels/{name}", handlers.DeleteLabel)
	r.Get("/api/v3/repos/{owner}/{repo}/issues/{issue_number}/labels", handlers.ListIssueLabels)
	r.Post("/api/v3/repos/{owner}/{repo}/issues/{issue_number}/labels", handlers.AddIssueLabels)
	r.Put("/api/v3/repos/{owner}/{repo}/issues/{issue_number}/labels", handlers.SetIssueLabels)
	r.Delete("/api/v3/repos/{owner}/{repo}/issues/{issue_number}/labels/{name}", handlers.RemoveIssueLabel)
	r.Delete("/api/v3/repos/{owner}/{repo}/issues/{issue_number}/labels", handlers.RemoveAllIssueLabels)
}

func registerRepoMilestoneRoutes(r chi.Router, handlers *rest.Deps) {
	// Milestones
	r.Get("/api/v3/repos/{owner}/{repo}/milestones", handlers.ListMilestones)
	r.Post("/api/v3/repos/{owner}/{repo}/milestones", handlers.CreateMilestone)
	r.Get("/api/v3/repos/{owner}/{repo}/milestones/{milestone_number}", handlers.GetMilestone)
	r.Patch("/api/v3/repos/{owner}/{repo}/milestones/{milestone_number}", handlers.UpdateMilestone)
	r.Delete("/api/v3/repos/{owner}/{repo}/milestones/{milestone_number}", handlers.DeleteMilestone)
	r.Get("/api/v3/repos/{owner}/{repo}/milestones/{milestone_number}/issues", handlers.ListMilestoneIssues)
	r.Get("/api/v3/repos/{owner}/{repo}/milestones/{milestone_number}/labels", handlers.ListMilestoneLabels)
}

func registerRepoDeployKeyRoutes(r chi.Router, handlers *rest.Deps) {
	// Deploy keys
	r.Get("/api/v3/repos/{owner}/{repo}/keys", handlers.ListDeployKeys)
	r.Post("/api/v3/repos/{owner}/{repo}/keys", handlers.CreateDeployKey)
	r.Delete("/api/v3/repos/{owner}/{repo}/keys/{key_id}", handlers.DeleteDeployKey)
}

func registerRepoReleaseRoutes(r chi.Router, handlers *rest.Deps) {
	// Releases
	r.Get("/api/v3/repos/{owner}/{repo}/releases", handlers.ListReleases)
	r.Post("/api/v3/repos/{owner}/{repo}/releases", handlers.CreateRelease)
	r.Get("/api/v3/repos/{owner}/{repo}/releases/tags/{tag}", handlers.GetReleaseByTag)
	r.Head("/api/v3/repos/{owner}/{repo}/releases/tags/{tag}", handlers.HeadReleaseByTag)
	r.Get("/api/v3/repos/{owner}/{repo}/releases/latest", handlers.GetLatestRelease)
	r.Post("/api/v3/repos/{owner}/{repo}/releases/generate-notes", handlers.GenerateReleaseNotes)
	r.Get("/api/v3/repos/{owner}/{repo}/contents", handlers.GetRepoContents)
	r.Get("/api/v3/repos/{owner}/{repo}/contents/*", handlers.GetRepoContents)
	r.Put("/api/v3/repos/{owner}/{repo}/contents/*", handlers.PutRepoContents)
	r.Delete("/api/v3/repos/{owner}/{repo}/contents/*", handlers.DeleteRepoContents)
	r.Get("/api/v3/repos/{owner}/{repo}/releases/{release_id}", handlers.GetRelease)
	r.Patch("/api/v3/repos/{owner}/{repo}/releases/{release_id}", handlers.UpdateRelease)
	r.Delete("/api/v3/repos/{owner}/{repo}/releases/{release_id}", handlers.DeleteRelease)
	// Release assets: upload, download, archive
	r.Post("/api/v3/repos/{owner}/{repo}/releases/{release_id}/assets", handlers.UploadReleaseAsset)
	r.Get("/api/v3/repos/{owner}/{repo}/releases/{release_id}/assets", handlers.ListReleaseAssets)
	r.Get("/api/v3/repos/{owner}/{repo}/releases/{release_id}/archive/{format}", handlers.DownloadReleaseArchive)
	r.Get("/api/v3/repos/{owner}/{repo}/releases/assets/{asset_id}", handlers.GetReleaseAsset)
	r.Get("/api/v3/repos/{owner}/{repo}/releases/assets/{asset_id}/download", handlers.DownloadReleaseAssetContent)
	r.Delete("/api/v3/repos/{owner}/{repo}/releases/assets/{asset_id}", handlers.DeleteReleaseAsset)
	// Archive downloads by tag ref (used by gh release download --archive=zip)
	r.Get("/api/v3/repos/{owner}/{repo}/archive/refs/tags/{tagfile}", handlers.DownloadArchiveByTag)
}

func registerActionsRoutes(r chi.Router, handlers *rest.Deps) {
	registerActionsVariableRoutes(r, handlers)
	registerActionsSecretRoutes(r, handlers)
	registerDependabotSecretRoutes(r, handlers)
	registerCodespacesSecretRoutes(r, handlers)
	registerEnvironmentRoutes(r, handlers)
	registerWorkflowRoutes(r, handlers)
	registerRepositoryDispatchRoutes(r, handlers)
	registerWorkflowRunRoutes(r, handlers)
	registerActionsCacheRoutes(r, handlers)
}

func registerActionsVariableRoutes(r chi.Router, handlers *rest.Deps) {
	// Actions: Variables (repo)
	r.Get("/api/v3/repos/{owner}/{repo}/actions/variables", handlers.ListRepoVariables)
	r.Post("/api/v3/repos/{owner}/{repo}/actions/variables", handlers.CreateRepoVariable)
	r.Get("/api/v3/repos/{owner}/{repo}/actions/variables/{name}", handlers.GetRepoVariable)
	r.Patch("/api/v3/repos/{owner}/{repo}/actions/variables/{name}", handlers.UpdateRepoVariable)
	r.Delete("/api/v3/repos/{owner}/{repo}/actions/variables/{name}", handlers.DeleteRepoVariable)

	// Actions: Variables (org)
	r.Get("/api/v3/orgs/{org}/actions/variables", handlers.ListOrgVariables)
	r.Post("/api/v3/orgs/{org}/actions/variables", handlers.CreateOrgVariable)
	r.Get("/api/v3/orgs/{org}/actions/variables/{name}", handlers.GetOrgVariable)
	r.Patch("/api/v3/orgs/{org}/actions/variables/{name}", handlers.UpdateOrgVariable)
	r.Delete("/api/v3/orgs/{org}/actions/variables/{name}", handlers.DeleteOrgVariable)

	// Actions: Variables (environment)
	r.Get("/api/v3/repos/{owner}/{repo}/environments/{environment_name}/variables", handlers.ListEnvVariables)
	r.Post("/api/v3/repos/{owner}/{repo}/environments/{environment_name}/variables", handlers.CreateEnvVariable)
	r.Get("/api/v3/repos/{owner}/{repo}/environments/{environment_name}/variables/{name}", handlers.GetEnvVariable)
	r.Patch("/api/v3/repos/{owner}/{repo}/environments/{environment_name}/variables/{name}", handlers.UpdateEnvVariable)
	r.Delete("/api/v3/repos/{owner}/{repo}/environments/{environment_name}/variables/{name}", handlers.DeleteEnvVariable)
}

// registerSecretRoutes registers repo and org secret CRUD endpoints for a given namespace.
// It does not include environment or user-specific routes.
func registerSecretRoutes(r chi.Router, handlers *rest.Deps, namespace string) {
	// Secrets (repo)
	r.Get("/api/v3/repos/{owner}/{repo}/"+namespace+"/secrets", handlers.ListRepoSecrets)
	r.Get("/api/v3/repos/{owner}/{repo}/"+namespace+"/secrets/public-key", handlers.GetRepoPublicKey)
	r.Get("/api/v3/repos/{owner}/{repo}/"+namespace+"/secrets/{name}", handlers.GetRepoSecret)
	r.Put("/api/v3/repos/{owner}/{repo}/"+namespace+"/secrets/{name}", handlers.CreateOrUpdateRepoSecret)
	r.Delete("/api/v3/repos/{owner}/{repo}/"+namespace+"/secrets/{name}", handlers.DeleteRepoSecret)

	// Secrets (org)
	r.Get("/api/v3/orgs/{org}/"+namespace+"/secrets", handlers.ListOrgSecrets)
	r.Get("/api/v3/orgs/{org}/"+namespace+"/secrets/public-key", handlers.GetOrgPublicKey)
	r.Get("/api/v3/orgs/{org}/"+namespace+"/secrets/{name}", handlers.GetOrgSecret)
	r.Put("/api/v3/orgs/{org}/"+namespace+"/secrets/{name}", handlers.CreateOrUpdateOrgSecret)
	r.Delete("/api/v3/orgs/{org}/"+namespace+"/secrets/{name}", handlers.DeleteOrgSecret)
	r.Get("/api/v3/orgs/{org}/"+namespace+"/secrets/{name}/repositories", handlers.GetOrgSecretRepos)
	r.Put("/api/v3/orgs/{org}/"+namespace+"/secrets/{name}/repositories", handlers.SetOrgSecretRepos)
}

func registerActionsSecretRoutes(r chi.Router, handlers *rest.Deps) {
	// Actions: Secrets (repo + org)
	registerSecretRoutes(r, handlers, "actions")

	// Actions: Secrets (environment)
	r.Get("/api/v3/repos/{owner}/{repo}/environments/{environment_name}/secrets", handlers.ListEnvSecrets)
	r.Get("/api/v3/repos/{owner}/{repo}/environments/{environment_name}/secrets/public-key", handlers.GetEnvPublicKey)
	r.Put("/api/v3/repos/{owner}/{repo}/environments/{environment_name}/secrets/{name}", handlers.CreateOrUpdateEnvSecret)
	r.Delete("/api/v3/repos/{owner}/{repo}/environments/{environment_name}/secrets/{name}", handlers.DeleteEnvSecret)
}

func registerDependabotSecretRoutes(r chi.Router, handlers *rest.Deps) {
	// Dependabot Secrets (repo + org)
	registerSecretRoutes(r, handlers, "dependabot")
}

func registerCodespacesSecretRoutes(r chi.Router, handlers *rest.Deps) {
	// Codespaces Secrets (repo + org)
	registerSecretRoutes(r, handlers, "codespaces")

	// Codespaces Secrets (user)
	r.Get("/api/v3/user/codespaces/secrets", handlers.ListUserCodespacesSecrets)
	r.Get("/api/v3/user/codespaces/secrets/public-key", handlers.GetUserCodespacesPublicKey)
	r.Get("/api/v3/user/codespaces/secrets/{name}", handlers.GetUserCodespacesSecret)
	r.Put("/api/v3/user/codespaces/secrets/{name}", handlers.CreateOrUpdateUserCodespacesSecret)
	r.Delete("/api/v3/user/codespaces/secrets/{name}", handlers.DeleteUserCodespacesSecret)
	r.Get("/api/v3/user/codespaces/secrets/{name}/repositories", handlers.GetUserCodespacesSecretRepos)
	r.Put("/api/v3/user/codespaces/secrets/{name}/repositories", handlers.SetUserCodespacesSecretRepos)
	r.Put("/api/v3/user/codespaces/secrets/{name}/repositories/{repository_id}", handlers.AddUserCodespacesSecretRepo)
	r.Delete("/api/v3/user/codespaces/secrets/{name}/repositories/{repository_id}", handlers.RemoveUserCodespacesSecretRepo)
}

func registerEnvironmentRoutes(r chi.Router, handlers *rest.Deps) {
	// Environments
	r.Get("/api/v3/repos/{owner}/{repo}/environments", handlers.ListEnvironments)
	r.Get("/api/v3/repos/{owner}/{repo}/environments/{environment_name}", handlers.GetEnvironment)
	r.Put("/api/v3/repos/{owner}/{repo}/environments/{environment_name}", handlers.CreateOrUpdateEnvironment)
	r.Delete("/api/v3/repos/{owner}/{repo}/environments/{environment_name}", handlers.DeleteEnvironment)
}

func registerWorkflowRoutes(r chi.Router, handlers *rest.Deps) {
	// Actions: Workflows
	r.Get("/api/v3/repos/{owner}/{repo}/actions/workflows", handlers.ListWorkflows)
	r.Get("/api/v3/repos/{owner}/{repo}/actions/workflows/{workflow_id}", handlers.GetWorkflow)
	r.Put("/api/v3/repos/{owner}/{repo}/actions/workflows/{workflow_id}/enable", handlers.EnableWorkflow)
	r.Put("/api/v3/repos/{owner}/{repo}/actions/workflows/{workflow_id}/disable", handlers.DisableWorkflow)
	r.Post("/api/v3/repos/{owner}/{repo}/actions/workflows/{workflow_id}/dispatches", handlers.DispatchWorkflow)
	r.Get("/api/v3/repos/{owner}/{repo}/actions/workflows/{workflow_id}/runs", handlers.ListWorkflowRunsByWorkflow)
}

func registerRepositoryDispatchRoutes(r chi.Router, handlers *rest.Deps) {
	// Dispatch (repository_dispatch)
	r.Post("/api/v3/repos/{owner}/{repo}/dispatches", handlers.CreateRepositoryDispatch)
}

func registerWorkflowRunRoutes(r chi.Router, handlers *rest.Deps) {
	// Actions: Workflow Runs
	r.Get("/api/v3/repos/{owner}/{repo}/actions/runs", handlers.ListWorkflowRuns)
	r.Get("/api/v3/repos/{owner}/{repo}/actions/runs/{run_id}", handlers.GetWorkflowRun)
	r.Post("/api/v3/repos/{owner}/{repo}/actions/runs/{run_id}/cancel", handlers.CancelWorkflowRun)
	r.Delete("/api/v3/repos/{owner}/{repo}/actions/runs/{run_id}", handlers.DeleteWorkflowRun)
	r.Post("/api/v3/repos/{owner}/{repo}/actions/runs/{run_id}/rerun", handlers.RerunWorkflowRun)
	r.Post("/api/v3/repos/{owner}/{repo}/actions/runs/{run_id}/rerun-failed-jobs", handlers.RerunWorkflowRun)
	r.Post("/api/v3/repos/{owner}/{repo}/actions/runs/{run_id}/force-cancel", handlers.ForceCancelWorkflowRun)
	r.Get("/api/v3/repos/{owner}/{repo}/actions/runs/{run_id}/logs", handlers.GetWorkflowRunLogs)
	r.Get("/api/v3/repos/{owner}/{repo}/actions/artifacts", handlers.ListRepoArtifacts)
	r.Get("/api/v3/repos/{owner}/{repo}/actions/runs/{run_id}/artifacts", handlers.ListWorkflowRunArtifacts)
	r.Get("/api/v3/repos/{owner}/{repo}/actions/runs/{run_id}/jobs", handlers.ListWorkflowRunJobs)
	r.Get("/api/v3/repos/{owner}/{repo}/actions/runs/{run_id}/attempts/{attempt_number}", handlers.GetWorkflowRunByAttempt)
	r.Get("/api/v3/repos/{owner}/{repo}/actions/runs/{run_id}/attempts/{attempt_number}/jobs", handlers.ListWorkflowRunJobsByAttempt)
	r.Get("/api/v3/repos/{owner}/{repo}/actions/runs/{run_id}/attempts/{attempt_number}/logs", handlers.GetWorkflowRunLogsByAttempt)
	r.Get("/api/v3/repos/{owner}/{repo}/actions/artifacts/{artifact_id}/zip", handlers.DownloadArtifact)
	r.Get("/api/v3/repos/{owner}/{repo}/actions/jobs/{job_id}", handlers.GetWorkflowJob)
	r.Get("/api/v3/repos/{owner}/{repo}/actions/jobs/{job_id}/logs", handlers.GetWorkflowJobLogs)
	r.Post("/api/v3/repos/{owner}/{repo}/actions/jobs/{job_id}/rerun", handlers.RerunWorkflowRunJob)
}

func registerActionsCacheRoutes(r chi.Router, handlers *rest.Deps) {
	// Actions: Cache
	r.Get("/api/v3/repos/{owner}/{repo}/actions/caches", handlers.ListActionsCaches)
	r.Delete("/api/v3/repos/{owner}/{repo}/actions/caches", handlers.DeleteActionsCaches)
	r.Delete("/api/v3/repos/{owner}/{repo}/actions/caches/{cache_id}", handlers.DeleteActionsCacheByID)
	r.Get("/api/v3/repos/{owner}/{repo}/actions/cache/usage", handlers.GetCacheUsage)
}

func registerRulesetRoutes(r chi.Router, handlers *rest.Deps) {
	// Rulesets
	r.Get("/api/v3/repos/{owner}/{repo}/rulesets", handlers.ListRulesets)
	r.Post("/api/v3/repos/{owner}/{repo}/rulesets", handlers.CreateRuleset)
	r.Get("/api/v3/repos/{owner}/{repo}/rulesets/{ruleset_id}", handlers.GetRuleset)
	r.Get("/api/v3/orgs/{org}/rulesets/{ruleset_id}", handlers.GetOrgRuleset)
	r.Get("/api/v3/repos/{owner}/{repo}/rules/branches/*", handlers.CheckBranchRules)
}

func registerLicenseRoutes(r chi.Router, handlers *rest.Deps) {
	// Licenses & Gitignore templates
	r.Get("/api/v3/licenses", handlers.ListLicenses)
	r.Get("/api/v3/licenses/{license}", handlers.GetLicense)
	r.Get("/api/v3/gitignore/templates", handlers.ListGitignoreTemplates)
	r.Get("/api/v3/gitignore/templates/{name}", handlers.GetGitignoreTemplate)
}

func registerSearchRoutes(r chi.Router, handlers *rest.Deps) {
	// Search
	r.Get("/api/v3/search/repositories", handlers.SearchRepos)
	r.Get("/api/v3/search/issues", handlers.SearchIssues)
	r.Get("/api/v3/search/commits", handlers.SearchCommits)
	r.Get("/api/v3/search/code", handlers.SearchCode)
	r.Get("/api/v3/search/labels", handlers.SearchLabels)
	r.Get("/api/v3/search/users", handlers.SearchUsers)
	r.Get("/api/v3/search/topics", handlers.SearchTopics)
}

func registerAppRoutes(r chi.Router, handlers *rest.Deps) {
	// App
	r.Get("/api/v3/app/installations", handlers.GetInstallations)
}

func registerEnvByRepoIDRoutes(r chi.Router, handlers *rest.Deps) {
	// Env via numeric repo ID  (gh variable set --env, gh secret set --env)
	r.Get("/api/v3/repositories/{repo_id}/environments", handlers.ListEnvironmentsByRepoID)
	r.Get("/api/v3/repositories/{repo_id}/environments/{environment_name}", handlers.GetEnvironmentByRepoID)
	r.Get("/api/v3/repositories/{repo_id}/environments/{environment_name}/variables", handlers.ListEnvVariablesByRepoID)
	r.Post("/api/v3/repositories/{repo_id}/environments/{environment_name}/variables", handlers.CreateEnvVariableByRepoID)
	r.Get("/api/v3/repositories/{repo_id}/environments/{environment_name}/variables/{name}", handlers.GetEnvVariableByRepoID)
	r.Patch("/api/v3/repositories/{repo_id}/environments/{environment_name}/variables/{name}", handlers.UpdateEnvVariableByRepoID)
	r.Delete("/api/v3/repositories/{repo_id}/environments/{environment_name}/variables/{name}", handlers.DeleteEnvVariableByRepoID)
	r.Get("/api/v3/repositories/{repo_id}/environments/{environment_name}/secrets", handlers.ListEnvSecretsByRepoID)
	r.Get("/api/v3/repositories/{repo_id}/environments/{environment_name}/secrets/public-key", handlers.GetEnvPublicKeyByRepoID)
	r.Put("/api/v3/repositories/{repo_id}/environments/{environment_name}/secrets/{name}", handlers.CreateOrUpdateEnvSecretByRepoID)
	r.Delete("/api/v3/repositories/{repo_id}/environments/{environment_name}/secrets/{name}", handlers.DeleteEnvSecretByRepoID)
	r.Put("/api/v3/repositories/{repo_id}/environments/{environment_name}", handlers.CreateOrUpdateEnvironmentByRepoID)
	r.Delete("/api/v3/repositories/{repo_id}/environments/{environment_name}", handlers.DeleteEnvironmentByRepoID)
}

func registerNotFoundHandler(r chi.Router) {
	// Catch-all: 404 for unknown routes
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			_, _ = fmt.Fprintf(w, `{"message":"Not Found","documentation_url":"https://docs.github.com/rest"}`)
			return
		}
		http.NotFound(w, req)
	})
}

// registerHostMux creates a host-aware multiplexer that rewrites
// api.github.localhost requests into the /api/v3/ form our router expects.
// go-gh builds URLs differently for github.localhost:
//
//	REST:    http://api.github.localhost/users/X   (no /api/v3/ prefix)
//	GraphQL: http://api.github.localhost/graphql
func registerHostMux(r chi.Router) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		host := req.Host
		// Strip port if present
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if strings.EqualFold(host, "api.github.localhost") {
			p := req.URL.Path
			if p == "/graphql" {
				req.URL.Path = "/api/graphql"
			} else if !strings.HasPrefix(p, "/api/") {
				req.URL.Path = "/api/v3" + p
			}
		}
		r.ServeHTTP(w, req)
	})
}
