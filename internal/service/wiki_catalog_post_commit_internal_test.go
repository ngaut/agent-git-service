package service

import (
	"testing"

	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

func TestWikiChangeNeedsReferenceSync(t *testing.T) {
	tests := []struct {
		name            string
		ch              wikicatalog.ChangeResult
		recoveryPending bool
		want            bool
	}{
		{
			name: "create without references",
			ch: wikicatalog.ChangeResult{
				Op:                wikicatalog.OpUpsert,
				UpsertDisposition: wikicatalog.UpsertDispositionCreate,
			},
			want: false,
		},
		{
			name: "restore without references",
			ch: wikicatalog.ChangeResult{
				Op:                wikicatalog.OpUpsert,
				UpsertDisposition: wikicatalog.UpsertDispositionRestore,
			},
			want: true,
		},
		{
			name: "create with reference",
			ch: wikicatalog.ChangeResult{
				Op:                wikicatalog.OpUpsert,
				UpsertDisposition: wikicatalog.UpsertDispositionCreate,
			},
			recoveryPending: true,
			want:            true,
		},
		{
			name: "update without references",
			ch: wikicatalog.ChangeResult{
				Op:                wikicatalog.OpUpsert,
				UpsertDisposition: wikicatalog.UpsertDispositionUpdate,
			},
			want: true,
		},
		{
			name: "unknown upsert disposition",
			ch:   wikicatalog.ChangeResult{Op: wikicatalog.OpUpsert},
			want: true,
		},
		{
			name: "rename",
			ch:   wikicatalog.ChangeResult{Op: wikicatalog.OpRename},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wikiChangeNeedsReferenceSync(tt.ch, tt.recoveryPending); got != tt.want {
				t.Fatalf("wikiChangeNeedsReferenceSync() = %v, want %v", got, tt.want)
			}
		})
	}
}
