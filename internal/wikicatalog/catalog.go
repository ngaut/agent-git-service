package wikicatalog

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Catalog is the wiki storage catalog. It owns the SQL access for all
// wiki_* tables, owns the blob CAS, and exposes ApplyChangeSet as the
// single write entry point. Callers (REST handlers, the migration
// tool, future push ingestion) all funnel through this struct.
type Catalog struct {
	DB   *gorm.DB
	Blob *BlobStore

	// DBFor resolves the *gorm.DB to use for a given request context.
	// This keeps catalog writes aligned with service transactions and
	// request cancellation.
	//
	// If nil, the catalog falls back to c.DB.WithContext(ctx) for
	// single-DB deployments. New() defaults to that behavior.
	DBFor func(ctx context.Context) *gorm.DB

	// Now lets tests inject a deterministic clock. Defaults to
	// time.Now().UTC().
	Now func() time.Time

	// MaxCASRetries bounds the optimistic concurrency loop on
	// wiki_repo_heads. Zero means a sensible default.
	MaxCASRetries int

	// OnChangeSetCommitted, if set, is called once per successful
	// ApplyChangeSet after the SQL transaction commits. It receives
	// the changeset's repository_id and the per-change results so the
	// caller can drive side effects (search reindexing, webhook
	// dispatch, cache invalidation) without the catalog package
	// depending on the service layer.
	//
	// The hook runs synchronously in the same goroutine as the
	// caller. Callers should make it cheap, or queue work to a
	// background goroutine inside the hook themselves.
	//
	// Errors from the hook do NOT roll back the changeset — the
	// catalog state is already committed by the time the hook runs.
	// Returning an error from the hook propagates back to the caller
	// of ApplyChangeSet, signaling that the side effects failed
	// even though the catalog state landed cleanly.
	OnChangeSetCommitted func(ctx context.Context, repoID uint, result ChangeSetResult) error

	// testForceCASLoss is a test-only injection point. When set, each
	// applyOnce attempt consults it before the in-tx CAS update and,
	// if it returns true, rolls back as if the CAS lost. Used to
	// exercise the retry budget deterministically without depending on
	// database scheduler timing.
	testForceCASLoss func() bool
}

// New constructs a Catalog. db and blob must be non-nil; the caller
// is responsible for AutoMigrate having already run on db.
//
// Callers may set DBFor after construction so the catalog uses the same
// context-scoped DB handle as the service layer.
func New(db *gorm.DB, blob *BlobStore) *Catalog {
	return &Catalog{
		DB:   db,
		Blob: blob,
		// Truncate to whole seconds so wiki_pages.updated_at and the
		// post-commit-materialized git commit timestamp compare equal
		// when callers read both back. Git stores second precision;
		// time.Now() carries nanos that would otherwise drift the
		// catalog ahead of git on every write.
		Now:           func() time.Time { return time.Now().UTC().Truncate(time.Second) },
		MaxCASRetries: 5,
	}
}

// db returns the *gorm.DB to use for a request. Resolves via DBFor
// if set, otherwise falls back to c.DB. Always attaches ctx so
// cancellation propagates into GORM.
func (c *Catalog) db(ctx context.Context) *gorm.DB {
	if c.DBFor != nil {
		return c.DBFor(ctx)
	}
	return c.DB.WithContext(ctx)
}

// changesetPlan is the validated form of a ChangeSetRequest. Slug
// grammar errors and duplicate source/destination slots have already
// been rejected so SQL queries and conflict checks can use the slug
// strings directly.
type changesetPlan struct {
	repoID       uint
	authorID     *uint
	message      string
	source       Source
	committedAt  time.Time
	parentExpect *uint64
	changes      []plannedChange
	// touchedSlugs is the union of every slug the changeset
	// references (sources and rename destinations). The pre-read step
	// loads exactly these pages in one query.
	touchedSlugs []string
	// overrideCommitSHA, when non-empty, is used as the changeset's
	// synth_commit_sha instead of computing a fresh one. Migration
	// sets this to the historical git commit SHA so REST clients see
	// the same identity post-cutover.
	overrideCommitSHA string
}

type plannedChange struct {
	op      Op
	srcSlug string

	// rename only
	dstSlug string

	// upsert only
	body []byte

	// caller's optional CAS expectation, lowercased hex
	ifMatch string
}

// planChangeSet validates the request and produces a changesetPlan,
// or returns the first validation error encountered. Slug grammar
// errors and intra-changeset duplicates are caught here so
// ApplyChangeSet need not redo this work inside its OCC retry loop.
func (c *Catalog) planChangeSet(req ChangeSetRequest) (changesetPlan, error) {
	if req.RepositoryID == 0 {
		return changesetPlan{}, fmt.Errorf("wiki catalog: repository_id required")
	}
	if req.Source == "" {
		return changesetPlan{}, fmt.Errorf("wiki catalog: source required")
	}
	if len(req.Changes) == 0 && req.Source != SourceMigration {
		return changesetPlan{}, fmt.Errorf("wiki catalog: no changes supplied")
	}
	if len(req.Changes) > MaxChangesPerChangeset {
		return changesetPlan{}, fmt.Errorf("%w: %d changes exceeds limit %d",
			ErrChangeSetTooLarge, len(req.Changes), MaxChangesPerChangeset)
	}
	var totalBytes int
	for _, ch := range req.Changes {
		totalBytes += len(ch.Body)
		if totalBytes > MaxBytesPerChangeset {
			return changesetPlan{}, fmt.Errorf("%w: body bytes exceed limit %d",
				ErrChangeSetTooLarge, MaxBytesPerChangeset)
		}
	}

	committedAt := c.Now()
	if req.OverrideCommittedAt != nil {
		committedAt = req.OverrideCommittedAt.UTC()
	}
	overrideSHA := strings.ToLower(strings.TrimSpace(req.OverrideCommitSHA))
	if overrideSHA != "" {
		if err := validateSHA(overrideSHA); err != nil {
			return changesetPlan{}, fmt.Errorf("wiki catalog: OverrideCommitSHA: %w", err)
		}
	}
	plan := changesetPlan{
		repoID:            req.RepositoryID,
		authorID:          req.AuthorID,
		message:           req.Message,
		source:            req.Source,
		committedAt:       committedAt,
		parentExpect:      req.ExpectedParent,
		changes:           make([]plannedChange, 0, len(req.Changes)),
		overrideCommitSHA: overrideSHA,
	}

	// Validate per-change; deduplicate by slug slot.
	seenSrc := make(map[string]struct{}, len(req.Changes))
	seenDst := make(map[string]struct{}, len(req.Changes))
	touched := make(map[string]struct{}, len(req.Changes)*2)

	for i, ch := range req.Changes {
		if err := ValidateWritable(ch.Slug); err != nil {
			return changesetPlan{}, fmt.Errorf("change[%d].Slug: %w", i, err)
		}
		if _, dup := seenSrc[ch.Slug]; dup {
			return changesetPlan{}, fmt.Errorf("%w: %q", ErrDuplicateInChangeset, ch.Slug)
		}
		seenSrc[ch.Slug] = struct{}{}
		touched[ch.Slug] = struct{}{}

		planned := plannedChange{
			op:      ch.Op,
			srcSlug: ch.Slug,
			ifMatch: strings.ToLower(strings.TrimSpace(ch.IfMatch)),
		}

		switch ch.Op {
		case OpUpsert:
			if ch.NewSlug != "" {
				return changesetPlan{}, fmt.Errorf("change[%d]: OpUpsert must not set NewSlug", i)
			}
			// Body may be empty (zero-length page) but must not be nil:
			// distinguish "no body provided" from "empty body intentionally".
			if ch.Body == nil {
				return changesetPlan{}, fmt.Errorf("change[%d]: OpUpsert requires Body", i)
			}
			planned.body = ch.Body
			// Upsert's effective destination is its own slug.
			if _, dup := seenDst[ch.Slug]; dup {
				return changesetPlan{}, fmt.Errorf("%w (destination): %q", ErrDuplicateInChangeset, ch.Slug)
			}
			seenDst[ch.Slug] = struct{}{}

		case OpDelete:
			if ch.NewSlug != "" {
				return changesetPlan{}, fmt.Errorf("change[%d]: OpDelete must not set NewSlug", i)
			}
			if ch.Body != nil {
				return changesetPlan{}, fmt.Errorf("change[%d]: OpDelete must not set Body", i)
			}

		case OpRename:
			// Body is allowed on OpRename: when non-empty, the rename
			// atomically updates the page body alongside the slug
			// move (documented on Change.Body). When empty, the
			// existing body is carried forward.
			if err := ValidateWritable(ch.NewSlug); err != nil {
				return changesetPlan{}, fmt.Errorf("change[%d].NewSlug: %w", i, err)
			}
			if ch.NewSlug == ch.Slug {
				return changesetPlan{}, fmt.Errorf("change[%d]: rename source and destination are the same slug %q", i, ch.NewSlug)
			}
			if _, dup := seenDst[ch.NewSlug]; dup {
				return changesetPlan{}, fmt.Errorf("%w (destination): %q", ErrDuplicateInChangeset, ch.NewSlug)
			}
			seenDst[ch.NewSlug] = struct{}{}
			touched[ch.NewSlug] = struct{}{}
			planned.dstSlug = ch.NewSlug
			// Forward the optional body bytes so applyRename can pick
			// them up. Empty body means "carry the existing blob".
			planned.body = ch.Body

		default:
			return changesetPlan{}, fmt.Errorf("change[%d]: unknown op %v", i, ch.Op)
		}

		plan.changes = append(plan.changes, planned)
	}

	plan.touchedSlugs = make([]string, 0, len(touched))
	for slug := range touched {
		plan.touchedSlugs = append(plan.touchedSlugs, slug)
	}
	sort.Strings(plan.touchedSlugs)
	return plan, nil
}

// computeSynthCommitSHA produces a deterministic 40-char hex SHA-1
// for a new changeset. The hash covers the parent identity, the
// committer, the wall clock, the message, and the sorted
// content of every change, so two equivalent changesets produce the
// same SHA — important for the migration replay path which may retry
// after partial progress.
//
// This SHA is opaque to clients; they treat it as a stable handle
// referenced by GetWikiPage?ref=<sha> and surfaced on history rows.
// It is not a real git commit object hash. The future git façade may
// override it for clones that need git-format identity.
func computeSynthCommitSHA(repoID uint, parentID *uint64, committedAt time.Time, message string, plan []plannedChange, blobBySlug map[string]string) string {
	var b strings.Builder
	b.Grow(256 + 128*len(plan))
	b.WriteString("wiki-changeset\x00")
	b.WriteString(strconv.FormatUint(uint64(repoID), 10))
	b.WriteByte(0)
	if parentID != nil {
		b.WriteString(strconv.FormatUint(*parentID, 10))
	}
	b.WriteByte(0)
	b.WriteString(strconv.FormatInt(committedAt.UnixNano(), 10))
	b.WriteByte(0)
	b.WriteString(message)
	b.WriteByte(0)

	// Sort changes by source slug for stability.
	sorted := make([]plannedChange, len(plan))
	copy(sorted, plan)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].srcSlug < sorted[j].srcSlug
	})
	for _, ch := range sorted {
		b.WriteString(ch.op.String())
		b.WriteByte(0)
		b.WriteString(ch.srcSlug)
		b.WriteByte(0)
		b.WriteString(ch.dstSlug)
		b.WriteByte(0)
		b.WriteString(blobBySlug[ch.srcSlug])
		b.WriteByte(0)
	}
	sum := sha1.Sum([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// splitParentLeaf returns (parent_dir, leaf_name) for a slug. Parent
// dirs use the same slash-joined form as the slug.
func splitParentLeaf(slug string) (parent, leaf string) {
	idx := strings.LastIndex(slug, "/")
	if idx < 0 {
		return "", slug
	}
	return slug[:idx], slug[idx+1:]
}

// parentChain returns the list of intermediate directories that must
// exist for slug's leaf to live in. For "a/b/c", chain = ["a", "a/b"];
// the leaf row at parent_dir="a/b", child_name="c" is the caller's
// concern.
func parentChain(slug string) []string {
	if slug == "" {
		return nil
	}
	parts := strings.Split(slug, "/")
	if len(parts) <= 1 {
		return nil
	}
	chain := make([]string, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		chain = append(chain, strings.Join(parts[:i], "/"))
	}
	return chain
}
