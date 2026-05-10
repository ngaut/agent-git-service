package gitstore

import "testing"

// Regression: issue #1297 widened revRegex to include "/" and "-" so real
// branch names like "octoswarm/fix-2" pass IsValidRev. This test pins the
// character set against accidental future tightening AND verifies that
// flag-injection guards (leading "-" / "/") still hold.
func TestIsValidRev(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"sha", "abc123def456", true},
		{"branch_main", "main", true},
		{"branch_with_slash_and_hyphen", "octoswarm/fix-2", true},
		{"deep_namespace", "release/v1.2-rc1", true},
		{"version_tag", "v1.0.0", true},
		{"caret_parent", "HEAD^1", true},
		{"tilde_ancestor", "HEAD~3", true},
		{"empty", "", false},
		{"leading_dash", "-rf", false},
		{"leading_slash", "/etc/passwd", false},
		{"space", "bad name", false},
		{"backslash", "a\\b", false},
		{"at_brace", "main@{1}", false},
		{"unicode", "fëature", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValidRev(tc.in); got != tc.want {
				t.Errorf("IsValidRev(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsValidRefName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"simple_branch", "main", true},
		{"branch_with_slash", "refs/heads/feature", true},
		{"branch_with_hyphen", "feat-123", true},
		{"custom_namespace", "refs/locks/issue-42", true},
		{"version_tag", "v1.0.0", true},
		{"empty", "", false},
		{"leading_dash", "-exec", false},
		{"leading_dash_in_path", "refs/heads/-bad", true}, // leading "-" only on whole name
		{"leading_dot", ".hidden", false},
		{"leading_slash", "/main", false},
		{"trailing_slash", "main/", false},
		{"trailing_dot", "main.", false},
		{"dot_lock_suffix", "main.lock", false},
		{"double_dot", "refs/heads/..", false},
		{"embedded_double_dot", "refs/heads/foo..bar", false},
		{"space", "bad name", false},
		{"null_byte", "bad\x00name", false},
		{"backslash", "bad\\name", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValidRefName(tc.in); got != tc.want {
				t.Errorf("IsValidRefName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
