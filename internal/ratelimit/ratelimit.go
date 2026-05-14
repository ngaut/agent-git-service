package ratelimit

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	compatibilityLimit  = 1000
	compatibilityWindow = time.Minute
)

type actorKey struct{}
type reportKey struct{}
type snapshotKey struct{}

// Resource identifies a GitHub rate-limit bucket.
type Resource string

const (
	ResourceActionsRunnerRegistration Resource = "actions_runner_registration"
	ResourceAuditLog                  Resource = "audit_log"
	ResourceAuditLogStreaming         Resource = "audit_log_streaming"
	ResourceCodeScanningAutofix       Resource = "code_scanning_autofix"
	ResourceCodeScanningUpload        Resource = "code_scanning_upload"
	ResourceCodeSearch                Resource = "code_search"
	ResourceCore                      Resource = "core"
	ResourceDependencySBOM            Resource = "dependency_sbom"
	ResourceDependencySnapshots       Resource = "dependency_snapshots"
	ResourceGraphQL                   Resource = "graphql"
	ResourceIntegrationManifest       Resource = "integration_manifest"
	ResourceSCIM                      Resource = "scim"
	ResourceSearch                    Resource = "search"
	ResourceSourceImport              Resource = "source_import"
)

var authenticatedResourceOrder = []Resource{
	ResourceCodeSearch,
	ResourceCore,
	ResourceGraphQL,
	ResourceIntegrationManifest,
	ResourceSearch,
	ResourceSourceImport,
	ResourceCodeScanningUpload,
	ResourceCodeScanningAutofix,
	ResourceActionsRunnerRegistration,
	ResourceSCIM,
	ResourceDependencySnapshots,
	ResourceDependencySBOM,
	ResourceAuditLog,
	ResourceAuditLogStreaming,
}

var anonymousResourceOrder = []Resource{
	ResourceCodeSearch,
	ResourceCore,
	ResourceGraphQL,
	ResourceIntegrationManifest,
	ResourceSearch,
}

type policy struct {
	limit  int
	window time.Duration
	bucket Resource
}

type bucketKey struct {
	actor    string
	resource Resource
}

type bucketState struct {
	resetUnix     int64
	windowSeconds int64
	used          int
}

type policySet map[Resource]policy

// Subject identifies the caller whose usage is being tracked.
type Subject struct {
	Actor         string
	Authenticated bool
}

// Snapshot is the GitHub-compatible rate-limit view surfaced on HTTP responses.
type Snapshot struct {
	Limit     int
	Used      int
	Remaining int
	Reset     int64
	Resource  Resource
}

// Report is the JSON payload returned by GET /api/v3/rate_limit.
type Report struct {
	Resources map[Resource]Snapshot
	Rate      Snapshot
}

// Limiter tracks per-actor usage within GitHub-like fixed windows.
type Limiter struct {
	mu            sync.Mutex
	authenticated policySet
	anonymous     policySet
	buckets       map[bucketKey]bucketState
	lastSweepUnix int64
}

// NewLimiter returns a simple limiter where tracked resources share the same
// fixed-window policy. It is primarily intended for focused tests.
func NewLimiter(limit int, window time.Duration) *Limiter {
	if limit <= 0 {
		limit = compatibilityLimit
	}
	if window <= 0 {
		window = compatibilityWindow
	}

	simple := policySet{
		ResourceCore:                {limit: limit, window: window, bucket: ResourceCore},
		ResourceSearch:              {limit: limit, window: window, bucket: ResourceSearch},
		ResourceCodeSearch:          {limit: limit, window: window, bucket: ResourceCodeSearch},
		ResourceGraphQL:             {limit: limit, window: window, bucket: ResourceGraphQL},
		ResourceIntegrationManifest: {limit: limit, window: window, bucket: ResourceIntegrationManifest},
		ResourceSourceImport:        {limit: limit, window: window, bucket: ResourceSourceImport},
	}
	return &Limiter{
		authenticated: simple,
		anonymous:     simple,
		buckets:       make(map[bucketKey]bucketState),
	}
}

// NewGitHubLimiter returns an in-memory limiter configured with GitHub-like
// primary rate-limit buckets.
func NewGitHubLimiter() *Limiter {
	return &Limiter{
		authenticated: authenticatedPolicies(),
		anonymous:     anonymousPolicies(),
		buckets:       make(map[bucketKey]bucketState),
	}
}

// CompatibilitySnapshot returns the authenticated core snapshot used when a
// handler is invoked without middleware in front of it.
func CompatibilitySnapshot(now time.Time) Snapshot {
	p := authenticatedPolicies()[ResourceCore]
	return snapshotForPolicy(p, ResourceCore, 0, now.UTC())
}

// SnapshotForContext returns the request-scoped snapshot when present so
// headers and handler payloads stay in sync for a single response.
func SnapshotForContext(ctx context.Context, now time.Time) Snapshot {
	if snapshot, ok := SnapshotFromContext(ctx); ok {
		return snapshot
	}
	return CompatibilitySnapshot(now)
}

// ReportForContext returns the request-scoped rate-limit report when present.
func ReportForContext(ctx context.Context, now time.Time) Report {
	if report, ok := reportFromContext(ctx); ok {
		return report
	}
	return NewGitHubLimiter().Report(Subject{Actor: "ip:compat", Authenticated: false}, now)
}

// WithSnapshot stores a rate-limit snapshot on the request context.
func WithSnapshot(ctx context.Context, snapshot Snapshot) context.Context {
	return context.WithValue(ctx, snapshotKey{}, snapshot)
}

// SnapshotFromContext loads the rate-limit snapshot from the request context.
func SnapshotFromContext(ctx context.Context) (Snapshot, bool) {
	snapshot, ok := ctx.Value(snapshotKey{}).(Snapshot)
	return snapshot, ok
}

// WithReport stores a rate-limit report on the request context.
func WithReport(ctx context.Context, report Report) context.Context {
	return context.WithValue(ctx, reportKey{}, report)
}

func reportFromContext(ctx context.Context) (Report, bool) {
	report, ok := ctx.Value(reportKey{}).(Report)
	return report, ok
}

// WithActor stores the authenticated actor used for per-token rate limiting.
func WithActor(ctx context.Context, actor string) context.Context {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return ctx
	}
	return context.WithValue(ctx, actorKey{}, actor)
}

// ActorFromContext loads the request actor set by authentication middleware.
func ActorFromContext(ctx context.Context) (string, bool) {
	actor, ok := ctx.Value(actorKey{}).(string)
	return actor, ok
}

// ActorForRequest resolves the limiter key for a request, preferring the
// authenticated actor and falling back to the client IP address.
func ActorForRequest(r *http.Request) string {
	if r == nil {
		return "ip:unknown"
	}
	if actor, ok := ActorFromContext(r.Context()); ok && strings.TrimSpace(actor) != "" {
		return actor
	}
	return "ip:" + clientIP(r)
}

// SubjectForRequest resolves the tracked caller and whether it is authenticated.
func SubjectForRequest(r *http.Request) Subject {
	actor := ActorForRequest(r)
	return Subject{
		Actor:         actor,
		Authenticated: strings.HasPrefix(actor, "token:"),
	}
}

// ResourceForRequest classifies the GitHub rate-limit bucket for an HTTP request.
func ResourceForRequest(r *http.Request) (Resource, bool) {
	if r == nil || r.URL == nil {
		return "", false
	}
	path := r.URL.Path
	switch {
	case path == "/api/graphql" || path == "/graphql":
		return ResourceGraphQL, true
	case path == "/api/v3/rate_limit":
		return ResourceCore, true
	case strings.HasPrefix(path, "/api/v3/search/code"):
		return ResourceCodeSearch, true
	case strings.HasPrefix(path, "/api/v3/search/"):
		return ResourceSearch, true
	case strings.HasPrefix(path, "/api/v3"):
		return ResourceCore, true
	default:
		return "", false
	}
}

// SetHeaders writes the GitHub-compatible rate-limit headers.
func SetHeaders(header http.Header, snapshot Snapshot) {
	header.Set("X-RateLimit-Limit", strconv.Itoa(snapshot.Limit))
	header.Set("X-RateLimit-Used", strconv.Itoa(snapshot.Used))
	header.Set("X-RateLimit-Remaining", strconv.Itoa(snapshot.Remaining))
	header.Set("X-RateLimit-Reset", strconv.FormatInt(snapshot.Reset, 10))
	if snapshot.Resource != "" {
		header.Set("X-RateLimit-Resource", string(snapshot.Resource))
	}
}

// ResourceBody returns the JSON shape used by GET /api/v3/rate_limit.
func (s Snapshot) ResourceBody() map[string]any {
	body := map[string]any{
		"limit":     s.Limit,
		"used":      s.Used,
		"remaining": s.Remaining,
		"reset":     s.Reset,
	}
	if s.Resource != "" {
		body["resource"] = string(s.Resource)
	}
	return body
}

// ResourcesBody returns the GitHub-compatible resources object.
func (r Report) ResourcesBody() map[string]any {
	body := make(map[string]any, len(r.Resources))
	for _, resource := range resourceOrderForReport(r.Resources) {
		if snapshot, ok := r.Resources[resource]; ok {
			body[string(resource)] = snapshot.ResourceBody()
		}
	}
	return body
}

// Inspect returns the current snapshot for resource without consuming quota.
func (l *Limiter) Inspect(subject Subject, resource Resource, now time.Time) Snapshot {
	if l == nil {
		return snapshotForPolicy(authenticatedPolicies()[ResourceCore], ResourceCore, 0, now.UTC())
	}

	p, ok := l.policyFor(subject, resource)
	if !ok {
		return Snapshot{Resource: resource}
	}
	now = now.UTC()

	if p.limit == 0 {
		return snapshotForPolicy(p, resource, 0, now)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweepLocked(now)
	key := bucketKey{actor: normalizeActor(subject.Actor), resource: p.bucket}
	state := l.currentStateLocked(key, p, now)
	l.buckets[key] = state
	return snapshotFromState(resource, p, state)
}

// Allow consumes cost units from the given resource bucket.
func (l *Limiter) Allow(subject Subject, resource Resource, now time.Time, cost int) (Snapshot, bool) {
	if l == nil {
		return snapshotForPolicy(authenticatedPolicies()[ResourceCore], ResourceCore, 0, now.UTC()), true
	}

	p, ok := l.policyFor(subject, resource)
	if !ok {
		return Snapshot{Resource: resource}, true
	}
	now = now.UTC()

	if cost <= 0 {
		cost = 1
	}

	if p.limit == 0 {
		return snapshotForPolicy(p, resource, 0, now), false
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweepLocked(now)
	key := bucketKey{actor: normalizeActor(subject.Actor), resource: p.bucket}
	state := l.currentStateLocked(key, p, now)
	if state.used+cost > p.limit {
		l.buckets[key] = state
		return snapshotFromState(resource, p, state), false
	}

	state.used += cost
	l.buckets[key] = state
	return snapshotFromState(resource, p, state), true
}

// Report returns the GitHub-compatible /rate_limit payload for a subject.
func (l *Limiter) Report(subject Subject, now time.Time) Report {
	policies := l.policiesFor(subject)
	resources := make(map[Resource]Snapshot, len(policies))
	for _, resource := range resourceOrder(policies) {
		resources[resource] = l.Inspect(subject, resource, now)
	}
	rate := resources[ResourceCore]
	if rate.Resource == "" {
		rate = Snapshot{Resource: ResourceCore}
	}
	return Report{
		Resources: resources,
		Rate:      rate,
	}
}

func (l *Limiter) policyFor(subject Subject, resource Resource) (policy, bool) {
	policies := l.policiesFor(subject)
	p, ok := policies[resource]
	return p, ok
}

func (l *Limiter) policiesFor(subject Subject) policySet {
	if l == nil {
		return authenticatedPolicies()
	}
	if subject.Authenticated {
		return l.authenticated
	}
	return l.anonymous
}

func (l *Limiter) currentStateLocked(key bucketKey, p policy, now time.Time) bucketState {
	state, ok := l.buckets[key]
	if !ok || now.Unix() >= state.resetUnix || state.windowSeconds != int64(p.window/time.Second) {
		return bucketState{
			resetUnix:     windowEnd(now, p.window).Unix(),
			windowSeconds: int64(p.window / time.Second),
			used:          0,
		}
	}
	return state
}

func (l *Limiter) sweepLocked(now time.Time) {
	nowUnix := now.Unix()
	if l.lastSweepUnix != 0 && nowUnix-l.lastSweepUnix < 60 {
		return
	}
	for key, state := range l.buckets {
		if state.resetUnix <= nowUnix {
			delete(l.buckets, key)
		}
	}
	l.lastSweepUnix = nowUnix
}

func snapshotForPolicy(p policy, resource Resource, used int, now time.Time) Snapshot {
	if p.window <= 0 {
		p.window = compatibilityWindow
	}
	return Snapshot{
		Limit:     p.limit,
		Used:      clampUsage(p.limit, used),
		Remaining: remaining(p.limit, used),
		Reset:     windowEnd(now, p.window).Unix(),
		Resource:  resource,
	}
}

func snapshotFromState(resource Resource, p policy, state bucketState) Snapshot {
	return Snapshot{
		Limit:     p.limit,
		Used:      clampUsage(p.limit, state.used),
		Remaining: remaining(p.limit, state.used),
		Reset:     state.resetUnix,
		Resource:  resource,
	}
}

func clampUsage(limit int, used int) int {
	if used < 0 {
		return 0
	}
	if limit >= 0 && used > limit {
		return limit
	}
	return used
}

func remaining(limit int, used int) int {
	if limit <= 0 {
		return 0
	}
	used = clampUsage(limit, used)
	if limit-used < 0 {
		return 0
	}
	return limit - used
}

func resourceOrder(policies policySet) []Resource {
	if len(policies) == 0 {
		return nil
	}
	switch {
	case len(policies) == len(authenticatedPolicies()):
		return authenticatedResourceOrder
	case len(policies) == len(anonymousPolicies()):
		return anonymousResourceOrder
	default:
		ordered := make([]Resource, 0, len(policies))
		for _, resource := range authenticatedResourceOrder {
			if _, ok := policies[resource]; ok {
				ordered = append(ordered, resource)
			}
		}
		for resource := range policies {
			found := false
			for _, existing := range ordered {
				if existing == resource {
					found = true
					break
				}
			}
			if !found {
				ordered = append(ordered, resource)
			}
		}
		return ordered
	}
}

func resourceOrderForReport(resources map[Resource]Snapshot) []Resource {
	if len(resources) == 0 {
		return nil
	}
	if _, ok := resources[ResourceSourceImport]; ok {
		return authenticatedResourceOrder
	}
	return anonymousResourceOrder
}

func authenticatedPolicies() policySet {
	return policySet{
		ResourceCore:                      {limit: 1000, window: time.Minute, bucket: ResourceCore},
		ResourceSearch:                    {limit: 300, window: time.Minute, bucket: ResourceSearch},
		ResourceGraphQL:                   {limit: 1000, window: time.Minute, bucket: ResourceGraphQL},
		ResourceIntegrationManifest:       {limit: 5000, window: time.Hour, bucket: ResourceIntegrationManifest},
		ResourceSourceImport:              {limit: 100, window: time.Minute, bucket: ResourceSourceImport},
		ResourceCodeScanningUpload:        {limit: 5000, window: time.Hour, bucket: ResourceCodeScanningUpload},
		ResourceCodeScanningAutofix:       {limit: 10, window: time.Minute, bucket: ResourceCodeScanningAutofix},
		ResourceActionsRunnerRegistration: {limit: 10000, window: time.Hour, bucket: ResourceActionsRunnerRegistration},
		ResourceSCIM:                      {limit: 15000, window: time.Hour, bucket: ResourceSCIM},
		ResourceDependencySnapshots:       {limit: 100, window: time.Minute, bucket: ResourceDependencySnapshots},
		ResourceDependencySBOM:            {limit: 100, window: time.Minute, bucket: ResourceDependencySBOM},
		ResourceAuditLog:                  {limit: 1750, window: time.Hour, bucket: ResourceAuditLog},
		ResourceAuditLogStreaming:         {limit: 15, window: time.Hour, bucket: ResourceAuditLogStreaming},
		ResourceCodeSearch:                {limit: 100, window: time.Minute, bucket: ResourceCodeSearch},
	}
}

func anonymousPolicies() policySet {
	return policySet{
		ResourceCore:                {limit: 100, window: time.Minute, bucket: ResourceCore},
		ResourceCodeSearch:          {limit: 10, window: time.Minute, bucket: ResourceCodeSearch},
		ResourceGraphQL:             {limit: 100, window: time.Minute, bucket: ResourceGraphQL},
		ResourceIntegrationManifest: {limit: 5000, window: time.Hour, bucket: ResourceIntegrationManifest},
		ResourceSearch:              {limit: 30, window: time.Minute, bucket: ResourceSearch},
	}
}

func normalizeActor(actor string) string {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return "ip:unknown"
	}
	return actor
}

func clientIP(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		if first, _, ok := strings.Cut(forwarded, ","); ok {
			forwarded = first
		}
		forwarded = strings.TrimSpace(forwarded)
		if forwarded != "" {
			return forwarded
		}
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	raw := strings.TrimSpace(r.RemoteAddr)
	if raw == "" {
		return "unknown"
	}
	return raw
}

func windowStart(now time.Time, window time.Duration) time.Time {
	if window <= 0 {
		window = compatibilityWindow
	}
	return now.UTC().Truncate(window)
}

func windowEnd(now time.Time, window time.Duration) time.Time {
	return windowStart(now, window).Add(window)
}
