package wikiv2

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

const (
	DefaultBranch = "master"
	PageExtension = ".md"
	DefaultRef    = "refs/heads/" + DefaultBranch
)

var ErrRefCASMismatch = errors.New("wiki v2 ref changed")

// PageMutation describes one planned git tree mutation for a wiki page.
type PageMutation struct {
	Slug    string
	Path    string
	Content []byte
	Delete  bool
}

// WritePlan is the minimal durable write contract for a future git-backed
// wiki mutation.
type WritePlan struct {
	Ref            string
	ExpectedOldSHA string
	Message        string
	Mutations      []PageMutation
}

// IndexPage is one derived live-page row for the Wiki V2 index.
type IndexPage struct {
	Slug          string
	Title         string
	HeadBlobSHA   string
	HeadCommitSHA string
	Size          int
	UpdatedAt     time.Time
	LastAuthorID  *uint
}

// IndexState is the derived reconciler progress state for one repository.
type IndexState struct {
	IndexedCommitSHA     string
	IndexedAt            *time.Time
	ReconcileRequestedAt *time.Time
	ReconcilerLeaseUntil *time.Time
}

// ReconcileRequest is the minimal repo-scoped reconcile contract.
type ReconcileRequest struct {
	RepositoryID       uint
	RepositoryFullName string
	WikiRepoFullName   string
	RequestedAt        time.Time
}

// ReconcileResult summarizes one manual reconcile run.
type ReconcileResult struct {
	RepositoryID     uint
	IndexedCommitSHA string
	PageCount        int
	Reconciled       bool
}

// Reconciler is the minimal service contract for a manual Wiki V2 index pass.
type Reconciler interface {
	Reconcile(context.Context, ReconcileRequest) (ReconcileResult, error)
}

// RefCASStore is the git capability required for durable ref compare-and-swap.
type RefCASStore interface {
	LookupRef(ctx context.Context, fullName, ref string) (string, error)
	UpdateRefCAS(ctx context.Context, fullName, ref, newSHA, expectedOldSHA string) error
	CreateRef(ctx context.Context, fullName, ref, sha string) error
}

// AdvanceRefResult captures the visible outcome of one CAS attempt.
type AdvanceRefResult struct {
	PreviousSHA string
	CurrentSHA  string
	Updated     bool
}

// SlugToPath maps a wiki slug to its canonical git path.
func SlugToPath(slug string) (string, error) {
	if err := wikicatalog.ValidateWritable(slug); err != nil {
		return "", err
	}
	return slug + PageExtension, nil
}

// PathToSlug returns the wiki slug for a canonical git path.
func PathToSlug(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" || strings.HasPrefix(path, ".") || !strings.HasSuffix(path, PageExtension) {
		return "", false
	}
	slug := strings.TrimSuffix(path, PageExtension)
	if err := wikicatalog.ValidateWritable(slug); err != nil {
		return "", false
	}
	return slug, true
}

// PlanPageUpsert creates the minimal write plan for one page create/update.
func PlanPageUpsert(slug string, content []byte, message, expectedOldSHA string) (WritePlan, error) {
	path, err := SlugToPath(slug)
	if err != nil {
		return WritePlan{}, err
	}
	return WritePlan{
		Ref:            DefaultRef,
		ExpectedOldSHA: strings.TrimSpace(expectedOldSHA),
		Message:        message,
		Mutations: []PageMutation{{
			Slug:    slug,
			Path:    path,
			Content: append([]byte(nil), content...),
		}},
	}, nil
}

// PlanPageDelete creates the minimal write plan for one page delete.
func PlanPageDelete(slug, message, expectedOldSHA string) (WritePlan, error) {
	path, err := SlugToPath(slug)
	if err != nil {
		return WritePlan{}, err
	}
	return WritePlan{
		Ref:            DefaultRef,
		ExpectedOldSHA: strings.TrimSpace(expectedOldSHA),
		Message:        message,
		Mutations: []PageMutation{{
			Slug:   slug,
			Path:   path,
			Delete: true,
		}},
	}, nil
}

// AdvanceRefCAS applies a durable ref CAS with idempotent no-op semantics when
// the target already points at newSHA.
func AdvanceRefCAS(ctx context.Context, store RefCASStore, fullName, ref, expectedOldSHA, newSHA string) (AdvanceRefResult, error) {
	if normalizeSHA(newSHA) == "" {
		return AdvanceRefResult{}, gitstore.ErrInvalidSHA
	}
	currentSHA, err := store.LookupRef(ctx, fullName, ref)
	if err != nil && !errors.Is(err, gitstore.ErrRefNotFound) {
		return AdvanceRefResult{}, err
	}
	if equalSHA(currentSHA, newSHA) {
		return AdvanceRefResult{
			PreviousSHA: currentSHA,
			CurrentSHA:  currentSHA,
			Updated:     false,
		}, nil
	}

	expectedOldSHA = normalizeSHA(expectedOldSHA)
	if currentSHA == "" {
		if expectedOldSHA != "" {
			return AdvanceRefResult{
				PreviousSHA: "",
				CurrentSHA:  "",
			}, ErrRefCASMismatch
		}
		if err := store.CreateRef(ctx, fullName, ref, newSHA); err != nil {
			if errors.Is(err, gitstore.ErrRefAlreadyExists) || errors.Is(err, gitstore.ErrRefChanged) {
				return AdvanceRefResult{}, ErrRefCASMismatch
			}
			return AdvanceRefResult{}, err
		}
		return AdvanceRefResult{
			PreviousSHA: "",
			CurrentSHA:  normalizeSHA(newSHA),
			Updated:     true,
		}, nil
	}

	if !equalSHA(currentSHA, expectedOldSHA) {
		return AdvanceRefResult{
			PreviousSHA: normalizeSHA(currentSHA),
			CurrentSHA:  normalizeSHA(currentSHA),
		}, ErrRefCASMismatch
	}
	if err := store.UpdateRefCAS(ctx, fullName, ref, newSHA, currentSHA); err != nil {
		if errors.Is(err, gitstore.ErrRefChanged) || errors.Is(err, gitstore.ErrRefAlreadyExists) {
			return AdvanceRefResult{}, ErrRefCASMismatch
		}
		return AdvanceRefResult{}, err
	}
	return AdvanceRefResult{
		PreviousSHA: normalizeSHA(currentSHA),
		CurrentSHA:  normalizeSHA(newSHA),
		Updated:     true,
	}, nil
}

func equalSHA(a, b string) bool {
	return normalizeSHA(a) == normalizeSHA(b)
}

func normalizeSHA(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}
