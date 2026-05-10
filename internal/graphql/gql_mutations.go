package graphql

import (
	"context"
	"encoding/base64"
	"strconv"
	"strings"
)

// extractStringSlice normalizes GraphQL list inputs that may arrive as []any
// (decoded JSON) or []string (tests and internal callers).
func extractStringSlice(m map[string]any, key string) []string {
	switch raw := m[key].(type) {
	case []string:
		out := make([]string, 0, len(raw))
		for _, s := range raw {
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(raw))
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func normalizeNodeID(nodeID string) string {
	if nodeID == "" {
		return ""
	}
	if strings.Contains(nodeID, "_") {
		return nodeID
	}
	decoded, err := base64.StdEncoding.DecodeString(nodeID)
	if err != nil {
		return nodeID
	}
	return string(decoded)
}

// resolveUserLoginByNodeID resolves a User_N or Organization_N node ID to a login.
func (s *Server) resolveUserLoginByNodeID(ctx context.Context, nodeID string) string {
	nodeID = normalizeNodeID(nodeID)
	if !strings.HasPrefix(nodeID, "User_") && !strings.HasPrefix(nodeID, "Organization_") {
		return ""
	}
	parts := strings.SplitN(nodeID, "_", 2)
	if len(parts) != 2 {
		return ""
	}
	u, err := s.Svc.GetUserByID(ctx, parts[1])
	if err != nil {
		return ""
	}
	return u.Login
}

// resolveOwnerLogin extracts the owner login from a mutation input, falling
// back to "testorg" when no ownerId is present (project creation).
func (s *Server) resolveOwnerLogin(ctx context.Context, inp map[string]any) string {
	if ownerID := strFrom(inp, "ownerId"); ownerID != "" {
		if login := s.resolveUserLoginByNodeID(ctx, ownerID); login != "" {
			return login
		}
	}
	return "testorg"
}

// parseNodeID extracts the numeric DB ID from a "Prefix_N" node ID string.
func parseNodeID(id, prefix string) uint {
	id = normalizeNodeID(id)
	if strings.HasPrefix(id, prefix+"_") {
		n, _ := strconv.Atoi(strings.TrimPrefix(id, prefix+"_"))
		return uint(n)
	}
	return 0
}
