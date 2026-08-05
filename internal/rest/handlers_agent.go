package rest

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/ngaut/agent-git-service/internal/middleware"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/service"
)

// CreateAgent handles POST /api/ext/v1/agents (no auth).
func (d *Deps) CreateAgent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PrefixLogin     string `json:"prefix_login"`
		DefaultRepoName string `json:"default_repo_name"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	res, err := d.Svc.RegisterAgent(r.Context(), body.PrefixLogin, body.DefaultRepoName)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusCreated, map[string]any{
		"login":          res.Login,
		"token":          res.Token,
		"repo_full_name": res.RepoFullName,
	})
}

// CreateAgentInvite handles POST /api/ext/v1/agent-invites.
func (d *Deps) CreateAgentInvite(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RepoGrants []service.AgentInviteRepoGrant `json:"repo_grants"`
		TeamGrants []service.AgentInviteTeamGrant `json:"team_grants"`
	}
	if r.ContentLength > 0 {
		if err := decodeBodyStrict(r, &body); err != nil {
			respond.ValidationFailed(w, "invalid body")
			return
		}
	}
	invite, err := d.Svc.CreateAgentInvite(r.Context(), service.CreateAgentInviteInput{
		RepoGrants: body.RepoGrants,
		TeamGrants: body.TeamGrants,
	})
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	var repoGrants []service.AgentInviteRepoGrant
	_ = json.Unmarshal([]byte(invite.RepoGrantsJSON), &repoGrants)
	var teamGrants []service.AgentInviteTeamGrant
	_ = json.Unmarshal([]byte(invite.TeamGrantsJSON), &teamGrants)
	respond.JSON(w, http.StatusCreated, map[string]any{
		"invite_token": invite.Token,
		"repo_grants":  repoGrants,
		"team_grants":  teamGrants,
	})
}

// ConfirmAgentBinding handles POST /api/ext/v1/agent-bindings/confirm (agent auth).
func (d *Deps) ConfirmAgentBinding(w http.ResponseWriter, r *http.Request) {
	var body struct {
		InviteToken string `json:"invite_token"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	binding, err := d.Svc.ConfirmAgentBinding(r.Context(), body.InviteToken)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusCreated, map[string]any{
		"human_user_id": binding.HumanUserID,
		"agent_user_id": binding.AgentUserID,
		"bound_at":      binding.CreatedAt.UTC().Format(time.RFC3339),
	})
}

// ListBoundAgents handles GET /api/ext/v1/user/agents (human auth).
func (d *Deps) ListBoundAgents(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	agents, err := d.Svc.ListBoundAgents(r.Context(), u.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, 0, len(agents))
	for _, item := range agents {
		out = append(out, boundAgentJSON(item))
	}
	respond.JSON(w, http.StatusOK, out)
}

func boundAgentJSON(item service.BoundAgent) map[string]any {
	var tokenStatus any
	if item.TokenStatus.CreatedAt != nil {
		tokenStatus = map[string]any{
			"state":      item.TokenStatus.State,
			"created_at": item.TokenStatus.CreatedAt.UTC().Format(time.RFC3339),
		}
	} else {
		tokenStatus = map[string]any{"state": item.TokenStatus.State}
	}
	return map[string]any{
		"agent":        transform.User(item.Agent),
		"bound_at":     item.BoundAt.UTC().Format(time.RFC3339),
		"token_status": tokenStatus,
		"access_summary": map[string]any{
			"repos": item.AccessSummary.Repos,
			"teams": item.AccessSummary.Teams,
		},
	}
}

// RenameBoundAgent handles PATCH /api/ext/v1/agent-bindings/{agent_login}.
func (d *Deps) RenameBoundAgent(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	agentLogin := pathParam(r, "agent_login")
	agent, err := d.Svc.RenameBoundAgent(r.Context(), u.ID, agentLogin, body.Name)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"agent": transform.User(agent)})
}

// ResetAgentToken handles POST /api/ext/v1/agent-bindings/{agent_login}/reset-token.
func (d *Deps) ResetAgentToken(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	agentLogin := pathParam(r, "agent_login")
	tok, err := d.Svc.ResetAgentToken(r.Context(), u.ID, agentLogin)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{
		"agent_login": agentLogin,
		"token":       transform.Token(tok),
	})
}

// SwitchAgentSession handles POST /api/ext/v1/agent-bindings/{agent_login}/switch-session.
func (d *Deps) SwitchAgentSession(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	agentLogin := pathParam(r, "agent_login")
	res, err := d.Svc.CreateAgentSwitchSession(r.Context(), u.ID, agentLogin)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{
		"agent_login": agentLogin,
		"token":       transform.Token(res.Token),
		"user":        transform.UserPrivate(res.Agent),
	})
}

// RefreshAgentSwitchSession handles POST /api/ext/v1/agent-bindings/{agent_login}/refresh-session.
func (d *Deps) RefreshAgentSwitchSession(w http.ResponseWriter, r *http.Request) {
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	currentToken := middleware.ExtractToken(r)
	if currentToken == "" {
		respond.Unauthorized(w, "Bad credentials")
		return
	}
	agentLogin := pathParam(r, "agent_login")
	res, err := d.Svc.RefreshAgentSwitchSession(r.Context(), u.ID, currentToken, agentLogin)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{
		"agent_login": agentLogin,
		"token":       transform.Token(res.Token),
		"user":        transform.UserPrivate(res.Agent),
	})
}
