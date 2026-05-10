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

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
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
	return fmt.Sprintf("\"%x\"", sum)
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
	for _, existing := range header.Values("Vary") {
		for _, part := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(part), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
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
