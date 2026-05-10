package db

import (
	"path/filepath"
	"testing"
)

func TestSafePragmaRows(t *testing.T) {
	gdb := openSQLiteDB(t, filepath.Join(t.TempDir(), "safe-pragma.db"))

	tests := []struct {
		name    string
		pragma  string
		ident   string
		wantErr bool
	}{
		{"valid_index_list", "index_list", "teams", false},
		{"valid_index_info", "index_info", "idx_org_slug", false},
		{"injection_via_ident", "index_list", "teams); DROP TABLE teams;--", true},
		{"space_in_ident", "index_list", "teams teams", true},
		{"empty_ident", "index_list", "", true},
		{"empty_pragma", "", "teams", true},
		{"pragma_with_paren", "index_list()", "teams", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := safePragmaRows(gdb, tt.pragma, tt.ident)
			if tt.wantErr {
				if err == nil {
					_ = rows.Close()
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			_ = rows.Close()
		})
	}
}
