package rest

import (
	"net/http"
	"time"

	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
)

// CreateAgent handles POST /api/v3/agents (no auth).
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

// CreateAgentInvite handles POST /api/v3/agent-invites.
func (d *Deps) CreateAgentInvite(w http.ResponseWriter, r *http.Request) {
	invite, err := d.Svc.CreateAgentInvite(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusCreated, map[string]any{
		"invite_token": invite.Token,
	})
}

// ConfirmAgentBinding handles POST /api/v3/agent-bindings/confirm (agent auth).
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

// ListBoundAgents handles GET /api/v3/user/agents (human auth).
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
		row := map[string]any{
			"agent":    transform.User(item.Agent),
			"bound_at": item.BoundAt.UTC().Format(time.RFC3339),
		}
		if item.Token.ID != 0 {
			row["token"] = transform.Token(item.Token)
		} else {
			row["token"] = nil
		}
		out = append(out, row)
	}
	respond.JSON(w, http.StatusOK, out)
}

// ResetAgentToken handles POST /api/v3/agent-bindings/{agent_login}/reset-token.
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
