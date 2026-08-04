package middleware

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
)

// ConditionalETag adds ETag / If-None-Match support to poll-heavy JSON
// endpoints consumed by the console frontend.
func ConditionalETag() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !shouldApplyConditionalETag(r) {
				next.ServeHTTP(w, r)
				return
			}

			rec := &bufferedResponseWriter{header: make(http.Header)}
			next.ServeHTTP(rec, r)

			status := rec.statusCode()
			header := rec.Header()
			body := rec.body.Bytes()

			if status == http.StatusOK && isJSONContentType(header.Get("Content-Type")) {
				etag := buildETag(body)
				header.Set("ETag", etag)
				appendVary(header, "Authorization")

				if ifNoneMatchMatches(r.Header.Get("If-None-Match"), etag) {
					header.Del("Content-Length")
					mergeHeader(w.Header(), header)
					w.WriteHeader(http.StatusNotModified)
					return
				}
			}

			mergeHeader(w.Header(), header)
			w.WriteHeader(status)
			if r.Method != http.MethodHead {
				_, _ = w.Write(body)
			}
		})
	}
}

type bufferedResponseWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *bufferedResponseWriter) Header() http.Header {
	return w.header
}

func (w *bufferedResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
}

func (w *bufferedResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func (w *bufferedResponseWriter) Flush() {}

func (w *bufferedResponseWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func shouldApplyConditionalETag(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if UsesWikiNavigationETag(r) {
		return false
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) >= 4 && parts[0] == "api" && parts[1] == "ext" && parts[2] == "v1" {
		return matchExtensionETagPath(parts[3:])
	}
	if len(parts) < 3 || parts[0] != "api" || parts[1] != "v3" {
		return false
	}

	switch parts[2] {
	case "notifications":
		return len(parts) == 3
	case "user":
		return matchUserETagPath(parts)
	case "users":
		return len(parts) == 4
	case "orgs":
		return matchOrgETagPath(parts)
	case "repos":
		return matchRepoETagPath(parts)
	case "search":
		return len(parts) == 4 && parts[3] == "repositories"
	default:
		return false
	}
}

// UsesWikiNavigationETag identifies live Wiki navigation responses whose
// representation is fully versioned by the Wiki head. Their handlers can
// answer If-None-Match before loading and serializing a large tree.
func UsesWikiNavigationETag(r *http.Request) bool {
	if r == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 8 || parts[0] != "api" || parts[1] != "ext" || parts[2] != "v1" || parts[3] != "repos" || parts[6] != "wiki" {
		return false
	}
	switch parts[7] {
	case "tree":
		return strings.TrimSpace(r.URL.Query().Get("ref")) == ""
	case "catalog":
		if hasCSVQueryValues(r.URL.Query().Get("labels")) || hasCSVQueryValues(r.URL.Query().Get("exclude_labels")) {
			return false
		}
		include := map[string]bool{}
		for _, raw := range strings.Split(r.URL.Query().Get("include"), ",") {
			name := strings.ToLower(strings.TrimSpace(raw))
			if name != "" {
				include[name] = true
			}
		}
		return include["tree"] && !include["pages"] && !include["labels"]
	default:
		return false
	}
}

func hasCSVQueryValues(raw string) bool {
	for _, value := range strings.Split(raw, ",") {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// BuildSemanticETag returns a weak validator for a versioned representation.
func BuildSemanticETag(r *http.Request, namespace, version string) string {
	sum := sha256.Sum256([]byte(namespace + "\x00" + version + "\x00" + r.URL.EscapedPath() + "\x00" + r.URL.Query().Encode()))
	return fmt.Sprintf("W/\"%x\"", sum)
}

// SetSemanticETag publishes a validator after the representation was built
// successfully.
func SetSemanticETag(w http.ResponseWriter, etag string) {
	w.Header().Set("ETag", etag)
	appendVary(w.Header(), "Authorization")
}

// WriteSemanticNotModified completes a matching conditional request with 304.
func WriteSemanticNotModified(w http.ResponseWriter, r *http.Request, etag string) bool {
	if !ifNoneMatchMatches(r.Header.Get("If-None-Match"), etag) {
		return false
	}
	SetSemanticETag(w, etag)
	w.Header().Del("Content-Length")
	w.WriteHeader(http.StatusNotModified)
	return true
}

func matchExtensionETagPath(parts []string) bool {
	switch {
	case len(parts) == 2 && parts[0] == "viewer" && parts[1] == "summary":
		return true
	case len(parts) == 2 && parts[0] == "notifications" && parts[1] == "summary":
		return true
	case len(parts) == 2 && parts[0] == "user" && (parts[1] == "agents" || parts[1] == "tokens"):
		return true
	case len(parts) == 3 && parts[0] == "orgs" && parts[2] == "management-summary":
		return true
	case len(parts) == 4 && parts[0] == "repos" && parts[3] == "summary":
		return true
	case len(parts) >= 5 && parts[0] == "repos" && parts[3] == "wiki":
		return true
	case len(parts) == 6 && parts[0] == "repos" && parts[3] == "issues" && parts[5] == "thread":
		return true
	default:
		return false
	}
}

func matchUserETagPath(parts []string) bool {
	switch len(parts) {
	case 3:
		return true
	case 4:
		switch parts[3] {
		case "repos", "orgs", "agents", "organization_invitations", "repository_invitations":
			return true
		}
	}
	return false
}

func matchOrgETagPath(parts []string) bool {
	switch len(parts) {
	case 4:
		return true
	case 5:
		switch parts[4] {
		case "repos", "teams", "outside_collaborators", "invitations", "members":
			return true
		}
	case 6:
		if parts[4] == "memberships" {
			return true
		}
		return parts[4] == "teams"
	case 7:
		return parts[4] == "teams" && (parts[6] == "members" || parts[6] == "repos")
	case 8:
		return parts[4] == "teams" && parts[6] == "memberships"
	}
	return false
}

func matchRepoETagPath(parts []string) bool {
	switch len(parts) {
	case 5:
		return true
	case 6:
		switch parts[5] {
		case "issues", "pulls", "labels", "milestones", "branches", "commits", "contributors", "releases", "collaborators", "invitations":
			return true
		}
	case 7:
		if parts[5] == "issues" {
			return true
		}
		return parts[5] == "actions" && (parts[6] == "workflows" || parts[6] == "runs")
	case 8:
		return parts[5] == "issues" && parts[7] == "comments"
	}
	return false
}

func isJSONContentType(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	return strings.HasPrefix(contentType, "application/json")
}

func buildETag(body []byte) string {
	sum := sha256.Sum256(body)
	// Compression is negotiated outside this middleware, so the same logical
	// response can have different wire bytes. A weak validator is valid across
	// those content codings while still serving GET/HEAD revalidation.
	return fmt.Sprintf("W/\"%x\"", sum)
}

func ifNoneMatchMatches(headerValue, candidate string) bool {
	for _, part := range strings.Split(headerValue, ",") {
		tag := strings.TrimSpace(part)
		if tag == "" {
			continue
		}
		if tag == "*" {
			return true
		}
		if weakETag(tag) == weakETag(candidate) {
			return true
		}
	}
	return false
}

func weakETag(tag string) string {
	tag = strings.TrimSpace(tag)
	if strings.HasPrefix(tag, "W/") {
		return strings.TrimSpace(tag[2:])
	}
	return tag
}

func appendVary(header http.Header, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}

	values := make([]string, 0, len(header.Values("Vary"))+1)
	for _, existing := range header.Values("Vary") {
		for _, part := range strings.Split(existing, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if part == "*" || strings.EqualFold(part, value) {
				return
			}
			values = append(values, part)
		}
	}
	if value == "*" {
		header.Set("Vary", value)
		return
	}
	values = append(values, value)
	header.Set("Vary", strings.Join(values, ", "))
}

func mergeHeader(dst, src http.Header) {
	for key, values := range src {
		if strings.EqualFold(key, "Vary") {
			for _, value := range values {
				for _, part := range strings.Split(value, ",") {
					part = strings.TrimSpace(part)
					if part != "" {
						appendVary(dst, part)
					}
				}
			}
			continue
		}
		dst[key] = append([]string(nil), values...)
	}
}
