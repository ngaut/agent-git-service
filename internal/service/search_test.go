package service

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/embedding"
	searchsvc "github.com/ngaut/agent-git-service/internal/service/search"

	"gorm.io/gorm"
)

var errFakeEmbed = errors.New("fake embed error")

func ptr[T any](v T) *T {
	return &v
}

func TestParseSearchQuery(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  SearchQualifiers
	}{
		{
			name:  "empty",
			query: "",
			want:  SearchQualifiers{},
		},
		{
			name:  "free text only",
			query: "hello world",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					FreeText: []string{"hello", "world"},
				},
			},
		},
		{
			name:  "basic qualifiers",
			query: "repo:owner/repo state:open author:alice assignee:bob",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					Repo:     "owner/repo",
					Repos:    []string{"owner/repo"},
					State:    "open",
					Author:   "alice",
					Assignee: "bob",
				},
			},
		},
		{
			name:  "multiple repo qualifiers",
			query: "repo:owner/one repo:owner/two state:open",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					Repo:  "owner/two",
					Repos: []string{"owner/one", "owner/two"},
					State: "open",
				},
			},
		},
		{
			name:  "label OR logic (comma-separated)",
			query: "label:bug,ui",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					Labels: [][]string{{"bug", "ui"}},
				},
			},
		},
		{
			name:  "label AND logic (separate qualifiers)",
			query: "label:bug label:ui",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					Labels: [][]string{{"bug"}, {"ui"}},
				},
			},
		},
		{
			name:  "label combined OR and AND logic",
			query: "label:bug,ui label:critical",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					Labels: [][]string{{"bug", "ui"}, {"critical"}},
				},
			},
		},
		{
			name:  "is:pr and is:draft",
			query: "is:pr is:draft",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					IsPR:     true,
					Draft:    true,
					DraftSet: true,
				},
			},
		},
		{
			name:  "is:merged",
			query: "is:merged",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					IsPR:   true,
					State:  "closed",
					Merged: ptr(true),
				},
			},
		},
		{
			name:  "is:unmerged",
			query: "is:unmerged",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					IsPR:   true,
					Merged: ptr(false),
				},
			},
		},
		{
			name:  "reviews",
			query: "review:approved reviewed-by:charlie review-requested:dave",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					IsPR:            true,
					Review:          "approved",
					ReviewedBy:      "charlie",
					ReviewRequested: "dave",
				},
			},
		},
		{
			name:  "sort",
			query: "sort:updated-desc free text",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					Sort:     "updated-desc",
					FreeText: []string{"free", "text"},
				},
			},
		},
		{
			name:  "negation",
			query: "-label:wontfix -author:bot -assignee:nobody label:bug",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					Labels: [][]string{{"bug"}},
				},
				NegationFilters: NegationFilters{
					NegatedLabels:   [][]string{{"wontfix"}},
					NegatedAuthor:   "bot",
					NegatedAssignee: "nobody",
				},
			},
		},
		{
			name:  "metadata has and no",
			query: "no:label has:assignee no:milestone",
			want: SearchQualifiers{
				MetadataFilters: MetadataFilters{
					NoLabel:     true,
					HasAssignee: true,
					NoMilestone: true,
				},
			},
		},
		{
			name:  "reason and involves",
			query: `reason:completed involves:eve reason:"not planned"`,
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					Reason:   "not planned", // last one wins
					Involves: "eve",
				},
			},
		},
		{
			name:  "commenter qualifier",
			query: `commenter:alice`,
			want: SearchQualifiers{
				ParserFields: ParserFields{
					Commenter: "alice",
				},
			},
		},
		{
			name:  "commenter with other qualifiers",
			query: `repo:owner/repo commenter:bob state:open`,
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					Repo:  "owner/repo",
					Repos: []string{"owner/repo"},
					State: "open",
				},
				ParserFields: ParserFields{
					Commenter: "bob",
				},
			},
		},
		{
			name:  "previously unsupported silences now parsed",
			query: "linked:pr status:success team-review-requested:foo/bar order:asc hello",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					Order:    "asc",
					FreeText: []string{"hello"},
				},
				ParserFields: ParserFields{
					Linked:              "pr",
					Status:              "success",
					TeamReviewRequested: "foo/bar",
				},
			},
		},
		{
			name:  "quoted values",
			query: `label:"bug fix" author:"john doe" "free text phrase"`,
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					Labels:   [][]string{{"bug fix"}},
					Author:   "john doe",
					FreeText: []string{"free text phrase"},
				},
			},
		},
		{
			name:  "complex mixed",
			query: `repo:test/test is:issue state:open label:bug,ui -label:wontfix sort:created-asc hello world`,
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					Repo:     "test/test",
					Repos:    []string{"test/test"},
					IsIssue:  true,
					State:    "open",
					Labels:   [][]string{{"bug", "ui"}},
					Sort:     "created-asc",
					FreeText: []string{"hello", "world"},
				},
				NegationFilters: NegationFilters{
					NegatedLabels: [][]string{{"wontfix"}},
				},
			},
		},
		{
			name:  "github docs - is:open is:issue",
			query: "is:open is:issue",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					State:   "open",
					IsIssue: true,
				},
			},
		},
		{
			name:  "github docs - is:closed is:pr",
			query: "is:closed is:pr",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					State: "closed",
					IsPR:  true,
				},
			},
		},
		{
			name:  "conflicting state and is",
			query: "state:open is:closed",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					State:         "open",
					StateConflict: true,
				},
			},
		},
		{
			name:  "conflicting is qualifiers",
			query: "is:open is:closed",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					State:         "open",
					StateConflict: true,
				},
			},
		},
		{
			name:  "conflicting merged and open",
			query: "state:open is:merged",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					State:         "open",
					StateConflict: true,
					IsPR:          true,
					Merged:        ptr(true),
				},
			},
		},
		{
			name:  "github docs - author and assignee handles",
			query: "author:@me assignee:octocat involves:octocat",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					Author:   "@me",
					Assignee: "octocat",
					Involves: "octocat",
				},
			},
		},
		{
			name:  "github docs - review requested handles",
			query: "user-review-requested:@me team-review-requested:github/docs",
			want: SearchQualifiers{
				ParserFields: ParserFields{
					UserReviewRequested: "@me",
					TeamReviewRequested: "github/docs",
				},
			},
		},
		{
			name:  "github docs - CI statuses and links",
			query: "status:success status:pending linked:issue linked:pr",
			want: SearchQualifiers{
				ParserFields: ParserFields{
					Status: "pending", // last one wins
					Linked: "pr",      // last one wins
				},
			},
		},
		{
			name:  "github docs - project and draft",
			query: "project:github/5 draft:true",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					Draft:    true,
					DraftSet: true,
					IsPR:     true,
				},
				ParserFields: ParserFields{
					Project: "github/5",
				},
			},
		},
		{
			name:  "case sensitivity of keys",
			query: "REPO:test/test IS:PR AUTHOR:alice",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					Repo:   "test/test",
					Repos:  []string{"test/test"},
					IsPR:   true,
					Author: "alice",
				},
			},
		},
		{
			name:  "visibility and fork qualifiers",
			query: "visibility:private fork:only",
			want: SearchQualifiers{
				ParserFields: ParserFields{
					Visibility: "private",
					Fork:       "only",
				},
			},
		},
		{
			name:  "universal negation limits",
			query: "-repo:foo/bar -is:pr -type:issue -state:closed -involves:bob",
			// Currently, if unsupported negated, they should ideally fall through or be dropped,
			// but we want to ensure they don't apply as POSITIVE filters.
			want: SearchQualifiers{
				// Repo should remain empty
				CoreFilters: CoreFilters{
					// IsIssue becomes true for -is:pr, but then -type:issue overwrites it to false
					IsIssue: false,
					// IsPR becomes false for -is:pr, but then -type:issue overwrites it to true
					IsPR: true,
					// State should remain empty
					// Involves should remain empty
				},
			},
		},
		{
			name:  "number qualifier #8",
			query: "#8",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					Number: ptr(8),
				},
			},
		},
		{
			name:  "number qualifier with repo",
			query: "#42 repo:owner/repo",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					Number: ptr(42),
					Repo:   "owner/repo",
					Repos:  []string{"owner/repo"},
				},
			},
		},
		{
			name:  "number qualifier with state",
			query: "#123 state:closed",
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					Number: ptr(123),
					State:  "closed",
				},
			},
		},
		// Issue #543: Regression tests for quoted phrase search
		{
			name:  "issue #543 - quoted phrase in free text",
			query: `"Alpha Phrase"`,
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					FreeText: []string{"Alpha Phrase"},
				},
			},
		},
		{
			name:  "issue #543 - quoted phrase with repo and is:issue",
			query: `repo:test/repo is:issue "Alpha Phrase"`,
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					Repo:     "test/repo",
					Repos:    []string{"test/repo"},
					IsIssue:  true,
					FreeText: []string{"Alpha Phrase"},
				},
			},
		},
		{
			name:  "issue #543 - quoted phrase with repo and is:pr",
			query: `repo:test/repo is:pr "Draft Alpha"`,
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					Repo:     "test/repo",
					Repos:    []string{"test/repo"},
					IsPR:     true,
					FreeText: []string{"Draft Alpha"},
				},
			},
		},
		{
			name:  "issue #543 - multiple quoted phrases",
			query: `"first phrase" "second phrase"`,
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					FreeText: []string{"first phrase", "second phrase"},
				},
			},
		},
		{
			name:  "issue #543 - quoted phrase with other qualifiers",
			query: `state:open author:alice "exact match"`,
			want: SearchQualifiers{
				CoreFilters: CoreFilters{
					State:    "open",
					Author:   "alice",
					FreeText: []string{"exact match"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseSearchQuery(tt.query)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseSearchQuery(%q)\ngot  = %+v\nwant = %+v", tt.query, got, tt.want)
			}
		})
	}
}

func TestParseCommitSearchQuery(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  CommitSearchQuery
	}{
		{
			name:  "qualifiers stripped from free text",
			query: "fix committer:gh-server author-date:>2021-01-01",
			want: CommitSearchQuery{
				FreeText:      []string{"fix"},
				Committer:     "gh-server",
				AuthorDate:    ">2021-01-01",
				HasQualifiers: true,
			},
		},
		{
			name:  "repo qualifier parsed",
			query: "repo:octo/repo fix",
			want: CommitSearchQuery{
				FreeText:      []string{"fix"},
				Repo:          "octo/repo",
				Repos:         []string{"octo/repo"},
				HasQualifiers: true,
			},
		},
		{
			name:  "author qualifier",
			query: "author:octocat",
			want: CommitSearchQuery{
				Author:        "octocat",
				HasQualifiers: true,
			},
		},
		{
			name:  "committer qualifier",
			query: "committer:bot",
			want: CommitSearchQuery{
				Committer:     "bot",
				HasQualifiers: true,
			},
		},
		{
			name:  "hash qualifier",
			query: "hash:abc123",
			want: CommitSearchQuery{
				Hash:          "abc123",
				HasQualifiers: true,
			},
		},
		{
			name:  "is:merge qualifier",
			query: "is:merge",
			want: CommitSearchQuery{
				Merge:         ptr(true),
				HasQualifiers: true,
			},
		},
		{
			name:  "merge:true qualifier",
			query: "merge:true",
			want: CommitSearchQuery{
				Merge:         ptr(true),
				HasQualifiers: true,
			},
		},
		{
			name:  "merge:false qualifier",
			query: "merge:false",
			want: CommitSearchQuery{
				Merge:         ptr(false),
				HasQualifiers: true,
			},
		},
		{
			name:  "author-date qualifier",
			query: "author-date:>=2023-01-01",
			want: CommitSearchQuery{
				AuthorDate:    ">=2023-01-01",
				HasQualifiers: true,
			},
		},
		{
			name:  "committer-date qualifier",
			query: "committer-date:2023-01-01..2023-12-31",
			want: CommitSearchQuery{
				CommitterDate: "2023-01-01..2023-12-31",
				HasQualifiers: true,
			},
		},
		{
			name:  "parent qualifier",
			query: "parent:abc123",
			want: CommitSearchQuery{
				Parent:        "abc123",
				HasQualifiers: true,
			},
		},
		{
			name:  "tree qualifier",
			query: "tree:def456",
			want: CommitSearchQuery{
				Tree:          "def456",
				HasQualifiers: true,
			},
		},
		{
			name:  "author-name qualifier",
			query: "author-name:John",
			want: CommitSearchQuery{
				AuthorName:    "John",
				HasQualifiers: true,
			},
		},
		{
			name:  "author-email qualifier",
			query: "author-email:john@example.com",
			want: CommitSearchQuery{
				AuthorEmail:   "john@example.com",
				HasQualifiers: true,
			},
		},
		{
			name:  "committer-name qualifier",
			query: "committer-name:Jane",
			want: CommitSearchQuery{
				CommitterName: "Jane",
				HasQualifiers: true,
			},
		},
		{
			name:  "committer-email qualifier",
			query: "committer-email:jane@example.com",
			want: CommitSearchQuery{
				CommitterEmail: "jane@example.com",
				HasQualifiers:  true,
			},
		},
		{
			name:  "org qualifier",
			query: "org:pingcap",
			want: CommitSearchQuery{
				Org:           "pingcap",
				HasQualifiers: true,
			},
		},
		{
			name:  "user qualifier",
			query: "user:octocat",
			want: CommitSearchQuery{
				User:          "octocat",
				HasQualifiers: true,
			},
		},
		{
			name:  "visibility qualifier",
			query: "visibility:public",
			want: CommitSearchQuery{
				Visibility:    "public",
				HasQualifiers: true,
			},
		},
		{
			name:  "combined qualifiers",
			query: "repo:pingcap/agent-git-service author:octocat is:merge fix bug",
			want: CommitSearchQuery{
				FreeText:      []string{"fix", "bug"},
				Repo:          "pingcap/agent-git-service",
				Repos:         []string{"pingcap/agent-git-service"},
				Author:        "octocat",
				Merge:         ptr(true),
				HasQualifiers: true,
			},
		},
		{
			name:  "unknown qualifier stays as free text",
			query: "foo:bar fix",
			want: CommitSearchQuery{
				FreeText: []string{"foo:bar", "fix"},
			},
		},
		{
			name:  "qualifier only",
			query: "committer:gh-server",
			want: CommitSearchQuery{
				Committer:     "gh-server",
				HasQualifiers: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCommitSearchQuery(tt.query)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseCommitSearchQuery(%q)\ngot  = %+v\nwant = %+v", tt.query, got, tt.want)
			}
		})
	}
}

func TestParseCodeSearchQuery(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  CodeSearchQuery
	}{
		{
			name:  "qualifiers stripped from free text",
			query: "fix filename:main.go extension:py path:src language:go",
			want: CodeSearchQuery{
				FreeText:      []string{"fix"},
				Filename:      "main.go",
				Extensions:    []string{".py"},
				Path:          "src",
				Language:      "go",
				HasQualifiers: true,
			},
		},
		{
			name:  "repo qualifier parsed",
			query: "repo:octo/repo fix",
			want: CodeSearchQuery{
				FreeText:      []string{"fix"},
				Repo:          "octo/repo",
				Repos:         []string{"octo/repo"},
				HasQualifiers: true,
			},
		},
		{
			name:  "filename qualifier",
			query: "filename:README.md",
			want: CodeSearchQuery{
				Filename:      "README.md",
				HasQualifiers: true,
			},
		},
		{
			name:  "extension qualifier with dot",
			query: "extension:.go",
			want: CodeSearchQuery{
				Extensions:    []string{".go"},
				HasQualifiers: true,
			},
		},
		{
			name:  "extension qualifier without dot",
			query: "extension:go",
			want: CodeSearchQuery{
				Extensions:    []string{".go"},
				HasQualifiers: true,
			},
		},
		{
			name:  "multiple extensions",
			query: "extension:go,py,js",
			want: CodeSearchQuery{
				Extensions:    []string{".go", ".py", ".js"},
				HasQualifiers: true,
			},
		},
		{
			name:  "path qualifier",
			query: "path:src/internal",
			want: CodeSearchQuery{
				Path:          "src/internal",
				HasQualifiers: true,
			},
		},
		{
			name:  "language qualifier",
			query: "language:python",
			want: CodeSearchQuery{
				Language:      "python",
				HasQualifiers: true,
			},
		},
		{
			name:  "combined qualifiers",
			query: "repo:pingcap/agent-git-service filename:main.go language:go error handling",
			want: CodeSearchQuery{
				FreeText:      []string{"error", "handling"},
				Repo:          "pingcap/agent-git-service",
				Repos:         []string{"pingcap/agent-git-service"},
				Filename:      "main.go",
				Language:      "go",
				HasQualifiers: true,
			},
		},
		{
			name:  "unknown qualifier stays as free text",
			query: "foo:bar fix",
			want: CodeSearchQuery{
				FreeText: []string{"foo:bar", "fix"},
			},
		},
		{
			name:  "qualifier only",
			query: "filename:test.go",
			want: CodeSearchQuery{
				Filename:      "test.go",
				HasQualifiers: true,
			},
		},
		{
			name:  "negated qualifier tracked",
			query: "-filename:test.go",
			want: CodeSearchQuery{
				NegatedQualifiers: []string{"-filename:test.go"},
				HasQualifiers:     true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCodeSearchQuery(tt.query)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseCodeSearchQuery(%q)\ngot  = %+v\nwant = %+v", tt.query, got, tt.want)
			}
		})
	}
}

func TestGetExtensionsForLanguage(t *testing.T) {
	tests := []struct {
		name     string
		language string
		wantLen  int
	}{
		{"go", "go", 1},
		{"python", "python", 3},
		{"javascript", "javascript", 4},
		{"typescript", "typescript", 4},
		{"unknown", "unknown", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetExtensionsForLanguage(tt.language)
			if len(got) != tt.wantLen {
				t.Errorf("GetExtensionsForLanguage(%q) returned %d extensions, want %d", tt.language, len(got), tt.wantLen)
			}
		})
	}
}

func setupSearchFilterDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, cleanup := openMigratedServiceTestDB(t)
	t.Cleanup(cleanup)
	return gdb
}

func issueNumbers(issues []db.Issue) []int {
	nums := make([]int, 0, len(issues))
	for _, iss := range issues {
		nums = append(nums, iss.Number)
	}
	sort.Ints(nums)
	return nums
}

func prNumbers(prs []db.PullRequest) []int {
	nums := make([]int, 0, len(prs))
	for _, pr := range prs {
		nums = append(nums, pr.Number)
	}
	sort.Ints(nums)
	return nums
}

func TestSearchQualifierRepoFilters(t *testing.T) {
	gdb := setupSearchFilterDB(t)

	owner := db.User{Login: "octo", Name: "Octo"}
	if err := gdb.Create(&owner).Error; err != nil {
		t.Fatalf("failed to create owner: %v", err)
	}
	repos := []db.Repository{
		{Name: "public", FullName: "octo/public", OwnerID: owner.ID, Visibility: "public", Fork: false, Language: "Go"},
		{Name: "private-fork", FullName: "octo/private-fork", OwnerID: owner.ID, Visibility: "private", Fork: true, Language: "Python"},
	}
	if err := gdb.Create(&repos).Error; err != nil {
		t.Fatalf("failed to create repositories: %v", err)
	}

	issues := []db.Issue{
		{Number: 1, RepositoryID: repos[0].ID, Title: "Issue public", AuthorID: owner.ID},
		{Number: 2, RepositoryID: repos[1].ID, Title: "Issue private fork", AuthorID: owner.ID},
	}
	if err := gdb.Create(&issues).Error; err != nil {
		t.Fatalf("failed to create issues: %v", err)
	}

	prs := []db.PullRequest{
		{Number: 1, RepositoryID: repos[0].ID, HeadRepositoryID: repos[0].ID, Title: "PR public", AuthorID: owner.ID},
		{Number: 2, RepositoryID: repos[1].ID, HeadRepositoryID: repos[1].ID, Title: "PR private fork", AuthorID: owner.ID},
	}
	if err := gdb.Create(&prs).Error; err != nil {
		t.Fatalf("failed to create pull requests: %v", err)
	}

	tests := []struct {
		name       string
		query      string
		wantIssues []int
		wantPRs    []int
	}{
		{
			name:       "visibility public",
			query:      "visibility:public",
			wantIssues: []int{1},
			wantPRs:    []int{1},
		},
		{
			name:       "visibility private",
			query:      "visibility:private",
			wantIssues: []int{2},
			wantPRs:    []int{2},
		},
		{
			name:       "fork only",
			query:      "fork:only",
			wantIssues: []int{2},
			wantPRs:    []int{2},
		},
		{
			name:       "fork false",
			query:      "fork:false",
			wantIssues: []int{1},
			wantPRs:    []int{1},
		},
		{
			name:       "fork true includes both",
			query:      "fork:true",
			wantIssues: []int{1, 2},
			wantPRs:    []int{1, 2},
		},
		{
			name:       "language filter",
			query:      "language:go",
			wantIssues: []int{1},
			wantPRs:    []int{1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sq := ParseSearchQuery(tt.query)

			var gotIssues []db.Issue
			if err := searchsvc.ApplyIssueQualifiers(gdb.Model(&db.Issue{}), gdb, sq).Find(&gotIssues).Error; err != nil {
				t.Fatalf("issue query failed: %v", err)
			}
			if got := issueNumbers(gotIssues); !reflect.DeepEqual(got, tt.wantIssues) {
				t.Errorf("issue numbers for %q = %v, want %v", tt.query, got, tt.wantIssues)
			}

			var gotPRs []db.PullRequest
			if err := searchsvc.ApplyPRQualifiers(gdb.Model(&db.PullRequest{}), gdb, sq).Find(&gotPRs).Error; err != nil {
				t.Fatalf("pr query failed: %v", err)
			}
			if got := prNumbers(gotPRs); !reflect.DeepEqual(got, tt.wantPRs) {
				t.Errorf("pr numbers for %q = %v, want %v", tt.query, got, tt.wantPRs)
			}
		})
	}
}

func TestSearchQualifierRepoScopedLabelFilters(t *testing.T) {
	gdb := setupSearchFilterDB(t)

	owner := db.User{Login: "octo", Name: "Octo"}
	if err := gdb.Create(&owner).Error; err != nil {
		t.Fatalf("failed to create owner: %v", err)
	}
	repos := []db.Repository{
		{Name: "public", FullName: "octo/public", OwnerID: owner.ID},
		{Name: "private", FullName: "octo/private", OwnerID: owner.ID},
	}
	if err := gdb.Create(&repos).Error; err != nil {
		t.Fatalf("failed to create repositories: %v", err)
	}

	issues := []db.Issue{
		{Number: 1, RepositoryID: repos[0].ID, Title: "public bug", AuthorID: owner.ID},
		{Number: 2, RepositoryID: repos[0].ID, Title: "public wontfix", AuthorID: owner.ID},
		{Number: 3, RepositoryID: repos[1].ID, Title: "private bug", AuthorID: owner.ID},
	}
	if err := gdb.Create(&issues).Error; err != nil {
		t.Fatalf("failed to create issues: %v", err)
	}

	prs := []db.PullRequest{
		{Number: 1, RepositoryID: repos[0].ID, HeadRepositoryID: repos[0].ID, Title: "public bug pr", AuthorID: owner.ID},
		{Number: 2, RepositoryID: repos[1].ID, HeadRepositoryID: repos[1].ID, Title: "private bug pr", AuthorID: owner.ID},
	}
	if err := gdb.Create(&prs).Error; err != nil {
		t.Fatalf("failed to create pull requests: %v", err)
	}

	labels := []db.Label{
		{RepositoryID: repos[0].ID, Name: "bug"},
		{RepositoryID: repos[0].ID, Name: "wontfix"},
		{RepositoryID: repos[1].ID, Name: "bug"},
	}
	if err := gdb.Create(&labels).Error; err != nil {
		t.Fatalf("failed to create labels: %v", err)
	}

	issueLabelLinks := [][2]uint{
		{issues[0].ID, labels[0].ID},
		{issues[1].ID, labels[0].ID},
		{issues[1].ID, labels[1].ID},
		{issues[2].ID, labels[2].ID},
	}
	for _, link := range issueLabelLinks {
		if err := gdb.Exec("INSERT INTO issue_labels (issue_id, label_id) VALUES (?, ?)", link[0], link[1]).Error; err != nil {
			t.Fatalf("insert issue_labels: %v", err)
		}
	}

	prLabelLinks := [][2]uint{
		{prs[0].ID, labels[0].ID},
		{prs[1].ID, labels[2].ID},
	}
	for _, link := range prLabelLinks {
		if err := gdb.Exec("INSERT INTO pr_labels (pull_request_id, label_id) VALUES (?, ?)", link[0], link[1]).Error; err != nil {
			t.Fatalf("insert pr_labels: %v", err)
		}
	}

	sq := ParseSearchQuery("repo:octo/public label:bug -label:wontfix")

	var gotIssues []db.Issue
	if err := searchsvc.ApplyIssueQualifiers(gdb.Model(&db.Issue{}), gdb, sq).Find(&gotIssues).Error; err != nil {
		t.Fatalf("issue query failed: %v", err)
	}
	if got := issueNumbers(gotIssues); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("issue numbers = %v, want [1]", got)
	}

	var gotPRs []db.PullRequest
	if err := searchsvc.ApplyPRQualifiers(gdb.Model(&db.PullRequest{}), gdb, sq).Find(&gotPRs).Error; err != nil {
		t.Fatalf("pr query failed: %v", err)
	}
	if got := prNumbers(gotPRs); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("pr numbers = %v, want [1]", got)
	}
}

// -----------------------------------------------------------------------------
// Vector Search Path Unit Tests (Issue #234)
// -----------------------------------------------------------------------------

// TestDeduplicateIssues tests the search package issue deduplication helper.
func TestDeduplicateIssues(t *testing.T) {
	t.Run("overlapping IDs dedup", func(t *testing.T) {
		primary := []db.Issue{
			{ID: 1, Number: 1, Title: "Issue 1"},
			{ID: 2, Number: 2, Title: "Issue 2"},
		}
		secondary := []db.Issue{
			{ID: 2, Number: 2, Title: "Issue 2 Duplicate"},
			{ID: 3, Number: 3, Title: "Issue 3"},
		}
		result := searchsvc.DeduplicateIssues(primary, secondary, defaultListLimit)
		if len(result) != 3 {
			t.Errorf("Expected 3 results, got %d", len(result))
		}
		// Check that IDs 1, 2, 3 are present
		seen := make(map[uint]bool)
		for _, iss := range result {
			seen[iss.ID] = true
		}
		if !seen[1] || !seen[2] || !seen[3] {
			t.Errorf("Expected IDs 1, 2, 3, got %v", seen)
		}
	})

	t.Run("non-overlapping merge", func(t *testing.T) {
		primary := []db.Issue{
			{ID: 1, Number: 1, Title: "Issue 1"},
		}
		secondary := []db.Issue{
			{ID: 2, Number: 2, Title: "Issue 2"},
			{ID: 3, Number: 3, Title: "Issue 3"},
		}
		result := searchsvc.DeduplicateIssues(primary, secondary, defaultListLimit)
		if len(result) != 3 {
			t.Errorf("Expected 3 results, got %d", len(result))
		}
	})

	t.Run("limit cap", func(t *testing.T) {
		// Create primary with defaultListLimit - 1 items (IDs 1-999)
		primary := make([]db.Issue, defaultListLimit-1)
		for i := 0; i < len(primary); i++ {
			primary[i] = db.Issue{ID: uint(i + 1), Number: i + 1, Title: "Issue"}
		}
		// Secondary has 5 more items with non-overlapping IDs (1000-1004)
		secondary := []db.Issue{
			{ID: 1000, Number: 1000, Title: "Issue 1000"},
			{ID: 1001, Number: 1001, Title: "Issue 1001"},
			{ID: 1002, Number: 1002, Title: "Issue 1002"},
			{ID: 1003, Number: 1003, Title: "Issue 1003"},
			{ID: 1004, Number: 1004, Title: "Issue 1004"},
		}
		result := searchsvc.DeduplicateIssues(primary, secondary, defaultListLimit)
		if len(result) != defaultListLimit {
			t.Errorf("Expected %d results (capped at limit), got %d", defaultListLimit, len(result))
		}
	})

	t.Run("empty primary", func(t *testing.T) {
		primary := []db.Issue{}
		secondary := []db.Issue{
			{ID: 1, Number: 1, Title: "Issue 1"},
			{ID: 2, Number: 2, Title: "Issue 2"},
		}
		result := searchsvc.DeduplicateIssues(primary, secondary, defaultListLimit)
		if len(result) != 2 {
			t.Errorf("Expected 2 results, got %d", len(result))
		}
	})

	t.Run("empty secondary", func(t *testing.T) {
		primary := []db.Issue{
			{ID: 1, Number: 1, Title: "Issue 1"},
		}
		secondary := []db.Issue{}
		result := searchsvc.DeduplicateIssues(primary, secondary, defaultListLimit)
		if len(result) != 1 {
			t.Errorf("Expected 1 result, got %d", len(result))
		}
	})
}

// TestDeduplicatePRs tests the search package pull request deduplication helper.
func TestDeduplicatePRs(t *testing.T) {
	t.Run("overlapping IDs dedup", func(t *testing.T) {
		primary := []db.PullRequest{
			{ID: 1, Number: 1, Title: "PR 1"},
			{ID: 2, Number: 2, Title: "PR 2"},
		}
		secondary := []db.PullRequest{
			{ID: 2, Number: 2, Title: "PR 2 Duplicate"},
			{ID: 3, Number: 3, Title: "PR 3"},
		}
		result := searchsvc.DeduplicatePRs(primary, secondary, defaultListLimit)
		if len(result) != 3 {
			t.Errorf("Expected 3 results, got %d", len(result))
		}
		seen := make(map[uint]bool)
		for _, pr := range result {
			seen[pr.ID] = true
		}
		if !seen[1] || !seen[2] || !seen[3] {
			t.Errorf("Expected IDs 1, 2, 3, got %v", seen)
		}
	})

	t.Run("non-overlapping merge", func(t *testing.T) {
		primary := []db.PullRequest{
			{ID: 1, Number: 1, Title: "PR 1"},
		}
		secondary := []db.PullRequest{
			{ID: 2, Number: 2, Title: "PR 2"},
			{ID: 3, Number: 3, Title: "PR 3"},
		}
		result := searchsvc.DeduplicatePRs(primary, secondary, defaultListLimit)
		if len(result) != 3 {
			t.Errorf("Expected 3 results, got %d", len(result))
		}
	})

	t.Run("limit cap", func(t *testing.T) {
		primary := make([]db.PullRequest, defaultListLimit-1)
		for i := 0; i < len(primary); i++ {
			primary[i] = db.PullRequest{ID: uint(i + 1), Number: i + 1, Title: "PR"}
		}
		// Secondary has 5 more items with non-overlapping IDs (1000-1004)
		secondary := []db.PullRequest{
			{ID: 1000, Number: 1000, Title: "PR 1000"},
			{ID: 1001, Number: 1001, Title: "PR 1001"},
			{ID: 1002, Number: 1002, Title: "PR 1002"},
			{ID: 1003, Number: 1003, Title: "PR 1003"},
			{ID: 1004, Number: 1004, Title: "PR 1004"},
		}
		result := searchsvc.DeduplicatePRs(primary, secondary, defaultListLimit)
		if len(result) != defaultListLimit {
			t.Errorf("Expected %d results (capped at limit), got %d", defaultListLimit, len(result))
		}
	})

	t.Run("empty primary", func(t *testing.T) {
		primary := []db.PullRequest{}
		secondary := []db.PullRequest{
			{ID: 1, Number: 1, Title: "PR 1"},
			{ID: 2, Number: 2, Title: "PR 2"},
		}
		result := searchsvc.DeduplicatePRs(primary, secondary, defaultListLimit)
		if len(result) != 2 {
			t.Errorf("Expected 2 results, got %d", len(result))
		}
	})

	t.Run("empty secondary", func(t *testing.T) {
		primary := []db.PullRequest{
			{ID: 1, Number: 1, Title: "PR 1"},
		}
		secondary := []db.PullRequest{}
		result := searchsvc.DeduplicatePRs(primary, secondary, defaultListLimit)
		if len(result) != 1 {
			t.Errorf("Expected 1 result, got %d", len(result))
		}
	})
}

// TestEmbedQuery tests the embedQuery method with various scenarios.
func TestEmbedQuery(t *testing.T) {
	t.Run("normal text with FakeEmbedder", func(t *testing.T) {
		svc := &Service{
			Embedder: &FakeEmbedder{Vec: []float32{0.1, 0.2, 0.3}},
		}

		result := svc.embedQuery(context.Background(), "test query")
		expected := "[0.1,0.2,0.3]"
		if result != expected {
			t.Errorf("Expected %q, got %q", expected, result)
		}
	})

	t.Run("NopEmbedder returns empty string", func(t *testing.T) {
		svc := &Service{
			Embedder: embedding.NopEmbedder{},
		}

		result := svc.embedQuery(context.Background(), "test query")
		if result != "" {
			t.Errorf("Expected empty string for NopEmbedder, got %q", result)
		}
	})

	t.Run("nil Embedder returns empty string", func(t *testing.T) {
		svc := &Service{
			Embedder: nil,
		}

		result := svc.embedQuery(context.Background(), "test query")
		if result != "" {
			t.Errorf("Expected empty string for nil Embedder, got %q", result)
		}
	})

	t.Run("embed error returns empty string", func(t *testing.T) {
		fakeEmbedder := &FakeEmbedder{Vec: nil, Err: errFakeEmbed}
		svc := &Service{
			Embedder: fakeEmbedder,
		}

		result := svc.embedQuery(context.Background(), "test query")
		if result != "" {
			t.Errorf("Expected empty string on error, got %q", result)
		}
		if fakeEmbedder.Called != 1 {
			t.Errorf("Expected Embed called 1 time, got %d", fakeEmbedder.Called)
		}
	})

	t.Run("empty vector returns empty string", func(t *testing.T) {
		fakeEmbedder := &FakeEmbedder{Vec: []float32{}}
		svc := &Service{
			Embedder: fakeEmbedder,
		}

		result := svc.embedQuery(context.Background(), "test query")
		if result != "" {
			t.Errorf("Expected empty string for empty vector, got %q", result)
		}
	})

	t.Run("token truncation for long text", func(t *testing.T) {
		fakeEmbedder := &FakeEmbedder{Vec: []float32{0.1, 0.2, 0.3}}
		svc := &Service{
			Embedder: fakeEmbedder,
		}

		longText := strings.Repeat(" token", embedding.MaxInputTokens+512)
		result := svc.embedQuery(context.Background(), longText)
		expected := "[0.1,0.2,0.3]"
		if result != expected {
			t.Errorf("Expected %q, got %q", expected, result)
		}
		gotTokens, err := embedding.CountInputTokens(fakeEmbedder.LastText)
		if err != nil {
			t.Fatalf("count truncated tokens: %v", err)
		}
		if gotTokens > embedding.MaxInputTokens {
			t.Errorf("expected <= %d tokens, got %d", embedding.MaxInputTokens, gotTokens)
		}
		if len(fakeEmbedder.LastText) >= len(longText) {
			t.Errorf("expected text to be truncated, got %d chars", len(fakeEmbedder.LastText))
		}
	})
}

func TestBuildIssueTextWhere(t *testing.T) {
	tests := []struct {
		name       string
		inValues   []string
		text       string
		wantClause string
		wantArgs   []any
	}{
		{
			name:       "empty defaults to title and body",
			inValues:   []string{},
			text:       "%test%",
			wantClause: "(LOWER(issues.title) LIKE LOWER(?) OR LOWER(issues.body) LIKE LOWER(?))",
			wantArgs:   []any{"%test%", "%test%"},
		},
		{
			name:       "in:title only",
			inValues:   []string{"title"},
			text:       "%test%",
			wantClause: "LOWER(issues.title) LIKE LOWER(?)",
			wantArgs:   []any{"%test%"},
		},
		{
			name:       "in:body only",
			inValues:   []string{"body"},
			text:       "%test%",
			wantClause: "LOWER(issues.body) LIKE LOWER(?)",
			wantArgs:   []any{"%test%"},
		},
		{
			name:       "in:comments only",
			inValues:   []string{"comments"},
			text:       "%test%",
			wantClause: "EXISTS (SELECT 1 FROM issue_comments WHERE repository_id = issues.repository_id AND issue_number = issues.number AND LOWER(issue_comments.body) LIKE LOWER(?))",
			wantArgs:   []any{"%test%"},
		},
		{
			name:       "in:title,comments",
			inValues:   []string{"title", "comments"},
			text:       "%test%",
			wantClause: "(LOWER(issues.title) LIKE LOWER(?) OR EXISTS (SELECT 1 FROM issue_comments WHERE repository_id = issues.repository_id AND issue_number = issues.number AND LOWER(issue_comments.body) LIKE LOWER(?)))",
			wantArgs:   []any{"%test%", "%test%"},
		},
		{
			name:       "in:body,comments",
			inValues:   []string{"body", "comments"},
			text:       "%test%",
			wantClause: "(LOWER(issues.body) LIKE LOWER(?) OR EXISTS (SELECT 1 FROM issue_comments WHERE repository_id = issues.repository_id AND issue_number = issues.number AND LOWER(issue_comments.body) LIKE LOWER(?)))",
			wantArgs:   []any{"%test%", "%test%"},
		},
		{
			name:       "in:title,body,comments",
			inValues:   []string{"title", "body", "comments"},
			text:       "%test%",
			wantClause: "(LOWER(issues.title) LIKE LOWER(?) OR LOWER(issues.body) LIKE LOWER(?) OR EXISTS (SELECT 1 FROM issue_comments WHERE repository_id = issues.repository_id AND issue_number = issues.number AND LOWER(issue_comments.body) LIKE LOWER(?)))",
			wantArgs:   []any{"%test%", "%test%", "%test%"},
		},
		{
			name:       "in:title,body (no comments)",
			inValues:   []string{"title", "body"},
			text:       "%test%",
			wantClause: "(LOWER(issues.title) LIKE LOWER(?) OR LOWER(issues.body) LIKE LOWER(?))",
			wantArgs:   []any{"%test%", "%test%"},
		},
		{
			name:       "invalid in values fallback",
			inValues:   []string{"unknown"},
			text:       "%test%",
			wantClause: "(LOWER(issues.title) LIKE LOWER(?) OR LOWER(issues.body) LIKE LOWER(?))",
			wantArgs:   []any{"%test%", "%test%"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotClause, gotArgs := searchsvc.BuildIssueTextWhere(tt.inValues, tt.text)
			if gotClause != tt.wantClause {
				t.Errorf("searchsvc.BuildIssueTextWhere(%v, %q)\ngot  clause = %q\nwant clause = %q", tt.inValues, tt.text, gotClause, tt.wantClause)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("searchsvc.BuildIssueTextWhere(%v, %q)\ngot  args = %v\nwant args = %v", tt.inValues, tt.text, gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestBuildPRTextWhere(t *testing.T) {
	tests := []struct {
		name       string
		inValues   []string
		text       string
		wantClause string
		wantArgs   []any
	}{
		{
			name:       "empty defaults to title, body, commit messages, and filenames",
			inValues:   []string{},
			text:       "%test%",
			wantClause: "(pull_requests.title LIKE ? OR pull_requests.body LIKE ? OR pull_requests.commit_messages LIKE ? OR pull_requests.filenames LIKE ?)",
			wantArgs:   []any{"%test%", "%test%", "%test%", "%test%"},
		},
		{
			name:       "in:title only",
			inValues:   []string{"title"},
			text:       "%test%",
			wantClause: "pull_requests.title LIKE ?",
			wantArgs:   []any{"%test%"},
		},
		{
			name:       "in:body only",
			inValues:   []string{"body"},
			text:       "%test%",
			wantClause: "pull_requests.body LIKE ?",
			wantArgs:   []any{"%test%"},
		},
		{
			name:       "in:comments only",
			inValues:   []string{"comments"},
			text:       "%test%",
			wantClause: "EXISTS (SELECT 1 FROM issue_comments WHERE repository_id = pull_requests.repository_id AND issue_number = pull_requests.number AND body LIKE ?)",
			wantArgs:   []any{"%test%"},
		},
		{
			name:       "in:title,comments",
			inValues:   []string{"title", "comments"},
			text:       "%test%",
			wantClause: "(pull_requests.title LIKE ? OR EXISTS (SELECT 1 FROM issue_comments WHERE repository_id = pull_requests.repository_id AND issue_number = pull_requests.number AND body LIKE ?))",
			wantArgs:   []any{"%test%", "%test%"},
		},
		{
			name:       "in:body,comments",
			inValues:   []string{"body", "comments"},
			text:       "%test%",
			wantClause: "(pull_requests.body LIKE ? OR EXISTS (SELECT 1 FROM issue_comments WHERE repository_id = pull_requests.repository_id AND issue_number = pull_requests.number AND body LIKE ?))",
			wantArgs:   []any{"%test%", "%test%"},
		},
		{
			name:       "in:title,body,comments",
			inValues:   []string{"title", "body", "comments"},
			text:       "%test%",
			wantClause: "(pull_requests.title LIKE ? OR pull_requests.body LIKE ? OR EXISTS (SELECT 1 FROM issue_comments WHERE repository_id = pull_requests.repository_id AND issue_number = pull_requests.number AND body LIKE ?))",
			wantArgs:   []any{"%test%", "%test%", "%test%"},
		},
		{
			name:       "invalid in values fallback",
			inValues:   []string{"unknown"},
			text:       "%test%",
			wantClause: "(pull_requests.title LIKE ? OR pull_requests.body LIKE ? OR pull_requests.commit_messages LIKE ? OR pull_requests.filenames LIKE ?)",
			wantArgs:   []any{"%test%", "%test%", "%test%", "%test%"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotClause, gotArgs := searchsvc.BuildPRTextWhere(tt.inValues, tt.text)
			if gotClause != tt.wantClause {
				t.Errorf("searchsvc.BuildPRTextWhere(%v, %q)\ngot  clause = %q\nwant clause = %q", tt.inValues, tt.text, gotClause, tt.wantClause)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("searchsvc.BuildPRTextWhere(%v, %q)\ngot  args = %v\nwant args = %v", tt.inValues, tt.text, gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestBuildIssueInFilter(t *testing.T) {
	tests := []struct {
		name       string
		inValues   []string
		wantClause string
		wantArgs   []any
	}{
		{
			name:       "empty defaults to no filter",
			inValues:   nil,
			wantClause: "",
			wantArgs:   nil,
		},
		{
			name:       "title only",
			inValues:   []string{"title"},
			wantClause: "issues.title IS NOT NULL AND issues.title != ''",
			wantArgs:   nil,
		},
		{
			name:       "body only",
			inValues:   []string{"body"},
			wantClause: "issues.body IS NOT NULL AND issues.body != ''",
			wantArgs:   nil,
		},
		{
			name:       "comments only treated as body",
			inValues:   []string{"comments"},
			wantClause: "issues.body IS NOT NULL AND issues.body != ''",
			wantArgs:   nil,
		},
		{
			name:       "title and body allows all",
			inValues:   []string{"title", "body"},
			wantClause: "",
			wantArgs:   nil,
		},
		{
			name:       "title and comments allows all",
			inValues:   []string{"title", "comments"},
			wantClause: "",
			wantArgs:   nil,
		},
		{
			name:       "body and comments filters body",
			inValues:   []string{"body", "comments"},
			wantClause: "issues.body IS NOT NULL AND issues.body != ''",
			wantArgs:   nil,
		},
		{
			name:       "unknown values default to no filter",
			inValues:   []string{"unknown"},
			wantClause: "",
			wantArgs:   nil,
		},
		{
			name:       "mixed valid and unknown keeps valid",
			inValues:   []string{"title", "unknown"},
			wantClause: "issues.title IS NOT NULL AND issues.title != ''",
			wantArgs:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotClause, gotArgs := searchsvc.BuildIssueInFilter(tt.inValues)
			if gotClause != tt.wantClause {
				t.Errorf("searchsvc.BuildIssueInFilter(%v)\ngot  clause = %q\nwant clause = %q", tt.inValues, gotClause, tt.wantClause)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("searchsvc.BuildIssueInFilter(%v)\ngot  args = %v\nwant args = %v", tt.inValues, gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestBuildPRInFilter(t *testing.T) {
	tests := []struct {
		name       string
		inValues   []string
		wantClause string
		wantArgs   []any
	}{
		{
			name:       "empty defaults to no filter",
			inValues:   nil,
			wantClause: "",
			wantArgs:   nil,
		},
		{
			name:       "title only",
			inValues:   []string{"title"},
			wantClause: "pull_requests.title IS NOT NULL AND pull_requests.title != ''",
			wantArgs:   nil,
		},
		{
			name:       "body only",
			inValues:   []string{"body"},
			wantClause: "pull_requests.body IS NOT NULL AND pull_requests.body != ''",
			wantArgs:   nil,
		},
		{
			name:       "title and body allows all",
			inValues:   []string{"title", "body"},
			wantClause: "",
			wantArgs:   nil,
		},
		{
			name:       "comments only ignored",
			inValues:   []string{"comments"},
			wantClause: "",
			wantArgs:   nil,
		},
		{
			name:       "title and comments keeps title filter",
			inValues:   []string{"title", "comments"},
			wantClause: "pull_requests.title IS NOT NULL AND pull_requests.title != ''",
			wantArgs:   nil,
		},
		{
			name:       "body and comments keeps body filter",
			inValues:   []string{"body", "comments"},
			wantClause: "pull_requests.body IS NOT NULL AND pull_requests.body != ''",
			wantArgs:   nil,
		},
		{
			name:       "unknown values default to no filter",
			inValues:   []string{"unknown"},
			wantClause: "",
			wantArgs:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotClause, gotArgs := searchsvc.BuildPRInFilter(tt.inValues)
			if gotClause != tt.wantClause {
				t.Errorf("searchsvc.BuildPRInFilter(%v)\ngot  clause = %q\nwant clause = %q", tt.inValues, gotClause, tt.wantClause)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("searchsvc.BuildPRInFilter(%v)\ngot  args = %v\nwant args = %v", tt.inValues, gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestSortOrder(t *testing.T) {
	tests := []struct {
		name   string
		sort   string
		prefix string
		want   string
	}{
		{
			name:   "created-asc",
			sort:   "created-asc",
			prefix: "issues",
			want:   "issues.created_at ASC",
		},
		{
			name:   "created-desc",
			sort:   "created-desc",
			prefix: "issues",
			want:   "issues.created_at DESC",
		},
		{
			name:   "updated-asc",
			sort:   "updated-asc",
			prefix: "issues",
			want:   "issues.updated_at ASC",
		},
		{
			name:   "updated-desc",
			sort:   "updated-desc",
			prefix: "issues",
			want:   "issues.updated_at DESC",
		},
		{
			name:   "comments-desc uses subquery",
			sort:   "comments-desc",
			prefix: "issues",
			want:   "(SELECT COUNT(*) FROM issue_comments WHERE issue_comments.repository_id = issues.repository_id AND issue_comments.issue_number = issues.number) DESC",
		},
		{
			name:   "comments-asc uses subquery",
			sort:   "comments-asc",
			prefix: "issues",
			want:   "(SELECT COUNT(*) FROM issue_comments WHERE issue_comments.repository_id = issues.repository_id AND issue_comments.issue_number = issues.number) ASC",
		},
		{
			name:   "reactions-desc uses subquery",
			sort:   "reactions-desc",
			prefix: "issues",
			want:   "(SELECT COUNT(*) FROM reactions WHERE reactions.issue_id = issues.id) DESC",
		},
		{
			name:   "default sort",
			sort:   "unknown",
			prefix: "issues",
			want:   "issues.id DESC",
		},
		{
			name:   "comments-desc with pull_requests prefix",
			sort:   "comments-desc",
			prefix: "pull_requests",
			want:   "(SELECT COUNT(*) FROM issue_comments WHERE issue_comments.repository_id = pull_requests.repository_id AND issue_comments.issue_number = pull_requests.number) DESC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sortOrder(tt.sort, tt.prefix)
			if got != tt.want {
				t.Errorf("sortOrder(%q, %q)\ngot  = %q\nwant = %q", tt.sort, tt.prefix, got, tt.want)
			}
		})
	}
}
