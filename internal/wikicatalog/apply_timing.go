package wikicatalog

import (
	"context"
	"time"
)

// ApplyPhase identifies one measured portion of a catalog changeset apply.
type ApplyPhase string

const (
	ApplyPhaseBlobUpload          ApplyPhase = "blob_upload"
	ApplyPhaseTransaction         ApplyPhase = "transaction"
	ApplyPhaseTransactionBody     ApplyPhase = "transaction_body"
	ApplyPhaseTransactionBoundary ApplyPhase = "transaction_boundary"
	ApplyPhaseChangesetInsert     ApplyPhase = "changeset_insert"
	ApplyPhaseHeadCAS             ApplyPhase = "head_cas"
	ApplyPhaseChanges             ApplyPhase = "changes"
	ApplyPhasePageWrite           ApplyPhase = "page_write"
	ApplyPhaseRevisionInsert      ApplyPhase = "revision_insert"
	ApplyPhaseBlobRefs            ApplyPhase = "blob_refs"
	ApplyPhaseOutlinks            ApplyPhase = "outlinks"
	ApplyPhaseInboundLinks        ApplyPhase = "inbound_links"
	ApplyPhaseLabelMutation       ApplyPhase = "label_mutation"
	ApplyPhasePendingBlobCleanup  ApplyPhase = "pending_blob_cleanup"
	ApplyPhaseCommitBarrier       ApplyPhase = "commit_barrier"
	ApplyPhasePostCommit          ApplyPhase = "post_commit"
)

type applyPhaseObserver func(phase ApplyPhase, duration time.Duration)
type applyPhaseObserverContextKey struct{}

// WithApplyPhaseObserver returns a context that reports catalog apply phase
// durations to observer. It is intended for request-scoped instrumentation;
// catalog behavior is unchanged when observer is nil.
func WithApplyPhaseObserver(
	ctx context.Context,
	observer func(phase ApplyPhase, duration time.Duration),
) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, applyPhaseObserverContextKey{}, applyPhaseObserver(observer))
}

func observeApplyPhase(ctx context.Context, phase ApplyPhase, started time.Time) {
	recordApplyPhase(ctx, phase, time.Since(started))
}

func recordApplyPhase(ctx context.Context, phase ApplyPhase, duration time.Duration) {
	observer, _ := ctx.Value(applyPhaseObserverContextKey{}).(applyPhaseObserver)
	if observer != nil {
		observer(phase, duration)
	}
}

func measureApplyPhase(ctx context.Context, phase ApplyPhase, apply func() error) error {
	started := time.Now()
	err := apply()
	observeApplyPhase(ctx, phase, started)
	return err
}
