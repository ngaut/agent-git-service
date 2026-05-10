package rest

import (
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/rest/transform"
)

func init() {
	transform.Init("http://test.local")
}

// TestBranchProtectionJSON_MalformedJSON verifies that malformed JSON fields
// produce nil fallback without panicking — the "warn + zero-value fallback"
// contract introduced by the error-handling fix.
func TestBranchProtectionJSON_MalformedJSON(t *testing.T) {
	bp := db.BranchProtection{
		Repository:               db.Repository{FullName: "owner/repo"},
		BranchName:               "main",
		RequiredStatusChecksJSON: `{not valid json`,
		RequiredPullRequestJSON:  `[broken`,
		RestrictionsJSON:         `"unterminated`,
	}

	result := branchProtectionJSON(bp)

	assertEmptyOrNilMap(t, result["required_status_checks"], "required_status_checks")
	assertEmptyOrNilMap(t, result["required_pull_request_reviews"], "required_pull_request_reviews")
	assertEmptyOrNilMap(t, result["restrictions"], "restrictions")

	// Non-JSON fields should still be populated.
	if result["enforce_admins"] == nil {
		t.Error("expected enforce_admins to be populated")
	}
	url, _ := result["url"].(string)
	if url == "" {
		t.Error("expected url to be populated")
	}
}

// TestBranchProtectionJSON_EmptyJSON verifies that empty JSON strings produce
// zero-value fallback for the optional JSON fields.
func TestBranchProtectionJSON_EmptyJSON(t *testing.T) {
	bp := db.BranchProtection{
		Repository: db.Repository{FullName: "owner/repo"},
		BranchName: "main",
		// All JSON fields left as empty strings.
	}

	result := branchProtectionJSON(bp)

	assertEmptyOrNilMap(t, result["required_status_checks"], "required_status_checks")
	assertEmptyOrNilMap(t, result["required_pull_request_reviews"], "required_pull_request_reviews")
	assertEmptyOrNilMap(t, result["restrictions"], "restrictions")
}

// TestBranchProtectionJSON_ValidJSON verifies that valid JSON is correctly
// unmarshalled and returned (sanity check for the happy path).
func TestBranchProtectionJSON_ValidJSON(t *testing.T) {
	bp := db.BranchProtection{
		Repository:               db.Repository{FullName: "owner/repo"},
		BranchName:               "main",
		RequiredStatusChecksJSON: `{"strict":true}`,
		RequiredPullRequestJSON:  `{"dismiss_stale_reviews":false,"bypass_pull_request_allowances":{"users":["agent-1"]}}`,
		RestrictionsJSON:         `{"users":[],"teams":[]}`,
	}

	result := branchProtectionJSON(bp)

	if result["required_status_checks"] == nil {
		t.Error("expected required_status_checks to be non-nil for valid JSON")
	}
	if result["required_pull_request_reviews"] == nil {
		t.Error("expected required_pull_request_reviews to be non-nil for valid JSON")
	}
	if result["restrictions"] == nil {
		t.Error("expected restrictions to be non-nil for valid JSON")
	}
}

func TestValidateBranchProtectionBypassAllowances(t *testing.T) {
	if err := validateBranchProtectionBypassAllowances(map[string]any{
		"required_approving_review_count": float64(1),
		"bypass_pull_request_allowances": map[string]any{
			"users": []any{"agent-1"},
		},
	}); err != nil {
		t.Fatalf("expected user bypass actors to be accepted, got %v", err)
	}

	if err := validateBranchProtectionBypassAllowances(map[string]any{
		"bypass_pull_request_allowances": map[string]any{
			"teams": []any{"eng"},
		},
	}); err == nil {
		t.Fatal("expected team bypass actors to be rejected")
	}
}

func TestParseBranchProtectionPath(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		wantBranch   string
		wantResource string
		wantOK       bool
	}{
		{
			name:         "base protection endpoint",
			path:         "main/protection",
			wantBranch:   "main",
			wantResource: "",
			wantOK:       true,
		},
		{
			name:         "subresource endpoint",
			path:         "main/protection/required_status_checks",
			wantBranch:   "main",
			wantResource: "required_status_checks",
			wantOK:       true,
		},
		{
			name:         "branch contains protection segment",
			path:         "release/protection/hotfix/protection",
			wantBranch:   "release/protection/hotfix",
			wantResource: "",
			wantOK:       true,
		},
		{
			name:         "branch contains protection segment with subresource",
			path:         "release/protection/hotfix/protection/required_status_checks/contexts",
			wantBranch:   "release/protection/hotfix",
			wantResource: "required_status_checks/contexts",
			wantOK:       true,
		},
		{
			name:   "plain branch path is not a protection route",
			path:   "release/protection/hotfix",
			wantOK: false,
		},
		{
			name:   "non segment suffix is not a protection route",
			path:   "release/protectionary",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			branch, resource, ok := parseBranchProtectionPath(tt.path)
			if ok != tt.wantOK {
				t.Fatalf("ok: got %v, want %v (branch=%q resource=%q)", ok, tt.wantOK, branch, resource)
			}
			if !tt.wantOK {
				return
			}
			if branch != tt.wantBranch {
				t.Fatalf("branch: got %q, want %q", branch, tt.wantBranch)
			}
			if resource != tt.wantResource {
				t.Fatalf("resource: got %q, want %q", resource, tt.wantResource)
			}
		})
	}
}
