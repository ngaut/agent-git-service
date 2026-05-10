package shared

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsSession(t *testing.T) {
	assert.True(t, IsSessionID("00000000-0000-0000-0000-000000000000"))
	assert.True(t, IsSessionID("e2fa49d2-f164-4a56-ab99-498090b8fcdf"))
	assert.True(t, IsSessionID("E2FA49D2-F164-4A56-AB99-498090B8FCDF"))

	assert.False(t, IsSessionID(""))
	assert.False(t, IsSessionID(" "))
	assert.False(t, IsSessionID("\n"))
	assert.False(t, IsSessionID("not-a-uuid"))
	assert.False(t, IsSessionID("000000000000000000000000000000000000"))
	assert.False(t, IsSessionID("00000000-0000-0000-0000-000000000000-extra"))
}

func TestParsePullRequestAgentSessionURL(t *testing.T) {
	tests := []struct {
		name          string
		url           string
		wantSessionID string
		wantErr       bool
	}{
		{
			name:          "valid",
			url:           "https://github.com/OWNER/REPO/pull/123/agent-sessions/e2fa49d2-f164-4a56-ab99-498090b8fcdf",
			wantSessionID: "e2fa49d2-f164-4a56-ab99-498090b8fcdf",
		},
		{
			name:    "invalid session id",
			url:     "https://github.com/OWNER/REPO/pull/123/agent-sessions/fff",
			wantErr: true,
		},
		{
			name:    "no session id, trailing slash",
			url:     "https://github.com/OWNER/REPO/pull/123/agent-sessions/",
			wantErr: true,
		},
		{
			name:    "no session id",
			url:     "https://github.com/OWNER/REPO/pull/123/agent-sessions",
			wantErr: true,
		},
		{
			name:    "invalid pr url",
			url:     "https://github.com/OWNER/REPO/issues/123",
			wantErr: true,
		},
		{
			name:    "empty",
			url:     "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionID, err := ParseSessionIDFromURL(tt.url)

			if tt.wantErr {
				require.Error(t, err)
				assert.Zero(t, sessionID)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantSessionID, sessionID)
		})
	}
}
