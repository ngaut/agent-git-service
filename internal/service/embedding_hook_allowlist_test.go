package service

import "testing"

func TestEmbeddingUpdateSQLAllowlist(t *testing.T) {
	allowed := []string{"issues", "pull_requests"}
	for _, table := range allowed {
		if _, ok := embeddingUpdateSQL[table]; !ok {
			t.Errorf("embeddingUpdateSQL: missing entry for %q", table)
		}
	}

	rejected := []string{
		"",
		"users",
		"issues;DROP TABLE users;--",
		"issues`",
		"ISSUES",
	}
	for _, table := range rejected {
		if _, ok := embeddingUpdateSQL[table]; ok {
			t.Errorf("embeddingUpdateSQL: unexpectedly accepts %q", table)
		}
	}
}
