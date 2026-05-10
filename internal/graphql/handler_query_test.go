package graphql_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"gh-server/internal/graphql"
)

// =============================================================================
// MergeNestedData Tests
// =============================================================================

// TestMergeNestedData_BasicMerge tests basic nested data merge
func TestMergeNestedData_BasicMerge(t *testing.T) {
	dst := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"name": "test-repo",
				"id":   "repo-1",
			},
		},
	}
	src := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"description": "A test repository",
				"isPrivate":   false,
			},
		},
	}

	graphql.MergeNestedData(dst, src, "repository")

	repoData := dst["data"].(map[string]any)["repository"].(map[string]any)
	require.Equal(t, "test-repo", repoData["name"], "original name should be preserved")
	require.Equal(t, "repo-1", repoData["id"], "original id should be preserved")
	require.Equal(t, "A test repository", repoData["description"], "description should be merged")
	require.Equal(t, false, repoData["isPrivate"], "isPrivate should be merged")
}

// TestMergeNestedData_OverwriteExisting tests that src values overwrite dst values
func TestMergeNestedData_OverwriteExisting(t *testing.T) {
	dst := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"name":        "old-name",
				"description": "old description",
			},
		},
	}
	src := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"name":        "new-name",
				"description": "new description",
			},
		},
	}

	graphql.MergeNestedData(dst, src, "repository")

	repoData := dst["data"].(map[string]any)["repository"].(map[string]any)
	require.Equal(t, "new-name", repoData["name"], "name should be overwritten")
	require.Equal(t, "new description", repoData["description"], "description should be overwritten")
}

// TestMergeNestedData_NilDstData tests merge when dst data is nil
func TestMergeNestedData_NilDstData(t *testing.T) {
	dst := map[string]any{
		"data": nil,
	}
	src := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"name": "test-repo",
			},
		},
	}

	// Should not panic
	graphql.MergeNestedData(dst, src, "repository")
	// dst should remain unchanged
	require.Nil(t, dst["data"])
}

// TestMergeNestedData_NilSrcData tests merge when src data is nil
func TestMergeNestedData_NilSrcData(t *testing.T) {
	dst := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"name": "test-repo",
			},
		},
	}
	src := map[string]any{
		"data": nil,
	}

	// Should not panic
	graphql.MergeNestedData(dst, src, "repository")
	// dst should remain unchanged
	repoData := dst["data"].(map[string]any)["repository"].(map[string]any)
	require.Equal(t, "test-repo", repoData["name"])
}

// TestMergeNestedData_NilDstNested tests merge when dst nested data is nil
func TestMergeNestedData_NilDstNested(t *testing.T) {
	dst := map[string]any{
		"data": map[string]any{
			"repository": nil,
		},
	}
	src := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"name": "test-repo",
			},
		},
	}

	// Should not panic
	graphql.MergeNestedData(dst, src, "repository")
	// dst nested should remain nil
	require.Nil(t, dst["data"].(map[string]any)["repository"])
}

// TestMergeNestedData_NilSrcNested tests merge when src nested data is nil
func TestMergeNestedData_NilSrcNested(t *testing.T) {
	dst := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"name": "test-repo",
			},
		},
	}
	src := map[string]any{
		"data": map[string]any{
			"repository": nil,
		},
	}

	// Should not panic
	graphql.MergeNestedData(dst, src, "repository")
	// dst should remain unchanged
	repoData := dst["data"].(map[string]any)["repository"].(map[string]any)
	require.Equal(t, "test-repo", repoData["name"])
}

// TestMergeNestedData_DifferentKey tests merge with different key
func TestMergeNestedData_DifferentKey(t *testing.T) {
	dst := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"name": "test-repo",
			},
			"user": map[string]any{
				"login": "tester",
			},
		},
	}
	src := map[string]any{
		"data": map[string]any{
			"user": map[string]any{
				"name": "Test User",
			},
		},
	}

	graphql.MergeNestedData(dst, src, "user")

	userData := dst["data"].(map[string]any)["user"].(map[string]any)
	require.Equal(t, "tester", userData["login"], "login should be preserved")
	require.Equal(t, "Test User", userData["name"], "name should be merged")

	// repository should be unchanged
	repoData := dst["data"].(map[string]any)["repository"].(map[string]any)
	require.Equal(t, "test-repo", repoData["name"])
}

// TestMergeNestedData_NonExistentKey tests merge with non-existent key
func TestMergeNestedData_NonExistentKey(t *testing.T) {
	dst := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"name": "test-repo",
			},
		},
	}
	src := map[string]any{
		"data": map[string]any{
			"nonexistent": map[string]any{
				"field": "value",
			},
		},
	}

	// Should not panic
	graphql.MergeNestedData(dst, src, "nonexistent")
	// dst should remain unchanged
	repoData := dst["data"].(map[string]any)["repository"].(map[string]any)
	require.Equal(t, "test-repo", repoData["name"])
}

// TestMergeNestedData_EmptyMaps tests merge with empty maps
func TestMergeNestedData_EmptyMaps(t *testing.T) {
	dst := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{},
		},
	}
	src := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{},
		},
	}

	// Should not panic
	graphql.MergeNestedData(dst, src, "repository")
	// Both should remain empty
	repoData := dst["data"].(map[string]any)["repository"].(map[string]any)
	require.Len(t, repoData, 0)
}

// TestMergeNestedData_DeepNestedData tests merge with deeply nested structures
func TestMergeNestedData_DeepNestedData(t *testing.T) {
	dst := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"name": "test-repo",
				"owner": map[string]any{
					"login": "owner1",
				},
			},
		},
	}
	src := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"description": "A test repo",
				"owner": map[string]any{
					"name": "Owner Name",
				},
			},
		},
	}

	graphql.MergeNestedData(dst, src, "repository")

	repoData := dst["data"].(map[string]any)["repository"].(map[string]any)
	require.Equal(t, "test-repo", repoData["name"])
	require.Equal(t, "A test repo", repoData["description"])
	// Note: owner map is replaced, not merged
	ownerData := repoData["owner"].(map[string]any)
	require.Equal(t, "Owner Name", ownerData["name"])
}

// TestMergeNestedData_ArrayValues tests merge with array values
func TestMergeNestedData_ArrayValues(t *testing.T) {
	dst := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"name":   "test-repo",
				"labels": []any{"bug", "enhancement"},
			},
		},
	}
	src := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"description": "A test repo",
				"labels":      []any{"priority-high"},
			},
		},
	}

	graphql.MergeNestedData(dst, src, "repository")

	repoData := dst["data"].(map[string]any)["repository"].(map[string]any)
	require.Equal(t, "test-repo", repoData["name"])
	require.Equal(t, "A test repo", repoData["description"])
	// labels array is replaced
	labels := repoData["labels"].([]any)
	require.Len(t, labels, 1)
	require.Equal(t, "priority-high", labels[0])
}

// TestMergeNestedData_NoDataKey tests merge when data key doesn't exist
func TestMergeNestedData_NoDataKey(t *testing.T) {
	dst := map[string]any{
		"other": map[string]any{
			"repository": map[string]any{
				"name": "test-repo",
			},
		},
	}
	src := map[string]any{
		"other": map[string]any{
			"repository": map[string]any{
				"description": "A test repo",
			},
		},
	}

	// Should not panic
	graphql.MergeNestedData(dst, src, "repository")
	// dst should remain unchanged since there's no "data" key
	require.Nil(t, dst["data"])
}

// TestMergeNestedData_MultipleKeys tests multiple merge operations
func TestMergeNestedData_MultipleKeys(t *testing.T) {
	dst := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"name": "test-repo",
			},
			"user": map[string]any{
				"login": "tester",
			},
		},
	}

	src1 := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"description": "A test repo",
			},
		},
	}
	src2 := map[string]any{
		"data": map[string]any{
			"user": map[string]any{
				"name": "Test User",
			},
		},
	}

	graphql.MergeNestedData(dst, src1, "repository")
	graphql.MergeNestedData(dst, src2, "user")

	repoData := dst["data"].(map[string]any)["repository"].(map[string]any)
	require.Equal(t, "test-repo", repoData["name"])
	require.Equal(t, "A test repo", repoData["description"])

	userData := dst["data"].(map[string]any)["user"].(map[string]any)
	require.Equal(t, "tester", userData["login"])
	require.Equal(t, "Test User", userData["name"])
}

// =============================================================================
// MergeInto Tests
// =============================================================================

// TestMergeInto_BasicMerge tests basic top-level merge
func TestMergeInto_BasicMerge(t *testing.T) {
	dst := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"name": "test-repo",
			},
		},
	}
	src := map[string]any{
		"data": map[string]any{
			"viewer": map[string]any{
				"login": "tester",
			},
		},
	}

	graphql.MergeInto(dst, src)

	data := dst["data"].(map[string]any)
	require.Equal(t, "test-repo", data["repository"].(map[string]any)["name"])
	require.Equal(t, "tester", data["viewer"].(map[string]any)["login"])
}

// TestMergeInto_Overwrite tests that src values overwrite dst values
func TestMergeInto_Overwrite(t *testing.T) {
	dst := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"name": "old-name",
			},
		},
	}
	src := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"name": "new-name",
			},
		},
	}

	graphql.MergeInto(dst, src)

	data := dst["data"].(map[string]any)
	require.Equal(t, "new-name", data["repository"].(map[string]any)["name"])
}

// TestMergeInto_NilData tests merge when data is nil
func TestMergeInto_NilData(t *testing.T) {
	dst := map[string]any{
		"data": nil,
	}
	src := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"name": "test-repo",
			},
		},
	}

	// Should not panic
	graphql.MergeInto(dst, src)
	// dst data should remain nil
	require.Nil(t, dst["data"])
}

// TestMergeInto_NoDataKey tests merge when data key doesn't exist
func TestMergeInto_NoDataKey(t *testing.T) {
	dst := map[string]any{
		"other": map[string]any{},
	}
	src := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"name": "test-repo",
			},
		},
	}

	// Should not panic
	graphql.MergeInto(dst, src)
	// dst should remain unchanged
	require.Nil(t, dst["data"])
}

// TestMergeInto_EmptySrc tests merge with empty src
func TestMergeInto_EmptySrc(t *testing.T) {
	dst := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"name": "test-repo",
			},
		},
	}
	src := map[string]any{
		"data": map[string]any{},
	}

	graphql.MergeInto(dst, src)

	data := dst["data"].(map[string]any)
	require.Equal(t, "test-repo", data["repository"].(map[string]any)["name"])
}
