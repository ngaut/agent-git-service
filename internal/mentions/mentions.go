package mentions

import (
	"regexp"
	"strings"
)

var tokenRe = regexp.MustCompile(`(?:^|[^[:alnum:]_@])@([A-Za-z0-9](?:[A-Za-z0-9-]{0,38}))`)

// ExtractLogins returns distinct mentioned logins using GitHub-style token boundaries.
func ExtractLogins(body string) []string {
	matches := tokenRe.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	var logins []string
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		login := strings.ToLower(match[1])
		if _, exists := seen[login]; exists {
			continue
		}
		seen[login] = struct{}{}
		logins = append(logins, login)
	}
	return logins
}

// ContainsLogin reports whether body contains an exact mention token for login.
func ContainsLogin(body, login string) bool {
	login = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(login), "@"))
	if login == "" {
		return false
	}
	for _, candidate := range ExtractLogins(body) {
		if candidate == login {
			return true
		}
	}
	return false
}
