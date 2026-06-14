package wikicatalog

import (
	"errors"
	"fmt"
	"time"
)

// Op is the kind of mutation a single Change describes inside an
// ApplyChangeSet request.
type Op uint8

const (
	// OpUpsert creates a page if it does not exist, otherwise
	// replaces its body. Slug is required; Body is required.
	OpUpsert Op = iota

	// OpDelete removes a page. Slug is required.
	OpDelete

	// OpRename moves a page from Slug to NewSlug, preserving the
	// page_id and revision chain. The source body is reused unchanged;
	// callers do not provide Body for renames.
	OpRename
)

func (o Op) String() string {
	switch o {
	case OpUpsert:
		return "upsert"
	case OpDelete:
		return "delete"
	case OpRename:
		return "rename"
	}
	return fmt.Sprintf("Op(%d)", o)
}

// Change is one entry in a ChangeSetRequest. A single ApplyChangeSet
// call may carry many Changes, all committed atomically inside one
// SQL transaction and one wiki_changesets row.
//
// Within a changeset, no two Changes may target the same slug.
// ApplyChangeSet rejects this at validation time because resolving
// the intended final state would be ambiguous.
type Change struct {
	Op      Op
	Slug    string
	NewSlug string // OpRename only
	// Body is the page contents for OpUpsert. The catalog hashes
	// and persists Body inside ApplyChangeSet; callers must not
	// mutate the underlying slice between submitting the request
	// and the call returning. The catalog itself never mutates
	// Body.
	//
	// OpRename optionally accepts Body too: when non-empty, the
	// rename atomically updates the page's body alongside the slug
	// move, preserving page_id continuity. This is used by the
	// prefix-move path so a renamed page whose body references
	// another renamed slug lands with the rewritten content under
	// the new slug. When Body is empty on OpRename the existing
	// body is carried forward unchanged.
	Body    []byte
	IfMatch string // optional per-page CAS, hex git blob SHA-1
}

// Source identifies the entry point that originated a changeset. It
// is recorded on wiki_changesets.source for auditing and rate-limit
// classification.
type Source string

const (
	SourceREST      Source = "rest"
	SourceAdmin     Source = "admin"
	SourceBatch     Source = "batch"
	SourceCompact   Source = "compact"
	SourceMigration Source = "migration"
	SourcePush      Source = "push" // reserved for the future git façade
)

// ChangeSetRequest is the input to ApplyChangeSet.
type ChangeSetRequest struct {
	RepositoryID   uint
	AuthorID       *uint
	Message        string
	ExpectedParent *uint64 // optional; if set, ApplyChangeSet refuses unless wiki_repo_heads still points here
	Source         Source
	Changes        []Change

	// OverrideCommitSHA pins the synth_commit_sha for this changeset
	// instead of letting the catalog mint one. The migration tool uses
	// this to keep the original git commit SHA, including empty git
	// commits, so existing GetWikiPage?ref=<sha> requests and history
	// sampling continue to resolve after the catalog cutover. Must be
	// 40 lowercase hex characters.
	OverrideCommitSHA string

	// OverrideCommittedAt pins wiki_changesets.committed_at and the
	// per-revision committed_at instead of using Catalog.Now(). Used
	// by migration to preserve historical timestamps.
	OverrideCommittedAt *time.Time
}

// ChangeResult is the per-Change outcome surfaced to the caller, in
// ChangeSetResult.Changes. The slice indices match the input slice
// order so callers can correlate Result[i] with Request.Changes[i].
type ChangeResult struct {
	Op         Op
	Slug       string // post-change slug (NewSlug for rename, Slug otherwise)
	PrevSlug   string // pre-change slug; only set for OpRename and OpDelete
	PageID     uint64
	RevisionID uint64
	BlobSHA    string // empty for OpDelete
	BodySize   int
}

// ChangeSetResult is the return of ApplyChangeSet.
type ChangeSetResult struct {
	ChangesetID uint64
	ParentID    *uint64
	CommitSHA   string
	Source      Source
	Changes     []ChangeResult
}

// Typed errors returned by ApplyChangeSet. Callers (the REST layer)
// translate these to HTTP status codes.
var (
	// ErrCASLost indicates that ExpectedParent was set and no longer
	// matches the current wiki_repo_heads row, even after the
	// configured retry budget. Callers should refresh their view and
	// re-submit if they still want to apply.
	ErrCASLost = errors.New("wiki catalog: head changed under us")

	// ErrPageNotFound is returned by OpDelete / OpRename when the
	// source slug has no live page row.
	ErrPageNotFound = errors.New("wiki catalog: page not found")

	// ErrDuplicateInChangeset indicates that two Changes in the same
	// request target the same slug, including rename destinations.
	ErrDuplicateInChangeset = errors.New("wiki catalog: duplicate slug within changeset")
)

// ConflictError is returned when a request violates a per-page
// invariant (stale If-Match, rename destination occupied, prefix
// collision). It carries enough structure for the REST layer to emit
// a useful body. ApplyChangeSet returns the first conflict found.
type ConflictError struct {
	Code         string // see ConflictCode* below
	Slug         string // the slug that triggered the conflict
	Destination  string // OpRename target, if applicable
	ExpectedSHA  string // IfMatch the caller sent
	CurrentSHA   string // catalog head_blob_sha at conflict time
	CollidesWith string // existing slug that collides via prefix rule
	Message      string
}

func (e *ConflictError) Error() string { return e.Message }

// Sentinel codes — must remain stable because move endpoints surface
// them in JSON bodies that today carry codes like SOURCE_STALE.
const (
	ConflictCodeStale           = "SOURCE_STALE"
	ConflictCodeDestinationTake = "DESTINATION_EXISTS"
	ConflictCodePrefix          = "PREFIX_COLLISION"
)

// Revision op tags stored in wiki_page_revisions.op. The enum is a
// superset of Op because revisions distinguish first-write from
// later updates and record delete/restore as their own ops in the
// history chain. Package-internal; callers outside wikicatalog read
// these only via the catalog's API, never the raw column.
const (
	revOpCreate  = "create"
	revOpUpdate  = "update"
	revOpRename  = "rename"
	revOpDelete  = "delete"
	revOpRestore = "restore"
)

// Directory-index entry kinds stored in wiki_dir_index.child_kind.
// Package-internal.
const (
	childKindBlob = "blob"
	childKindTree = "tree"
)

// Per-changeset quotas. Enforced in planChangeSet so the entire
// request is rejected before any blob touches the filesystem or any
// SQL row touches the catalog. These values match the soft limits
// the RFC §11 recommends; they are intentionally well below the
// dialect-specific hard limits (TiDB's default txn size cap, IN-list
// parameter count, etc.) so a malformed request fails clean.
const (
	MaxChangesPerChangeset = 10_000
	MaxBytesPerChangeset   = 200 * 1024 * 1024
)

// ErrChangeSetTooLarge is returned by ApplyChangeSet when a request
// exceeds MaxChangesPerChangeset or MaxBytesPerChangeset. Callers
// (REST handlers, migration tool) translate this to a clean
// client-facing error rather than letting it surface as a
// dialect-specific transaction failure mid-flight.
var ErrChangeSetTooLarge = errors.New("wiki catalog: changeset exceeds size limits")
