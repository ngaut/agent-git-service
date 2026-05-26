package graphql

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"gh-server/internal/db"
)

// logErr logs a non-nil error from a service call that would otherwise be swallowed.
func logErr(ctx context.Context, op string, err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	slog.ErrorContext(ctx, op, "error", err)
}

// sshHost returns the hostname from BaseURL for ssh URL generation.
func (s *Server) sshHost() string {
	if u, err := url.Parse(s.Svc.BaseURL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return "localhost"
}

// authorGQL returns the full author shape used by issue and PR GraphQL responses.
func (s *Server) authorGQL(u db.User) map[string]any {
	return map[string]any{
		"login":     u.Login,
		"id":        gqlID("User", u.ID),
		"name":      u.Name,
		"avatarUrl": fmt.Sprintf("%s/avatars/%s", s.Svc.BaseURL, u.Login),
	}
}

// inputMap extracts the "input" variable from a GraphQL request as a map.
func inputMap(req gqlRequest) map[string]any {
	if m, ok := req.Variables["input"].(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// intVar extracts an integer from a map, trying each key in order.
// Handles float64 (JSON default), int, and string representations.
func intVar(vars map[string]any, keys ...string) int {
	for _, k := range keys {
		switch v := vars[k].(type) {
		case float64:
			return int(v)
		case int:
			return v
		case string:
			if n, err := strconv.Atoi(v); err == nil {
				return n
			}
		}
	}
	return 0
}

// strFrom extracts a string from a map key, returning "" if missing or wrong type.
func strFrom(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// resolveRepo extracts owner and name from GraphQL variables,
// falling back from "name" to "repo" for the repository name.
func resolveRepo(vars map[string]any) (owner, name, fullName string) {
	owner = strFrom(vars, "owner")
	name = strFrom(vars, "name")
	if name == "" {
		name = strFrom(vars, "repo")
	}
	fullName = owner + "/" + name
	return
}

// resolveRepoByNodeID converts a "Repository_N" node ID to a full repo name.
func (s *Server) resolveRepoByNodeID(ctx context.Context, id string) string {
	id = normalizeNodeID(id)
	if !strings.HasPrefix(id, "Repository_") {
		return ""
	}
	repIDStr := strings.TrimPrefix(id, "Repository_")
	rep, err := s.Svc.GetRepoByID(ctx, repIDStr)
	if err != nil {
		return ""
	}
	return rep.FullName
}

func (s *Server) labelsToGQL(ctx context.Context, labels []db.Label) map[string]any {
	nodes := make([]any, len(labels))
	for i, l := range labels {
		nodes[i] = map[string]any{
			"id":          gqlID("Label", l.ID),
			"name":        l.Name,
			"description": l.Description,
			"color":       l.Color,
			"__typename":  "Label",
		}
	}
	return gqlConn(nodes)
}

func (s *Server) assigneeLoginsToGQL(ctx context.Context, logins string) map[string]any {
	if logins == "" {
		return emptyConn()
	}
	parts := strings.Split(logins, ",")
	resolved := s.Svc.GetUsersByLogins(ctx, parts)
	nodes := make([]any, 0, len(parts))
	for _, login := range parts {
		login = strings.TrimSpace(login)
		if login == "" {
			continue
		}
		if u, ok := resolved[login]; ok {
			nodes = append(nodes, map[string]any{
				"login":      u.Login,
				"id":         gqlID("User", u.ID),
				"name":       u.Name,
				"__typename": "User",
			})
		} else {
			nodes = append(nodes, map[string]any{
				"login":      login,
				"id":         "",
				"name":       login,
				"__typename": "User",
			})
		}
	}
	return gqlConn(nodes)
}

// queryHasAny is a fast heuristic used to prevent N+1 database storms.
// If the raw GraphQL query string `q` is empty, it assumes the field is requested (fallback).
// Otherwise, it checks if any of the provided `fields` are present in `q`.
func queryHasAny(q string, fields ...string) bool {
	if q == "" {
		return true // Fallback: always fetch if query string isn't provided
	}
	for _, f := range fields {
		if strings.Contains(q, f) {
			return true
		}
	}
	return false
}
