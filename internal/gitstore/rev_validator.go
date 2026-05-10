package gitstore

import (
	"errors"
	"regexp"
	"strings"
)

// ErrInvalidRefName is returned when a ref/tag/branch name fails IsValidRefName.
// Callers should map this to HTTP 400/422; it is distinct from ErrRefNotFound,
// which indicates the name is syntactically valid but no ref matches.
var ErrInvalidRefName = errors.New("invalid ref name")

// ErrInvalidSHA is returned when a string purporting to be a SHA fails
// plumbing.IsHash (i.e. is not a 40-char hex digest).
var ErrInvalidSHA = errors.New("invalid sha")

// revRegex defines the allowed character set for a git revision (branch, tag, sha).
// Includes "/" and "-" so full ref names like "octoswarm/fix-2" are accepted;
// leading "-" and "/" are rejected separately to prevent command-line flag
// injection and absolute-path-style arguments into git subprocesses.
var revRegex = regexp.MustCompile(`^[a-zA-Z0-9_\./\-\^~:]+$`)

// IsValidRev reports whether s is a safe, valid git revision string
// that contains no spaces or dangerous shell flag prefixes.
func IsValidRev(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "/") {
		return false
	}
	return revRegex.MatchString(s)
}

// refNameRegex whitelists the character set for a git ref name: ASCII letters,
// digits, underscore, hyphen (not leading), dot (not leading), and slash. The
// whitelist alone rejects NUL, spaces, backslash, and "@{"; additional checks
// below reject ".." sequences and trailing "/" / "." / ".lock".
var refNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_\-\./]*$`)

// IsValidRefName reports whether name is a safe ref name (branch, tag, or
// full refs/... path). Conservative subset of git-check-ref-format suitable
// for guarding values that flow into exec.CommandContext("git", ...).
func IsValidRefName(name string) bool {
	if name == "" || len(name) > 255 {
		return false
	}
	if !refNameRegex.MatchString(name) {
		return false
	}
	if strings.Contains(name, "..") {
		return false
	}
	if strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") {
		return false
	}
	return true
}
