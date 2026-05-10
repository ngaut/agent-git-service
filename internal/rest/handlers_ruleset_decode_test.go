package rest

import (
	"encoding/json"
	"testing"
)

func TestDecodeRuleObject(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantKey string
		wantVal any
		wantErr bool
	}{
		{
			name:    "bare_object",
			raw:     `{"type":"required_status_checks","parameters":{"strict":true}}`,
			wantKey: "type",
			wantVal: "required_status_checks",
		},
		{
			name:    "legacy_stringified_object",
			raw:     `"{\"type\":\"branch_protection\"}"`,
			wantKey: "type",
			wantVal: "branch_protection",
		},
		{
			name:    "null_json",
			raw:     `null`,
			wantErr: true,
		},
		{
			name:    "number",
			raw:     `42`,
			wantErr: true,
		},
		{
			name:    "array",
			raw:     `[1,2,3]`,
			wantErr: true,
		},
		{
			name:    "string_but_not_object",
			raw:     `"plain text"`,
			wantErr: true,
		},
		{
			name:    "string_malformed_json",
			raw:     `"{not-valid}"`,
			wantErr: true,
		},
		{
			// Regression: previously returned a nil map without error; the
			// caller would then panic writing into the nil map.
			name:    "stringified_null",
			raw:     `"null"`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeRuleObject(json.RawMessage(tt.raw))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got[tt.wantKey] != tt.wantVal {
				t.Errorf("got[%q]=%v, want %v", tt.wantKey, got[tt.wantKey], tt.wantVal)
			}
		})
	}
}
