package graphql

import "fmt"

func gqlConn(nodes []any) map[string]any {
	edges := make([]any, len(nodes))
	for i, n := range nodes {
		edges[i] = map[string]any{"node": n}
	}
	return map[string]any{
		"totalCount": len(nodes),
		"nodes":      nodes,
		"edges":      edges,
		"pageInfo":   gqlPageInfo(),
	}
}

func gqlPageInfo() map[string]any {
	return map[string]any{"hasNextPage": false, "hasPreviousPage": false, "endCursor": "", "startCursor": ""}
}

func emptyConn() map[string]any { return gqlConn([]any{}) }

// gqlCountConn returns a connection with a specific totalCount but no nodes.
func gqlCountConn(count int) map[string]any {
	return map[string]any{
		"totalCount": count,
		"nodes":      []any{},
		"edges":      []any{},
		"pageInfo":   gqlPageInfo(),
	}
}

func gqlID(typ string, id uint) string {
	return fmt.Sprintf("%s_%d", typ, id)
}

func defaultReactionGroups() []any {
	reactions := []string{"THUMBS_UP", "THUMBS_DOWN", "LAUGH", "HOORAY", "CONFUSED", "HEART", "ROCKET", "EYES"}
	groups := make([]any, len(reactions))
	// We return empty groups here, but they should really be populated by the caller
	// if there are reactions. Since GQL doesn't strictly require 0 if missing,
	// returning 0 is okay for the 'default' empty state, but it shouldn't override real data.
	for i, r := range reactions {
		groups[i] = map[string]any{"content": r, "users": map[string]any{"totalCount": 0}}
	}
	return groups
}
