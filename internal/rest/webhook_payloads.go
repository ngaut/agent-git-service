package rest

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/rest/transform"
	"gh-server/internal/service"
)

func webhookSender(ctx context.Context) any {
	sender, ok := service.UserFromContext(ctx)
	if !ok {
		return nil
	}
	return transform.User(sender)
}

func (d *Deps) webhookIssuePayload(ctx context.Context, issue db.Issue, action string) map[string]any {
	reactions, _ := d.Svc.CountReactions(ctx, issue.ID, 0)
	return map[string]any{
		"action":     action,
		"issue":      transform.Issue(issue, d.userResolver(ctx), d.authorAssociationChecks(ctx, issue.Repository), transform.IssueCounts{Comments: d.Svc.CountIssueComments(ctx, issue.RepositoryID, issue.Number), Reactions: reactions}),
		"repository": transform.Repo(issue.Repository),
		"sender":     webhookSender(ctx),
	}
}

func (d *Deps) webhookIssueCommentPayload(ctx context.Context, comment db.IssueComment, action string) map[string]any {
	reactions, _ := d.Svc.CountReactions(ctx, 0, comment.ID)
	payload := map[string]any{
		"action":     action,
		"comment":    transform.IssueComment(comment, d.authorAssociationChecks(ctx, comment.Repository), reactions),
		"repository": transform.Repo(comment.Repository),
		"sender":     webhookSender(ctx),
	}

	if pr, err := d.Svc.GetPR(ctx, comment.Repository.FullName, comment.IssueNumber); err == nil {
		payload["issue"] = transform.IssueFromPR(pr, d.userResolver(ctx), d.authorAssociationChecks(ctx, pr.Repository), d.Svc.CountPRComments(ctx, pr.RepositoryID, pr.Number))
		return payload
	}
	if issue, err := d.Svc.GetIssue(ctx, comment.Repository.FullName, comment.IssueNumber); err == nil {
		issueReactions, _ := d.Svc.CountReactions(ctx, issue.ID, 0)
		payload["issue"] = transform.Issue(issue, d.userResolver(ctx), d.authorAssociationChecks(ctx, issue.Repository), transform.IssueCounts{Comments: d.Svc.CountIssueComments(ctx, issue.RepositoryID, issue.Number), Reactions: issueReactions})
	}
	return payload
}

func (d *Deps) webhookPRPayload(rctx context.Context, pr db.PullRequest, action string, prJSON map[string]any) map[string]any {
	return map[string]any{
		"action":       action,
		"number":       pr.Number,
		"pull_request": prJSON,
		"repository":   transform.Repo(pr.Repository),
		"sender":       webhookSender(rctx),
	}
}

func webhookPingPayload(ctx context.Context, repo db.Repository, hook db.Webhook) map[string]any {
	return map[string]any{
		"zen":        "Approachable is better than simple.",
		"hook_id":    hook.ID,
		"hook":       webhookJSON(hook),
		"repository": transform.Repo(repo),
		"sender":     webhookSender(ctx),
	}
}

func hookDeliveryJSON(delivery db.HookDelivery, hook *db.Webhook) map[string]any {
	var requestHeaders map[string][]string
	var responseHeaders map[string][]string
	if delivery.RequestHeaders != "" {
		_ = json.Unmarshal([]byte(delivery.RequestHeaders), &requestHeaders)
	}
	if delivery.ResponseHeaders != "" {
		_ = json.Unmarshal([]byte(delivery.ResponseHeaders), &responseHeaders)
	}
	var requestPayload any = map[string]any{}
	if len(delivery.RequestPayload) != 0 {
		requestPayload = string(delivery.RequestPayload)
		var decoded any
		if err := json.Unmarshal([]byte(delivery.RequestPayload), &decoded); err == nil {
			requestPayload = decoded
		}
	}
	var responsePayload any = string(delivery.ResponsePayload)
	if len(delivery.ResponsePayload) == 0 {
		responsePayload = ""
	}
	var hookConfig map[string]string
	if hook != nil && hook.ConfigJSON != "" {
		_ = json.Unmarshal([]byte(hook.ConfigJSON), &hookConfig)
	}

	var deliveredAt any
	if delivery.DeliveredAt != nil {
		deliveredAt = delivery.DeliveredAt.Format(time.RFC3339)
	}
	var statusCode any
	if delivery.StatusCode != 0 {
		statusCode = delivery.StatusCode
	}
	var action any
	if delivery.Action != "" {
		action = delivery.Action
	}

	return map[string]any{
		"id":              delivery.ID,
		"guid":            delivery.GUID,
		"delivered_at":    deliveredAt,
		"redelivery":      delivery.Redelivery,
		"duration":        float64(delivery.DurationMillis) / 1000,
		"status":          strings.ToUpper(delivery.Status),
		"status_code":     statusCode,
		"event":           delivery.Event,
		"action":          action,
		"installation_id": nil,
		"repository_id":   delivery.RepositoryID,
		"url":             hookConfig["url"],
		"throttled_at":    nil,
		"request": map[string]any{
			"headers": requestHeaders,
			"payload": requestPayload,
		},
		"response": map[string]any{
			"headers": responseHeaders,
			"payload": responsePayload,
		},
	}
}
