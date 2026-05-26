package rest

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
)

// webhookJSON returns the standard API shape for a webhook
func webhookJSON(w db.Webhook) map[string]any {
	var events []string
	if w.EventsJSON != "" {
		if err := json.Unmarshal([]byte(w.EventsJSON), &events); err != nil {
			slog.Warn("failed to unmarshal EventsJSON", "error", err, "webhook_id", w.ID)
		}
	}
	var config map[string]string
	if w.ConfigJSON != "" {
		if err := json.Unmarshal([]byte(w.ConfigJSON), &config); err != nil {
			slog.Warn("failed to unmarshal ConfigJSON", "error", err, "webhook_id", w.ID)
		}
	}
	// Redact secret field in config for security
	if config != nil && config["secret"] != "" {
		config["secret"] = "********"
	}
	return map[string]any{
		"type":       "Repository",
		"id":         w.ID,
		"name":       w.Name,
		"active":     w.Active,
		"events":     events,
		"config":     config,
		"updated_at": w.UpdatedAt.Format(time.RFC3339),
		"created_at": w.CreatedAt.Format(time.RFC3339),
	}
}

// CreateWebhook handles POST /api/v3/repos/{owner}/{repo}/hooks
func (d *Deps) CreateWebhook(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	var body struct {
		Name   string            `json:"name"`
		Active *bool             `json:"active"`
		Events []string          `json:"events"`
		Config map[string]string `json:"config"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid JSON")
		return
	}

	name := body.Name
	if name == "" {
		name = "web"
	}
	active := true
	if body.Active != nil {
		active = *body.Active
	}
	events := body.Events
	if len(events) == 0 {
		events = []string{"push"}
	}
	eventsJSON, _ := json.Marshal(events)
	configJSON, _ := json.Marshal(body.Config)

	hook := db.Webhook{
		RepositoryID: repo.ID,
		Name:         name,
		Active:       active,
		EventsJSON:   string(eventsJSON),
		ConfigJSON:   string(configJSON),
	}
	if err := d.Svc.CreateWebhook(r.Context(), &hook); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	logErr(r.Context(), "CreateWebhook: ping", d.Svc.DispatchWebhookPing(r.Context(), hook, webhookPingPayload(r.Context(), *repo, hook)))
	respond.JSON(w, 201, webhookJSON(hook))
}

// ListWebhooks handles GET /api/v3/repos/{owner}/{repo}/hooks
func (d *Deps) ListWebhooks(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	hooks, err := d.Svc.ListWebhooks(r.Context(), repo.ID)
	if err != nil {
		respond.JSON(w, 200, []any{})
		return
	}
	var out []any
	for _, h := range hooks {
		out = append(out, webhookJSON(h))
	}
	if out == nil {
		out = []any{}
	}
	respond.JSON(w, 200, out)
}

// GetWebhook handles GET /api/v3/repos/{owner}/{repo}/hooks/{hook_id}
func (d *Deps) GetWebhook(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	hookID, ok := mustIntParam(w, r, "hook_id")
	if !ok {
		return
	}
	hook, err := d.Svc.GetWebhook(r.Context(), repo.ID, uint(hookID))
	if err != nil {
		respond.NotFound(w)
		return
	}
	respond.JSON(w, 200, webhookJSON(*hook))
}

// UpdateWebhook handles PATCH /api/v3/repos/{owner}/{repo}/hooks/{hook_id}
func (d *Deps) UpdateWebhook(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	hookID, ok := mustIntParam(w, r, "hook_id")
	if !ok {
		return
	}
	hook, err := d.Svc.GetWebhook(r.Context(), repo.ID, uint(hookID))
	if err != nil {
		respond.NotFound(w)
		return
	}

	var body struct {
		Active *bool             `json:"active"`
		Events []string          `json:"events"`
		Config map[string]string `json:"config"`
	}
	decodeBody(r, &body)

	if body.Active != nil {
		hook.Active = *body.Active
	}
	if len(body.Events) > 0 {
		b, _ := json.Marshal(body.Events)
		hook.EventsJSON = string(b)
	}
	if body.Config != nil {
		b, _ := json.Marshal(body.Config)
		hook.ConfigJSON = string(b)
	}

	if err := d.Svc.UpdateWebhook(r.Context(), hook); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, webhookJSON(*hook))
}

// DeleteWebhook handles DELETE /api/v3/repos/{owner}/{repo}/hooks/{hook_id}
func (d *Deps) DeleteWebhook(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	hookID, ok := mustIntParam(w, r, "hook_id")
	if !ok {
		return
	}
	if err := d.Svc.DeleteWebhook(r.Context(), repo.ID, uint(hookID)); err != nil {
		respond.NotFound(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListWebhookDeliveries handles GET /api/v3/repos/{owner}/{repo}/hooks/{hook_id}/deliveries
func (d *Deps) ListWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	hookID, ok := mustIntParam(w, r, "hook_id")
	if !ok {
		return
	}
	hook, err := d.Svc.GetWebhook(r.Context(), repo.ID, uint(hookID))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	deliveries, err := d.Svc.ListHookDeliveries(r.Context(), repo.ID, uint(hookID))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, 0, len(deliveries))
	for _, delivery := range deliveries {
		out = append(out, hookDeliveryJSON(delivery, hook))
	}
	if out == nil {
		out = []any{}
	}
	respond.JSON(w, 200, out)
}

// GetWebhookDelivery handles GET /api/v3/repos/{owner}/{repo}/hooks/{hook_id}/deliveries/{delivery_id}
func (d *Deps) GetWebhookDelivery(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	hookID, ok := mustIntParam(w, r, "hook_id")
	if !ok {
		return
	}
	deliveryID, ok := mustIntParam(w, r, "delivery_id")
	if !ok {
		return
	}
	hook, err := d.Svc.GetWebhook(r.Context(), repo.ID, uint(hookID))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	delivery, err := d.Svc.GetHookDelivery(r.Context(), repo.ID, uint(hookID), uint(deliveryID))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, hookDeliveryJSON(*delivery, hook))
}

// RedeliverWebhook handles POST /api/v3/repos/{owner}/{repo}/hooks/{hook_id}/deliveries/{delivery_id}/attempts
func (d *Deps) RedeliverWebhook(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	hookID, ok := mustIntParam(w, r, "hook_id")
	if !ok {
		return
	}
	deliveryID, ok := mustIntParam(w, r, "delivery_id")
	if !ok {
		return
	}
	hook, err := d.Svc.GetWebhook(r.Context(), repo.ID, uint(hookID))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	delivery, err := d.Svc.RedeliverHookDelivery(r.Context(), repo.ID, uint(hookID), uint(deliveryID))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, http.StatusAccepted, hookDeliveryJSON(*delivery, hook))
}
