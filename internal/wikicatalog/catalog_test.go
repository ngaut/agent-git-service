package wikicatalog

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestPlanChangeSet_ValidatesInputs(t *testing.T) {
	c := New(nil, nil) // SQL not exercised in plan-only tests
	c.Now = func() time.Time { return time.Unix(0, 0).UTC() }

	cases := []struct {
		name    string
		req     ChangeSetRequest
		wantErr string
	}{
		{
			name:    "missing-repo",
			req:     ChangeSetRequest{Source: SourceREST, Changes: []Change{{Op: OpUpsert, Slug: "home", Body: []byte("x")}}},
			wantErr: "repository_id required",
		},
		{
			name:    "missing-source",
			req:     ChangeSetRequest{RepositoryID: 1, Changes: []Change{{Op: OpUpsert, Slug: "home", Body: []byte("x")}}},
			wantErr: "source required",
		},
		{
			name:    "no-changes",
			req:     ChangeSetRequest{RepositoryID: 1, Source: SourceREST},
			wantErr: "no changes supplied",
		},
		{
			name: "git-allows-empty-changeset",
			req: ChangeSetRequest{
				RepositoryID:      1,
				Source:            SourceGit,
				OverrideCommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		},
		{
			name: "bad-slug-uppercase",
			req: ChangeSetRequest{
				RepositoryID: 1, Source: SourceREST,
				Changes: []Change{{Op: OpUpsert, Slug: "Home", Body: []byte("x")}},
			},
			wantErr: "disallowed character",
		},
		{
			name: "bad-slug-disallowed-char",
			req: ChangeSetRequest{
				RepositoryID: 1, Source: SourceREST,
				Changes: []Change{{Op: OpUpsert, Slug: "with space", Body: []byte("x")}},
			},
			wantErr: "disallowed character",
		},
		{
			name: "upsert-without-body",
			req: ChangeSetRequest{
				RepositoryID: 1, Source: SourceREST,
				Changes: []Change{{Op: OpUpsert, Slug: "home"}},
			},
			wantErr: "OpUpsert requires Body",
		},
		{
			name: "upsert-with-newslug",
			req: ChangeSetRequest{
				RepositoryID: 1, Source: SourceREST,
				Changes: []Change{{Op: OpUpsert, Slug: "home", NewSlug: "other", Body: []byte("x")}},
			},
			wantErr: "must not set NewSlug",
		},
		{
			name: "rename-bad-newslug",
			req: ChangeSetRequest{
				RepositoryID: 1, Source: SourceREST,
				Changes: []Change{{Op: OpRename, Slug: "a", NewSlug: "B AD"}},
			},
			wantErr: "NewSlug",
		},
		{
			name: "rename-same-slug",
			req: ChangeSetRequest{
				RepositoryID: 1, Source: SourceREST,
				Changes: []Change{{Op: OpRename, Slug: "page", NewSlug: "page"}},
			},
			wantErr: "same slug",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.planChangeSet(tc.req)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if got := err.Error(); !contains(got, tc.wantErr) {
				t.Fatalf("error %q does not contain %q", got, tc.wantErr)
			}
		})
	}
}

func TestPlanChangeSet_EnforcesQuota(t *testing.T) {
	c := New(nil, nil)
	c.Now = func() time.Time { return time.Unix(0, 0).UTC() }

	// Too many changes.
	tooMany := make([]Change, MaxChangesPerChangeset+1)
	for i := range tooMany {
		tooMany[i] = Change{Op: OpDelete, Slug: "p" + strconvI(i)}
	}
	_, err := c.planChangeSet(ChangeSetRequest{
		RepositoryID: 1, Source: SourceREST, Changes: tooMany,
	})
	if !errors.Is(err, ErrChangeSetTooLarge) {
		t.Fatalf("expected ErrChangeSetTooLarge, got %v", err)
	}

	// Aggregate body bytes exceed limit.
	big := make([]byte, MaxBytesPerChangeset/2+1)
	_, err = c.planChangeSet(ChangeSetRequest{
		RepositoryID: 1, Source: SourceREST,
		Changes: []Change{
			{Op: OpUpsert, Slug: "a", Body: big},
			{Op: OpUpsert, Slug: "b", Body: big},
		},
	})
	if !errors.Is(err, ErrChangeSetTooLarge) {
		t.Fatalf("expected ErrChangeSetTooLarge for body size, got %v", err)
	}
}

// strconvI avoids dragging strconv into the test imports for a one-off.
func strconvI(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	out := make([]byte, 0, 8)
	for n > 0 {
		out = append([]byte{digits[n%10]}, out...)
		n /= 10
	}
	return string(out)
}

func TestPlanChangeSet_RejectsDuplicates(t *testing.T) {
	c := New(nil, nil)
	c.Now = func() time.Time { return time.Unix(0, 0).UTC() }

	// Two upserts to the same slug.
	_, err := c.planChangeSet(ChangeSetRequest{
		RepositoryID: 1, Source: SourceREST,
		Changes: []Change{
			{Op: OpUpsert, Slug: "home", Body: []byte("a")},
			{Op: OpUpsert, Slug: "home", Body: []byte("b")},
		},
	})
	if !errors.Is(err, ErrDuplicateInChangeset) {
		t.Fatalf("expected ErrDuplicateInChangeset, got %v", err)
	}

	// Rename A→B and Upsert B in the same changeset: destination clash.
	_, err = c.planChangeSet(ChangeSetRequest{
		RepositoryID: 1, Source: SourceREST,
		Changes: []Change{
			{Op: OpRename, Slug: "a", NewSlug: "b"},
			{Op: OpUpsert, Slug: "b", Body: []byte("body")},
		},
	})
	if !errors.Is(err, ErrDuplicateInChangeset) {
		t.Fatalf("expected ErrDuplicateInChangeset on dest clash, got %v", err)
	}
}

func TestPlanChangeSet_TouchedCISetIsSortedUnion(t *testing.T) {
	c := New(nil, nil)
	c.Now = func() time.Time { return time.Unix(0, 0).UTC() }
	plan, err := c.planChangeSet(ChangeSetRequest{
		RepositoryID: 1, Source: SourceREST,
		Changes: []Change{
			{Op: OpUpsert, Slug: "home", Body: []byte("h")},
			{Op: OpRename, Slug: "old", NewSlug: "new"},
			{Op: OpDelete, Slug: "trash"},
		},
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	want := []string{"home", "new", "old", "trash"}
	if !reflect.DeepEqual(plan.touchedSlugs, want) {
		t.Fatalf("touchedSlugs = %v, want %v", plan.touchedSlugs, want)
	}
}

func TestSplitParentLeaf(t *testing.T) {
	cases := []struct {
		in     string
		parent string
		leaf   string
	}{
		{"home", "", "home"},
		{"a/b", "a", "b"},
		{"a/b/c", "a/b", "c"},
		{"_sidebar", "", "_sidebar"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			p, l := splitParentLeaf(tc.in)
			if p != tc.parent || l != tc.leaf {
				t.Fatalf("splitParentLeaf(%q) = (%q, %q), want (%q, %q)",
					tc.in, p, l, tc.parent, tc.leaf)
			}
		})
	}
}

func TestParentChain(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"home", nil},
		{"a/b", []string{"a"}},
		{"a/b/c", []string{"a", "a/b"}},
		{"a/b/c/d", []string{"a", "a/b", "a/b/c"}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := parentChain(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parentChain(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestComputeSynthCommitSHA_Deterministic: identical inputs produce
// identical SHAs; differing inputs differ.
func TestComputeSynthCommitSHA_Deterministic(t *testing.T) {
	t0 := time.Unix(1700000000, 0).UTC()
	parent := uint64(10)
	plan := []plannedChange{
		{op: OpUpsert, srcSlug: "home"},
		{op: OpUpsert, srcSlug: "guides/intro"},
	}
	blobs := map[string]string{
		"home":         "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"guides/intro": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	a := computeSynthCommitSHA(1, &parent, t0, "msg", plan, blobs)
	b := computeSynthCommitSHA(1, &parent, t0, "msg", plan, blobs)
	if a != b {
		t.Fatalf("deterministic: %q vs %q", a, b)
	}
	if len(a) != 40 {
		t.Fatalf("expected 40-char hex, got %d: %q", len(a), a)
	}

	// Different message → different SHA.
	c := computeSynthCommitSHA(1, &parent, t0, "other", plan, blobs)
	if c == a {
		t.Fatalf("different message produced same SHA")
	}

	// Different parent → different SHA.
	other := uint64(11)
	d := computeSynthCommitSHA(1, &other, t0, "msg", plan, blobs)
	if d == a {
		t.Fatalf("different parent produced same SHA")
	}

	// nil parent → different SHA still.
	e := computeSynthCommitSHA(1, nil, t0, "msg", plan, blobs)
	if e == a {
		t.Fatalf("nil parent produced same SHA as non-nil")
	}
}

func contains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
