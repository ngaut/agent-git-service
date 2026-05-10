package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"

	"gh-server/internal/db"
)

const (
	webhookStatusPending     = "pending"
	webhookStatusOK          = "ok"
	webhookStatusFailed      = "failed"
	webhookStatusError       = "error"
	webhookQueueTimeout      = 5 * time.Second
	webhookRequestTimeout    = 10 * time.Second
	webhookResponseBodyLimit = 64 * 1024
	webhookWorkerCount       = 4
	webhookWorkerQueueSize   = 256
)

type webhookConfig struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	Secret      string `json:"secret"`
	InsecureSSL string `json:"insecure_ssl"`
}

type webhookJob struct {
	db         *gorm.DB
	deliveryID uint
	url        string
	headers    http.Header
	body       []byte
	insecure   bool
}

// CreateWebhook creates a new webhook for a repository.
func (s *Service) CreateWebhook(ctx context.Context, w *db.Webhook) error {
	return s.DBForCtx(ctx).Create(w).Error
}

// GetWebhook returns a webhook by ID for a repository.
func (s *Service) GetWebhook(ctx context.Context, repoID, hookID uint) (*db.Webhook, error) {
	var w db.Webhook
	err := s.DBForCtx(ctx).
		Where("repository_id = ? AND id = ?", repoID, hookID).
		First(&w).Error
	if err != nil {
		return nil, wrapErr(err)
	}
	return &w, nil
}

// UpdateWebhook updates an existing webhook.
func (s *Service) UpdateWebhook(ctx context.Context, w *db.Webhook) error {
	var existing db.Webhook
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND id = ?", w.RepositoryID, w.ID).
		First(&existing).Error; err != nil {
		return wrapErr(err)
	}
	return s.DBForCtx(ctx).Save(w).Error
}

// DeleteWebhook deletes a webhook by ID for a repository.
func (s *Service) DeleteWebhook(ctx context.Context, repoID, hookID uint) error {
	return s.DBForCtx(ctx).
		Where("repository_id = ? AND id = ?", repoID, hookID).
		Delete(&db.Webhook{}).Error
}

// ListWebhooks returns all webhooks for a repository.
func (s *Service) ListWebhooks(ctx context.Context, repoID uint) ([]db.Webhook, error) {
	var hooks []db.Webhook
	err := s.DBForCtx(ctx).
		Where("repository_id = ?", repoID).
		Find(&hooks).Error
	return hooks, err
}

// DispatchWebhookEvent fans out an event to all matching active repository hooks.
func (s *Service) DispatchWebhookEvent(ctx context.Context, repoID uint, event, action string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("webhook payload marshal: %w", err)
	}

	var hooks []db.Webhook
	if err := s.DBForCtx(ctx).
		Where("repository_id = ? AND active = ?", repoID, true).
		Find(&hooks).Error; err != nil {
		return err
	}

	for _, hook := range hooks {
		if !webhookSubscribed(hook.EventsJSON, event) {
			continue
		}
		if err := s.dispatchWebhookAttempt(ctx, hook, repoID, event, action, body, nil, "", false); err != nil {
			slog.WarnContext(ctx, "webhook dispatch enqueue failed", "hook_id", hook.ID, "event", event, "error", err)
		}
	}
	return nil
}

// DispatchWebhookPing sends the initial ping delivery for a newly created hook.
func (s *Service) DispatchWebhookPing(ctx context.Context, hook db.Webhook, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("webhook ping payload marshal: %w", err)
	}
	return s.dispatchWebhookAttempt(ctx, hook, hook.RepositoryID, "ping", "", body, nil, "", false)
}

// ListHookDeliveries returns all deliveries for a repository webhook.
func (s *Service) ListHookDeliveries(ctx context.Context, repoID, hookID uint) ([]db.HookDelivery, error) {
	var deliveries []db.HookDelivery
	err := s.DBForCtx(ctx).
		Where("repository_id = ? AND webhook_id = ?", repoID, hookID).
		Order("id desc").
		Find(&deliveries).Error
	return deliveries, err
}

// GetHookDelivery returns a single persisted delivery attempt.
func (s *Service) GetHookDelivery(ctx context.Context, repoID, hookID, deliveryID uint) (*db.HookDelivery, error) {
	var delivery db.HookDelivery
	err := s.DBForCtx(ctx).
		Where("repository_id = ? AND webhook_id = ? AND id = ?", repoID, hookID, deliveryID).
		First(&delivery).Error
	if err != nil {
		return nil, wrapErr(err)
	}
	return &delivery, nil
}

// RedeliverHookDelivery creates a new delivery attempt using the original payload.
func (s *Service) RedeliverHookDelivery(ctx context.Context, repoID, hookID, deliveryID uint) (*db.HookDelivery, error) {
	hook, err := s.GetWebhook(ctx, repoID, hookID)
	if err != nil {
		return nil, err
	}
	original, err := s.GetHookDelivery(ctx, repoID, hookID, deliveryID)
	if err != nil {
		return nil, err
	}

	headers := make(http.Header)
	if original.RequestHeaders != "" {
		if err := json.Unmarshal([]byte(original.RequestHeaders), &headers); err != nil {
			return nil, fmt.Errorf("webhook redelivery request headers: %w", err)
		}
	}

	cfg, err := parseWebhookConfig(hook.ConfigJSON)
	if err != nil {
		delivery := db.HookDelivery{
			WebhookID:        hook.ID,
			RepositoryID:     repoID,
			ParentDeliveryID: &original.ID,
			GUID:             original.GUID,
			Event:            original.Event,
			Action:           original.Action,
			Status:           webhookStatusError,
			Redelivery:       true,
			RequestHeaders:   original.RequestHeaders,
			RequestPayload:   original.RequestPayload,
			LastError:        err.Error(),
		}
		now := time.Now()
		delivery.DeliveredAt = &now
		if createErr := s.DBForCtx(ctx).Create(&delivery).Error; createErr != nil {
			return nil, createErr
		}
		return &delivery, nil
	}

	delivery := db.HookDelivery{
		WebhookID:        hook.ID,
		RepositoryID:     repoID,
		ParentDeliveryID: &original.ID,
		GUID:             original.GUID,
		Event:            original.Event,
		Action:           original.Action,
		Status:           webhookStatusPending,
		Redelivery:       true,
		RequestHeaders:   original.RequestHeaders,
		RequestPayload:   original.RequestPayload,
	}
	if err := s.DBForCtx(ctx).Create(&delivery).Error; err != nil {
		return nil, err
	}

	if err := s.enqueueWebhookJob(ctx, webhookJob{
		db:         s.webhookDB(ctx),
		deliveryID: delivery.ID,
		url:        cfg.URL,
		headers:    headers,
		body:       []byte(original.RequestPayload),
		insecure:   webhookConfigAllowsInsecureSSL(cfg.InsecureSSL),
	}); err != nil {
		return nil, err
	}
	return &delivery, nil
}

func (s *Service) dispatchWebhookAttempt(
	ctx context.Context,
	hook db.Webhook,
	repoID uint,
	event, action string,
	payload []byte,
	parent *db.HookDelivery,
	guid string,
	redelivery bool,
) error {
	if guid == "" {
		var err error
		guid, err = newWebhookGUID()
		if err != nil {
			return err
		}
	}

	cfg, cfgErr := parseWebhookConfig(hook.ConfigJSON)
	headers, requestBody := buildWebhookRequest(hook.ID, event, guid, payload, cfg.Secret, cfg.ContentType)

	delivery := db.HookDelivery{
		WebhookID:      hook.ID,
		RepositoryID:   repoID,
		GUID:           guid,
		Event:          event,
		Action:         action,
		Status:         webhookStatusPending,
		Redelivery:     redelivery,
		RequestHeaders: marshalWebhookHeaders(headers),
		RequestPayload: db.LargeText(requestBody),
	}
	if parent != nil {
		delivery.ParentDeliveryID = &parent.ID
	}
	if cfgErr != nil {
		delivery.Status = webhookStatusError
		delivery.LastError = cfgErr.Error()
		now := time.Now()
		delivery.DeliveredAt = &now
	}

	if err := s.DBForCtx(ctx).Create(&delivery).Error; err != nil {
		return err
	}
	if cfgErr != nil {
		return nil
	}

	return s.enqueueWebhookJob(ctx, webhookJob{
		db:         s.webhookDB(ctx),
		deliveryID: delivery.ID,
		url:        cfg.URL,
		headers:    headers,
		body:       requestBody,
		insecure:   webhookConfigAllowsInsecureSSL(cfg.InsecureSSL),
	})
}

func (s *Service) enqueueWebhookJob(ctx context.Context, job webhookJob) error {
	if s.Ctx == nil {
		s.processWebhookJob(context.Background(), job)
		return nil
	}

	s.ensureWebhookWorkers()
	s.Wg.Add(1)
	go func() {
		defer s.Wg.Done()
		select {
		case s.webhookJobs <- job:
		case <-s.ServerCtx().Done():
			s.completeWebhookError(job.db, job.deliveryID, "server shutting down")
		case <-time.After(webhookQueueTimeout):
			s.completeWebhookError(job.db, job.deliveryID, "webhook queue timeout")
		}
	}()
	return nil
}

func (s *Service) ensureWebhookWorkers() {
	s.webhookWorkersOnce.Do(func() {
		s.webhookJobs = make(chan webhookJob, webhookWorkerQueueSize)
		for i := 0; i < webhookWorkerCount; i++ {
			s.Wg.Add(1)
			go func() {
				defer s.Wg.Done()
				for {
					select {
					case <-s.ServerCtx().Done():
						return
					case job := <-s.webhookJobs:
						s.processWebhookJob(s.ServerCtx(), job)
					}
				}
			}()
		}
	})
}

func (s *Service) processWebhookJob(ctx context.Context, job webhookJob) {
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.url, bytes.NewReader(job.body))
	if err != nil {
		s.completeWebhookError(job.db, job.deliveryID, err.Error())
		return
	}
	for key, values := range job.headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	client := webhookHTTPClient(job.insecure)
	resp, err := client.Do(req)
	if err != nil {
		s.completeWebhookResult(job.db, job.deliveryID, webhookStatusError, 0, started, nil, err.Error(), err.Error())
		return
	}
	defer resp.Body.Close()

	responseBody := readWebhookBody(resp.Body, webhookResponseBodyLimit)
	status := webhookStatusOK
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status = webhookStatusFailed
	}
	s.completeWebhookResult(
		job.db,
		job.deliveryID,
		status,
		resp.StatusCode,
		started,
		resp.Header,
		string(responseBody),
		"",
	)
}

func webhookHTTPClient(insecure bool) *http.Client {
	client := &http.Client{Timeout: webhookRequestTimeout}
	if !insecure {
		return client
	}
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return client
	}
	transport := baseTransport.Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	transport.TLSClientConfig.InsecureSkipVerify = true
	client.Transport = transport
	return client
}

func (s *Service) completeWebhookError(database *gorm.DB, deliveryID uint, msg string) {
	s.completeWebhookResult(database, deliveryID, webhookStatusError, 0, time.Now(), nil, msg, msg)
}

func (s *Service) completeWebhookResult(
	database *gorm.DB,
	deliveryID uint,
	status string,
	statusCode int,
	started time.Time,
	headers http.Header,
	payload string,
	lastError string,
) {
	now := time.Now()
	updates := map[string]any{
		"status":           status,
		"status_code":      statusCode,
		"duration_millis":  now.Sub(started).Milliseconds(),
		"delivered_at":     &now,
		"response_headers": marshalWebhookHeaders(headers),
		"response_payload": db.LargeText(payload),
		"last_error":       lastError,
	}
	if err := database.WithContext(context.Background()).
		Model(&db.HookDelivery{}).
		Where("id = ?", deliveryID).
		Updates(updates).Error; err != nil {
		slog.Error("webhook delivery update failed", "delivery_id", deliveryID, "error", err)
	}
}

func (s *Service) webhookDB(ctx context.Context) *gorm.DB {
	if database, ok := DBFromContext(ctx); ok {
		return database
	}
	return s.DB
}

func parseWebhookConfig(raw string) (webhookConfig, error) {
	var cfg webhookConfig
	if raw == "" {
		return cfg, fmt.Errorf("webhook config missing")
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, fmt.Errorf("webhook config decode: %w", err)
	}
	cfg.URL = strings.TrimSpace(cfg.URL)
	if cfg.URL == "" {
		return cfg, fmt.Errorf("webhook config missing url")
	}
	if _, err := url.ParseRequestURI(cfg.URL); err != nil {
		return cfg, fmt.Errorf("webhook config invalid url: %w", err)
	}
	return cfg, nil
}

func webhookConfigAllowsInsecureSSL(raw string) bool {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "1", "true":
		return true
	default:
		return false
	}
}

func webhookSubscribed(eventsJSON, event string) bool {
	if event == "ping" {
		return true
	}
	var events []string
	if err := json.Unmarshal([]byte(eventsJSON), &events); err != nil {
		return false
	}
	for _, candidate := range events {
		candidate = strings.TrimSpace(strings.ToLower(candidate))
		if candidate == "*" || candidate == strings.ToLower(event) {
			return true
		}
	}
	return false
}

func buildWebhookRequest(hookID uint, event, guid string, payload []byte, secret, contentType string) (http.Header, []byte) {
	requestBody := payload
	headers := make(http.Header)
	headers.Set("User-Agent", fmt.Sprintf("GitHub-Hookshot/%d", hookID))
	headers.Set("X-GitHub-Event", event)
	headers.Set("X-GitHub-Delivery", guid)

	if strings.EqualFold(strings.TrimSpace(contentType), "form") {
		form := url.Values{}
		form.Set("payload", string(payload))
		requestBody = []byte(form.Encode())
		headers.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		headers.Set("Content-Type", "application/json")
	}

	if secret != "" {
		mac256 := hmac.New(sha256.New, []byte(secret))
		_, _ = mac256.Write(requestBody)
		headers.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac256.Sum(nil)))

		mac1 := hmac.New(sha1.New, []byte(secret))
		_, _ = mac1.Write(requestBody)
		headers.Set("X-Hub-Signature", "sha1="+hex.EncodeToString(mac1.Sum(nil)))
	}

	return headers, requestBody
}

func marshalWebhookHeaders(headers http.Header) string {
	if headers == nil {
		return "{}"
	}
	data, err := json.Marshal(headers)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func readWebhookBody(r io.Reader, limit int64) []byte {
	if r == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return []byte(err.Error())
	}
	if int64(len(body)) > limit {
		return body[:limit]
	}
	return body
}

func newWebhookGUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("webhook guid: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		raw[0:4],
		raw[4:6],
		raw[6:8],
		raw[8:10],
		raw[10:16],
	), nil
}
