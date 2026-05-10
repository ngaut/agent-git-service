package graphql

import (
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// extractFields parses a query and returns AST fields and fragments.
// Delegates to parseQuery for the actual parsing.
func extractFields(query string) (map[string]any, map[string]map[string]any) {
	fields, fragments, _ := parseQuery(query)
	return fields, fragments
}

func mergeSelections(target map[string]any, sel ast.SelectionSet) {
	for _, s := range sel {
		switch node := s.(type) {
		case *ast.Field:
			name := node.Name
			alias := node.Alias

			// We add both name and alias to the allowed fields set to ensure
			// filterMap keeps the field regardless of whether the backend
			// uses the alias or the original name in its response map.
			if len(node.SelectionSet) > 0 {
				child, ok := target[name].(map[string]any)
				if !ok {
					child = make(map[string]any)
					target[name] = child
				}
				mergeSelections(child, node.SelectionSet)

				if alias != "" && alias != name {
					aliasChild, ok := target[alias].(map[string]any)
					if !ok {
						aliasChild = make(map[string]any)
						target[alias] = aliasChild
					}
					mergeSelections(aliasChild, node.SelectionSet)
				}
			} else {
				target[name] = true
				if alias != "" && alias != name {
					target[alias] = true
				}
			}
		case *ast.FragmentSpread:
			target["..."+node.Name] = true
		case *ast.InlineFragment:
			mergeSelections(target, node.SelectionSet)
		}
	}
}

// parseQuery parses a GraphQL query string into the AST field map, fragment map,
// and __type introspection mappings (alias → type name) in a single pass.
func parseQuery(query string) (fields map[string]any, fragments map[string]map[string]any, typeIntrospections map[string]string) {
	fields = make(map[string]any)
	fragments = make(map[string]map[string]any)

	source := &ast.Source{Input: query}
	doc, err := parser.ParseQuery(source)
	if err != nil || doc == nil {
		return
	}

	for _, op := range doc.Operations {
		mergeSelections(fields, op.SelectionSet)
		// Extract __type introspections from top-level fields
		for _, sel := range op.SelectionSet {
			field, ok := sel.(*ast.Field)
			if !ok || field.Name != "__type" {
				continue
			}
			for _, arg := range field.Arguments {
				if arg.Name == "name" && arg.Value != nil {
					if typeIntrospections == nil {
						typeIntrospections = make(map[string]string)
					}
					alias := field.Alias
					if alias == "" {
						alias = arg.Value.Raw
					}
					typeIntrospections[alias] = arg.Value.Raw
				}
			}
		}
	}

	for _, frag := range doc.Fragments {
		fragMap := make(map[string]any)
		mergeSelections(fragMap, frag.SelectionSet)
		fragments[frag.Name] = fragMap
	}

	return
}

func filterMap(m map[string]any, fields map[string]any, fragments map[string]map[string]any) map[string]any {
	// Expand fragment spreads recursively so we have a flat set of allowed fields
	expanded := expandFragments(fields, fragments)

	out := make(map[string]any, len(m))
	for k, v := range m {
		node, allowed := expanded[k]
		if !allowed {
			continue
		}

		childFields, isMap := node.(map[string]any)
		switch val := v.(type) {
		case map[string]any:
			if isMap {
				out[k] = filterMap(val, childFields, fragments)
			} else {
				out[k] = val
			}
		case []any:
			filtered := make([]any, len(val))
			for i, elem := range val {
				if em, ok := elem.(map[string]any); ok && isMap {
					filtered[i] = filterMap(em, childFields, fragments)
				} else {
					filtered[i] = elem
				}
			}
			out[k] = filtered
		default:
			out[k] = v
		}
	}
	return out
}

// expandFragments recursively resolves all fragment spreads (`...fragName`)
// in a field set, merging fragment fields into the result.
func expandFragments(fields map[string]any, fragments map[string]map[string]any) map[string]any {
	result := make(map[string]any, len(fields))
	for k, v := range fields {
		if strings.HasPrefix(k, "...") {
			fragName := k[3:]
			if fragFields, ok := fragments[fragName]; ok {
				// Recursively expand this fragment (it may itself contain fragment spreads)
				expanded := expandFragments(fragFields, fragments)
				for fk, fv := range expanded {
					if _, exists := result[fk]; !exists {
						result[fk] = fv
					}
				}
			}
		} else {
			result[k] = v
		}
	}
	return result
}
