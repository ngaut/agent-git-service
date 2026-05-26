package graphql_test

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func ptr[T any](v T) *T { return &v }

// TestGraphQL_RepositoryQuery queries a repo by owner/name and asserts core fields.
func TestGraphQL_RepositoryQuery(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "repo-q",
		AutoInit:   true,
	})

	q := `
	query($owner: String!, $name: String!) {
		repository(owner: $owner, name: $name) {
			id
			name
			owner { login }
			defaultBranchRef { name }
			isPrivate
			url
		}
	}`
	data := doGql(t, mux, q, map[string]any{"owner": "tester", "name": "repo-q"})
	repo := data["repository"].(map[string]any)

	if repo["name"] != "repo-q" {
		t.Errorf("name: got %v, want repo-q", repo["name"])
	}
	owner := repo["owner"].(map[string]any)
	if owner["login"] != "tester" {
		t.Errorf("owner.login: got %v, want tester", owner["login"])
	}
	defBranch := repo["defaultBranchRef"].(map[string]any)
	if defBranch["name"] != "main" {
		t.Errorf("defaultBranchRef.name: got %v, want main", defBranch["name"])
	}
	if repo["isPrivate"] != false {
		t.Errorf("isPrivate: got %v, want false", repo["isPrivate"])
	}
	if repo["url"] == nil || repo["url"] == "" {
		t.Error("url should be non-empty")
	}
	id, ok := repo["id"].(string)
	if !ok || id == "" {
		t.Error("id should be a non-empty string")
	}
}

func TestGraphQL_RepositoryQuery_AutoMergeAllowed(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:     u.Login,
		Name:           "repo-auto-merge-q",
		AutoInit:       true,
		AllowAutoMerge: ptr(true),
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	q := `
	query($owner: String!, $name: String!) {
		repository(owner: $owner, name: $name) {
			name
			autoMergeAllowed
		}
	}`
	data := doGql(t, mux, q, map[string]any{"owner": "tester", "name": "repo-auto-merge-q"})
	repo := data["repository"].(map[string]any)

	if repo["name"] != "repo-auto-merge-q" {
		t.Errorf("name: got %v, want repo-auto-merge-q", repo["name"])
	}
	if repo["autoMergeAllowed"] != true {
		t.Errorf("autoMergeAllowed: got %v, want true", repo["autoMergeAllowed"])
	}
}

// TestGraphQL_IssueQuery queries a single issue with labels, assignees, and comments.
func TestGraphQL_IssueQuery(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Migrate Label table (not in default setupTestEnvironment).
	svc.DB.AutoMigrate(&db.Label{})

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "iq-repo",
		AutoInit:   true,
	})

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "tester/iq-repo",
		Title:        "Test Issue",
		Body:         "issue body",
		AuthorLogin:  "tester",
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Create the "bug" label explicitly and attach it to the issue.
	label, err := svc.CreateLabel(ctx, "tester/iq-repo", "bug", "d73a4a", "Something isn't working")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	if err := svc.DB.Exec("INSERT INTO issue_labels (issue_id, label_id) VALUES (?, ?)", issue.ID, label.ID).Error; err != nil {
		t.Fatalf("attach label: %v", err)
	}

	// Set assignees on the issue.
	svc.DB.Model(&db.Issue{}).Where("id = ?", issue.ID).Update("assignee_logins", "tester")

	// Create two comments on the issue.
	svc.CreateIssueComment(ctx, "tester/iq-repo", issue.Number, "first comment", "tester", nil)
	svc.CreateIssueComment(ctx, "tester/iq-repo", issue.Number, "second comment", "tester", nil)

	q := `
	query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) {
			issue(number: $number) {
				id
				number
				title
				state
				author { login }
				labels(first: 10) { nodes { name } }
				assignees(first: 10) { nodes { login } }
				comments(first: 10) {
					totalCount
					nodes { id body author { login } createdAt }
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "iq-repo", "number": float64(issue.Number),
	})
	repoData := data["repository"].(map[string]any)
	iss := repoData["issue"].(map[string]any)

	if iss["number"] != float64(issue.Number) {
		t.Errorf("number: got %v, want %d", iss["number"], issue.Number)
	}
	if iss["title"] != "Test Issue" {
		t.Errorf("title: got %v, want Test Issue", iss["title"])
	}
	if iss["state"] != "OPEN" {
		t.Errorf("state: got %v, want OPEN", iss["state"])
	}
	author := iss["author"].(map[string]any)
	if author["login"] != "tester" {
		t.Errorf("author.login: got %v, want tester", author["login"])
	}

	// Assert labels.
	labels := iss["labels"].(map[string]any)
	labelNodes := labels["nodes"].([]any)
	if len(labelNodes) != 1 {
		t.Fatalf("expected 1 label, got %d", len(labelNodes))
	}
	if labelNodes[0].(map[string]any)["name"] != "bug" {
		t.Errorf("label name: got %v, want bug", labelNodes[0].(map[string]any)["name"])
	}

	// Assert assignees.
	assignees := iss["assignees"].(map[string]any)
	assigneeNodes := assignees["nodes"].([]any)
	if len(assigneeNodes) != 1 {
		t.Fatalf("expected 1 assignee, got %d", len(assigneeNodes))
	}
	if assigneeNodes[0].(map[string]any)["login"] != "tester" {
		t.Errorf("assignee login: got %v, want tester", assigneeNodes[0].(map[string]any)["login"])
	}

	// Assert comments.
	comments := iss["comments"].(map[string]any)
	totalCount := comments["totalCount"].(float64)
	if totalCount != 2 {
		t.Errorf("comments totalCount: got %v, want 2", totalCount)
	}
	commentNodes := comments["nodes"].([]any)
	if len(commentNodes) != 2 {
		t.Fatalf("expected 2 comment nodes, got %d", len(commentNodes))
	}
	c0 := commentNodes[0].(map[string]any)
	if c0["body"] != "first comment" {
		t.Errorf("comment[0].body: got %v, want 'first comment'", c0["body"])
	}
	if c0["author"].(map[string]any)["login"] != "tester" {
		t.Errorf("comment[0].author.login: got %v, want tester", c0["author"].(map[string]any)["login"])
	}
	if c0["id"] == nil || c0["id"] == "" {
		t.Error("comment[0].id should be non-empty")
	}
	if c0["createdAt"] == nil || c0["createdAt"] == "" {
		t.Error("comment[0].createdAt should be non-empty")
	}
	c1 := commentNodes[1].(map[string]any)
	if c1["body"] != "second comment" {
		t.Errorf("comment[1].body: got %v, want 'second comment'", c1["body"])
	}
}

func TestGraphQL_IssueQuery_PinnedCommentsAppearFirst(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "iq-pinned-repo",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "tester/iq-pinned-repo",
		Title:        "Pinned comment ordering",
		Body:         "issue body",
		AuthorLogin:  "tester",
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	first, err := svc.CreateIssueComment(ctx, "tester/iq-pinned-repo", issue.Number, "first comment", "tester", nil)
	if err != nil {
		t.Fatalf("CreateIssueComment first: %v", err)
	}
	second, err := svc.CreateIssueComment(ctx, "tester/iq-pinned-repo", issue.Number, "second comment", "tester", nil)
	if err != nil {
		t.Fatalf("CreateIssueComment second: %v", err)
	}
	if err := svc.PinIssueComment(ctx, second.ID, true); err != nil {
		t.Fatalf("PinIssueComment: %v", err)
	}

	q := `
	query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) {
			issue(number: $number) {
				comments(first: 10) {
					nodes {
						body
						isPinned
					}
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "iq-pinned-repo", "number": float64(issue.Number),
	})

	repoData := data["repository"].(map[string]any)
	iss := repoData["issue"].(map[string]any)
	commentNodes := iss["comments"].(map[string]any)["nodes"].([]any)
	if len(commentNodes) != 2 {
		t.Fatalf("expected 2 comment nodes, got %d", len(commentNodes))
	}

	firstNode := commentNodes[0].(map[string]any)
	secondNode := commentNodes[1].(map[string]any)
	if firstNode["body"] != "second comment" || firstNode["isPinned"] != true {
		t.Fatalf("comment[0] = %#v, want pinned second comment first", firstNode)
	}
	if secondNode["body"] != "first comment" || secondNode["isPinned"] != false {
		t.Fatalf("comment[1] = %#v, want unpinned first comment second", secondNode)
	}

	storedFirst, err := svc.GetIssueCommentByID(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetIssueCommentByID first: %v", err)
	}
	if storedFirst.IsPinned {
		t.Fatal("expected first comment to remain unpinned")
	}
}

// TestGraphQL_PRQuery queries a single PR by number with head/base refs and review status.
func TestGraphQL_PRQuery(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Migrate PullRequestReview table for review coverage.
	svc.DB.AutoMigrate(&db.PullRequestReview{})

	svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "pr-repo",
		AutoInit:   true,
	})
	svc.Git.CreateBranch(ctx, "tester/pr-repo", "feature", "main")

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "tester/pr-repo",
		Title:        "Add feature",
		Body:         "pr body",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  "tester",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	// Submit an APPROVED review so reviewDecision is populated.
	if _, err := svc.AddPRReview(ctx, pr.ID, "reviewer1", db.ReviewApproved, "lgtm", ""); err != nil {
		t.Fatalf("AddPRReview(APPROVED): %v", err)
	}

	q := `
	query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) {
			pullRequest(number: $number) {
				id
				number
				title
				state
				headRefName
				baseRefName
				isDraft
				reviewDecision
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "pr-repo", "number": float64(pr.Number),
	})
	repoData := data["repository"].(map[string]any)
	prData := repoData["pullRequest"].(map[string]any)

	if prData["number"] != float64(pr.Number) {
		t.Errorf("number: got %v, want %d", prData["number"], pr.Number)
	}
	if prData["title"] != "Add feature" {
		t.Errorf("title: got %v, want 'Add feature'", prData["title"])
	}
	if prData["state"] != "OPEN" {
		t.Errorf("state: got %v, want OPEN", prData["state"])
	}
	if prData["headRefName"] != "feature" {
		t.Errorf("headRefName: got %v, want feature", prData["headRefName"])
	}
	if prData["baseRefName"] != "main" {
		t.Errorf("baseRefName: got %v, want main", prData["baseRefName"])
	}
	if prData["isDraft"] != false {
		t.Errorf("isDraft: got %v, want false", prData["isDraft"])
	}
	if prData["reviewDecision"] != db.ReviewApproved {
		t.Errorf("reviewDecision: got %v, want %s", prData["reviewDecision"], db.ReviewApproved)
	}

	// Add a CHANGES_REQUESTED review — it should override APPROVED.
	if _, err := svc.AddPRReview(ctx, pr.ID, "reviewer2", db.ReviewChangesRequested, "needs work", ""); err != nil {
		t.Fatalf("AddPRReview(CHANGES_REQUESTED): %v", err)
	}

	data2 := doGql(t, mux, q, map[string]any{
		"owner": "tester", "name": "pr-repo", "number": float64(pr.Number),
	})
	prData2 := data2["repository"].(map[string]any)["pullRequest"].(map[string]any)
	if prData2["reviewDecision"] != db.ReviewChangesRequested {
		t.Errorf("reviewDecision after CHANGES_REQUESTED: got %v, want %s", prData2["reviewDecision"], db.ReviewChangesRequested)
	}
}

// TestGraphQL_CreateIssueMutation creates an issue via GraphQL mutation and verifies the return.
func TestGraphQL_CreateIssueMutation(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, _ := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "ci-repo",
		AutoInit:   true,
	})
	repoNodeID := fmt.Sprintf("Repository_%d", repo.ID)

	q := `
	mutation($input: CreateIssueInput!) {
		createIssue(input: $input) {
			issue {
				id
				number
				title
				body
				state
				author { login }
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"input": map[string]any{
			"repositoryId": repoNodeID,
			"title":        "GQL Issue",
			"body":         "via mutation",
		},
	})
	ci := data["createIssue"].(map[string]any)
	iss := ci["issue"].(map[string]any)

	if iss["title"] != "GQL Issue" {
		t.Errorf("title: got %v, want 'GQL Issue'", iss["title"])
	}
	if iss["body"] != "via mutation" {
		t.Errorf("body: got %v, want 'via mutation'", iss["body"])
	}
	if iss["state"] != "OPEN" {
		t.Errorf("state: got %v, want OPEN", iss["state"])
	}
	if iss["author"].(map[string]any)["login"] != "tester" {
		t.Errorf("author.login: got %v, want tester", iss["author"].(map[string]any)["login"])
	}
	if iss["number"] == nil || iss["number"].(float64) < 1 {
		t.Errorf("number should be >= 1, got %v", iss["number"])
	}
	if iss["id"] == nil || iss["id"] == "" {
		t.Error("id should be non-empty")
	}
}

// TestGraphQL_MergePRMutation creates PRs with deterministic git state, merges them
// using each supported strategy (MERGE, SQUASH, REBASE), and verifies strategy-
// discriminating git topology:
//   - MERGE: 2-parent merge commit, user-provided commitHeadline, first-parent delta = 1
//   - SQUASH: 1-parent squashed commit (all branch changes in one commit),
//     server-default message, first-parent delta = 1
//   - REBASE: 1-parent linear commits (replayed individually), first-parent delta = 2
func TestGraphQL_MergePRMutation(t *testing.T) {
	strategies := []string{"MERGE", "SQUASH", "REBASE"}
	for _, strategy := range strategies {
		t.Run(strategy, func(t *testing.T) {
			svc, mux, u, cleanup := setupTestEnvironment(t)
			defer cleanup()
			ctx := context.Background()

			repoName := fmt.Sprintf("merge-%s-repo", strategy)
			fullName := fmt.Sprintf("tester/%s", repoName)
			branchName := fmt.Sprintf("feat-%s", strategy)

			if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
				OwnerLogin: u.Login,
				Name:       repoName,
				AutoInit:   true,
			}); err != nil {
				t.Fatalf("CreateRepo: %v", err)
			}

			if err := svc.Git.CreateBranch(ctx, fullName, branchName, "main"); err != nil {
				t.Fatalf("CreateBranch: %v", err)
			}

			// Write two files to create distinct commits on the feature branch.
			if _, err := svc.Git.WriteFile(ctx, fullName, branchName, "f1.txt", "first commit", []byte("content1\n")); err != nil {
				t.Fatalf("WriteFile(f1): %v", err)
			}
			if _, err := svc.Git.WriteFile(ctx, fullName, branchName, "f2.txt", "second commit", []byte("content2\n")); err != nil {
				t.Fatalf("WriteFile(f2): %v", err)
			}

			pr, err := svc.CreatePR(ctx, service.CreatePRInput{
				RepoFullName: fullName,
				Title:        "Merge via " + strategy,
				HeadRef:      branchName,
				BaseRef:      "main",
				AuthorLogin:  "tester",
			})
			if err != nil {
				t.Fatalf("CreatePR: %v", err)
			}
			prNodeID := fmt.Sprintf("PullRequest_%d", pr.ID)
			repoPath, err := svc.Git.GetRepoPath(ctx, fullName)
			if err != nil {
				t.Fatalf("GetRepoPath failed: %v", err)
			}

			// Build mutation input. Only MERGE supplies a custom commitHeadline
			// so the assertion is payload-derived. SQUASH omits the headline
			// and must receive the server-default message, making the two
			// subtests non-interchangeable.
			mutInput := map[string]any{
				"pullRequestId": prNodeID,
				"mergeMethod":   strategy,
			}
			var expectedHeadline string
			switch strategy {
			case "MERGE":
				expectedHeadline = fmt.Sprintf("merge-commit: PR #%d via MERGE", pr.Number)
				mutInput["commitHeadline"] = expectedHeadline
			case "SQUASH":
				// No commitHeadline — server must use default message.
				expectedHeadline = fmt.Sprintf("Merge pull request #%d", pr.Number)
			}

			// Capture first-parent commit count on main before merge.
			preCountOut, err := exec.CommandContext(ctx, "git", "-C", repoPath,
				"rev-list", "--first-parent", "--count", "refs/heads/main").Output()
			if err != nil {
				t.Fatalf("pre-merge rev-list --first-parent --count: %v", err)
			}
			var preCount int
			fmt.Sscanf(strings.TrimSpace(string(preCountOut)), "%d", &preCount)

			// Execute the merge mutation.
			mergeMut := `
			mutation($input: MergePullRequestInput!) {
				mergePullRequest(input: $input) {
					pullRequest {
						merged
						state
					}
				}
			}`
			data := doGql(t, mux, mergeMut, map[string]any{
				"input": mutInput,
			})
			mp := data["mergePullRequest"].(map[string]any)
			prResult := mp["pullRequest"].(map[string]any)

			if prResult["merged"] != true {
				t.Errorf("merged: got %v, want true", prResult["merged"])
			}
			if prResult["state"] != "MERGED" {
				t.Errorf("state: got %v, want MERGED", prResult["state"])
			}

			// Re-query the PR to verify merge metadata recorded in the DB.
			prQuery := `
			query($owner: String!, $name: String!, $number: Int!) {
				repository(owner: $owner, name: $name) {
					pullRequest(number: $number) {
						merged
						state
						mergeCommit { oid }
						mergedBy { login }
						mergedAt
					}
				}
			}`
			data2 := doGql(t, mux, prQuery, map[string]any{
				"owner": "tester", "name": repoName, "number": float64(pr.Number),
			})
			prData := data2["repository"].(map[string]any)["pullRequest"].(map[string]any)

			if prData["merged"] != true {
				t.Errorf("query merged: got %v, want true", prData["merged"])
			}
			mergedAt, _ := prData["mergedAt"].(string)
			if mergedAt == "" {
				t.Error("mergedAt should be non-empty after merge")
			}
			mergedBy, _ := prData["mergedBy"].(map[string]any)
			if mergedBy == nil || mergedBy["login"] != "tester" {
				t.Errorf("mergedBy.login: got %v, want tester", mergedBy)
			}

			mergeCommit, _ := prData["mergeCommit"].(map[string]any)
			if mergeCommit == nil {
				t.Fatal("mergeCommit should be present after merge")
			}
			mergeOid, _ := mergeCommit["oid"].(string)
			if mergeOid == "" {
				t.Fatal("mergeCommit.oid should be non-empty")
			}

			// Inspect the merge commit in git to verify strategy-specific routing.
			parentOut, err := exec.CommandContext(ctx, "git", "-C", repoPath,
				"rev-list", "--parents", "-1", mergeOid).Output()
			if err != nil {
				t.Fatalf("git rev-list --parents: %v", err)
			}
			// Output format: "<sha> <parent1> [<parent2>]"; count tokens minus 1.
			parentCount := len(strings.Fields(strings.TrimSpace(string(parentOut)))) - 1

			msgOut, err := exec.CommandContext(ctx, "git", "-C", repoPath,
				"log", "-1", "--format=%s", mergeOid).Output()
			if err != nil {
				t.Fatalf("git log --format=%%s: %v", err)
			}
			commitMsg := strings.TrimSpace(string(msgOut))

			// First-parent count after merge to verify commit topology.
			postCountOut, err := exec.CommandContext(ctx, "git", "-C", repoPath,
				"rev-list", "--first-parent", "--count", "refs/heads/main").Output()
			if err != nil {
				t.Fatalf("post-merge rev-list --first-parent --count: %v", err)
			}
			var postCount int
			fmt.Sscanf(strings.TrimSpace(string(postCountOut)), "%d", &postCount)
			firstParentDelta := postCount - preCount

			switch strategy {
			case "MERGE":
				// MERGE creates a 2-parent merge commit with the custom headline.
				// First-parent chain grows by exactly 1 (the merge commit).
				if parentCount != 2 {
					t.Errorf("MERGE: expected 2-parent merge commit, got %d parents", parentCount)
				}
				if commitMsg != expectedHeadline {
					t.Errorf("MERGE: commit message = %q, want %q", commitMsg, expectedHeadline)
				}
				if firstParentDelta != 1 {
					t.Errorf("MERGE: first-parent delta = %d, want 1", firstParentDelta)
				}
			case "SQUASH":
				// SQUASH creates a 1-parent commit (not a merge commit) containing
				// all branch changes squashed into a single commit. This is distinct
				// from MERGE (2-parent) and REBASE (1-parent but delta=2 for replayed
				// individual commits).
				if parentCount != 1 {
					t.Errorf("SQUASH: expected 1-parent squashed commit, got %d parents", parentCount)
				}
				if commitMsg != expectedHeadline {
					t.Errorf("SQUASH: commit message = %q, want server-default %q", commitMsg, expectedHeadline)
				}
				if firstParentDelta != 1 {
					t.Errorf("SQUASH: first-parent delta = %d, want 1 (single squashed commit)", firstParentDelta)
				}

				// Content completeness guard: SQUASH must include all branch changes,
				// not just the tip commit subset.
				diffOut, err := exec.CommandContext(ctx, "git", "-C", repoPath,
					"diff-tree", "--no-commit-id", "--name-only", "-r", mergeOid).Output()
				if err != nil {
					t.Fatalf("SQUASH: git diff-tree: %v", err)
				}
				changed := strings.Fields(strings.TrimSpace(string(diffOut)))
				hasF1, hasF2 := false, false
				for _, f := range changed {
					switch f {
					case "f1.txt":
						hasF1 = true
					case "f2.txt":
						hasF2 = true
					}
				}
				if !hasF1 || !hasF2 {
					t.Errorf("SQUASH: incomplete merged diff, changed=%v (want both f1.txt and f2.txt)", changed)
				}

				for _, fc := range []struct {
					file string
					want string
				}{
					{file: "f1.txt", want: "content1\n"},
					{file: "f2.txt", want: "content2\n"},
				} {
					out, err := exec.CommandContext(ctx, "git", "-C", repoPath,
						"show", "refs/heads/main:"+fc.file).Output()
					if err != nil {
						t.Errorf("SQUASH: %s missing on main: %v", fc.file, err)
						continue
					}
					if string(out) != fc.want {
						t.Errorf("SQUASH: %s content = %q, want %q", fc.file, string(out), fc.want)
					}
				}
			case "REBASE":
				// REBASE replays branch commits linearly (no merge commit).
				// Commit message should be the original branch tip commit,
				// not any merge/squash message.
				// First-parent chain grows by 2 (the replayed branch commits).
				if parentCount != 1 {
					t.Errorf("REBASE: expected 1-parent linear commit, got %d parents", parentCount)
				}
				if commitMsg != "second commit" {
					t.Errorf("REBASE: commit message = %q, want %q (original branch tip)", commitMsg, "second commit")
				}
				if firstParentDelta != 2 {
					t.Errorf("REBASE: first-parent delta = %d, want 2 (replayed branch commits)", firstParentDelta)
				}
			}
		})
	}
}

func TestGraphQL_MergePRMutation_FailureCases(t *testing.T) {
	mergeMut := `
	mutation($input: MergePullRequestInput!) {
		mergePullRequest(input: $input) {
			pullRequest {
				merged
				state
			}
		}
	}`

	t.Run("MissingPullRequestID", func(t *testing.T) {
		svc, mux, u, cleanup := setupTestEnvironment(t)
		defer cleanup()
		ctx := context.Background()

		repoName := "merge-missing-id-repo"
		fullName := fmt.Sprintf("%s/%s", u.Login, repoName)
		branchName := "feature-missing"

		if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: u.Login,
			Name:       repoName,
			AutoInit:   true,
		}); err != nil {
			t.Fatalf("CreateRepo: %v", err)
		}
		if err := svc.Git.CreateBranch(ctx, fullName, branchName, "main"); err != nil {
			t.Fatalf("CreateBranch: %v", err)
		}
		if _, err := svc.Git.WriteFile(ctx, fullName, branchName, "f1.txt", "feature commit", []byte("feature\n")); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		pr, err := svc.CreatePR(ctx, service.CreatePRInput{
			RepoFullName: fullName,
			Title:        "Missing ID PR",
			HeadRef:      branchName,
			BaseRef:      "main",
			AuthorLogin:  u.Login,
		})
		if err != nil {
			t.Fatalf("CreatePR: %v", err)
		}

		before := snapshotMergeState(t, mux, svc, ctx, u.Login, repoName, pr.Number, "main", branchName)
		if before.merged || before.mergeOID != "" {
			t.Fatalf("precondition failed: expected unmerged PR, got %+v", before)
		}

		res := doRawGql(t, mux, mergeMut, map[string]any{
			"input": map[string]any{
				"mergeMethod": "MERGE",
			},
		})
		msg := firstGQLErrorMessage(t, res)
		if !strings.Contains(msg, "invalid pull request ID") {
			t.Errorf("error message: got %q, want substring %q", msg, "invalid pull request ID")
		}

		after := snapshotMergeState(t, mux, svc, ctx, u.Login, repoName, pr.Number, "main", branchName)
		if after != before {
			t.Errorf("post-merge state changed: got %+v, want %+v", after, before)
		}
	})

	t.Run("NonexistentPullRequestID", func(t *testing.T) {
		svc, mux, u, cleanup := setupTestEnvironment(t)
		defer cleanup()
		ctx := context.Background()

		repoName := "merge-invalid-id-repo"
		fullName := fmt.Sprintf("%s/%s", u.Login, repoName)
		branchName := "feature-invalid"

		if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: u.Login,
			Name:       repoName,
			AutoInit:   true,
		}); err != nil {
			t.Fatalf("CreateRepo: %v", err)
		}
		if err := svc.Git.CreateBranch(ctx, fullName, branchName, "main"); err != nil {
			t.Fatalf("CreateBranch: %v", err)
		}
		if _, err := svc.Git.WriteFile(ctx, fullName, branchName, "f1.txt", "feature commit", []byte("feature\n")); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		pr, err := svc.CreatePR(ctx, service.CreatePRInput{
			RepoFullName: fullName,
			Title:        "Invalid ID PR",
			HeadRef:      branchName,
			BaseRef:      "main",
			AuthorLogin:  u.Login,
		})
		if err != nil {
			t.Fatalf("CreatePR: %v", err)
		}

		before := snapshotMergeState(t, mux, svc, ctx, u.Login, repoName, pr.Number, "main", branchName)
		if before.merged || before.mergeOID != "" {
			t.Fatalf("precondition failed: expected unmerged PR, got %+v", before)
		}

		res := doRawGql(t, mux, mergeMut, map[string]any{
			"input": map[string]any{
				"pullRequestId": "PullRequest_99999",
				"mergeMethod":   "MERGE",
			},
		})
		msg := firstGQLErrorMessage(t, res)
		if !strings.Contains(strings.ToLower(msg), "not found") {
			t.Errorf("error message: got %q, want substring %q", msg, "not found")
		}

		after := snapshotMergeState(t, mux, svc, ctx, u.Login, repoName, pr.Number, "main", branchName)
		if after != before {
			t.Errorf("post-merge state changed: got %+v, want %+v", after, before)
		}
	})

	t.Run("MergeConflict", func(t *testing.T) {
		svc, mux, u, cleanup := setupTestEnvironment(t)
		defer cleanup()
		ctx := context.Background()

		repoName := "merge-conflict-repo"
		fullName := fmt.Sprintf("%s/%s", u.Login, repoName)
		branchName := "feature-conflict"

		if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: u.Login,
			Name:       repoName,
			AutoInit:   true,
		}); err != nil {
			t.Fatalf("CreateRepo: %v", err)
		}
		if err := svc.Git.CreateBranch(ctx, fullName, branchName, "main"); err != nil {
			t.Fatalf("CreateBranch: %v", err)
		}
		if _, err := svc.Git.WriteFile(ctx, fullName, branchName, "conflict.txt", "feature commit", []byte("feature content\n")); err != nil {
			t.Fatalf("WriteFile(feature): %v", err)
		}
		if _, err := svc.Git.WriteFile(ctx, fullName, "main", "conflict.txt", "main commit", []byte("main content\n")); err != nil {
			t.Fatalf("WriteFile(main): %v", err)
		}

		pr, err := svc.CreatePR(ctx, service.CreatePRInput{
			RepoFullName: fullName,
			Title:        "Conflict PR",
			HeadRef:      branchName,
			BaseRef:      "main",
			AuthorLogin:  u.Login,
		})
		if err != nil {
			t.Fatalf("CreatePR: %v", err)
		}

		before := snapshotMergeState(t, mux, svc, ctx, u.Login, repoName, pr.Number, "main", branchName)
		if before.merged || before.mergeOID != "" {
			t.Fatalf("precondition failed: expected unmerged PR, got %+v", before)
		}

		res := doRawGql(t, mux, mergeMut, map[string]any{
			"input": map[string]any{
				"pullRequestId": fmt.Sprintf("PullRequest_%d", pr.ID),
				"mergeMethod":   "MERGE",
			},
		})
		msg := firstGQLErrorMessage(t, res)
		if !strings.Contains(strings.ToLower(msg), "conflict") {
			t.Errorf("error message: got %q, want substring %q", msg, "conflict")
		}

		after := snapshotMergeState(t, mux, svc, ctx, u.Login, repoName, pr.Number, "main", branchName)
		if after != before {
			t.Errorf("post-merge state changed: got %+v, want %+v", after, before)
		}
	})
}

type prMergeSnapshot struct {
	merged   bool
	state    string
	mergeOID string
	baseSHA  string
	headSHA  string
}

func snapshotMergeState(t *testing.T, mux http.Handler, svc *service.Service, ctx context.Context, owner, repo string, prNumber int, baseRef, headRef string) prMergeSnapshot {
	t.Helper()

	prQuery := `
	query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) {
			pullRequest(number: $number) {
				merged
				state
				mergeCommit { oid }
			}
		}
	}`
	data := doGql(t, mux, prQuery, map[string]any{
		"owner":  owner,
		"name":   repo,
		"number": float64(prNumber),
	})
	prData := data["repository"].(map[string]any)["pullRequest"].(map[string]any)

	merged, _ := prData["merged"].(bool)
	state, _ := prData["state"].(string)
	mergeOID := ""
	if mergeCommit, ok := prData["mergeCommit"].(map[string]any); ok && mergeCommit != nil {
		mergeOID, _ = mergeCommit["oid"].(string)
	}

	fullName := fmt.Sprintf("%s/%s", owner, repo)
	baseSHA, err := svc.Git.HeadSHA(ctx, fullName, baseRef)
	require.NoError(t, err)
	headSHA, err := svc.Git.HeadSHA(ctx, fullName, headRef)
	require.NoError(t, err)

	return prMergeSnapshot{
		merged:   merged,
		state:    state,
		mergeOID: mergeOID,
		baseSHA:  baseSHA,
		headSHA:  headSHA,
	}
}

func firstGQLErrorMessage(t *testing.T, res map[string]any) string {
	t.Helper()
	errs, ok := res["errors"].([]any)
	if !ok || len(errs) == 0 {
		t.Fatalf("expected errors in response, got: %v", res)
	}
	firstErr, _ := errs[0].(map[string]any)
	msg, _ := firstErr["message"].(string)
	if msg == "" {
		t.Fatalf("expected error message, got: %v", firstErr)
	}
	return msg
}

// TestGraphQL_ProjectQuery queries a project by owner and number, including fields and items.
func TestGraphQL_ProjectQuery(t *testing.T) {
	svc, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	proj, err := svc.CreateProject(ctx, "tester", "My Board")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Add project fields.
	fieldStatus := &db.ProjectField{ProjectID: proj.ID, Name: "Status", DataType: "SINGLE_SELECT", Options: `[{"id":"opt1","name":"Todo"},{"id":"opt2","name":"Done"}]`}
	if err := svc.CreateProjectField(ctx, fieldStatus); err != nil {
		t.Fatalf("CreateProjectField(Status): %v", err)
	}
	fieldPriority := &db.ProjectField{ProjectID: proj.ID, Name: "Priority", DataType: "NUMBER"}
	if err := svc.CreateProjectField(ctx, fieldPriority); err != nil {
		t.Fatalf("CreateProjectField(Priority): %v", err)
	}

	// Add project items (draft issues).
	item1 := &db.ProjectItem{ProjectID: proj.ID, Type: "DRAFT_ISSUE", DraftTitle: "Task A", DraftBody: "body a"}
	if err := svc.CreateProjectItem(ctx, item1); err != nil {
		t.Fatalf("CreateProjectItem(1): %v", err)
	}
	item2 := &db.ProjectItem{ProjectID: proj.ID, Type: "DRAFT_ISSUE", DraftTitle: "Task B", DraftBody: "body b"}
	if err := svc.CreateProjectItem(ctx, item2); err != nil {
		t.Fatalf("CreateProjectItem(2): %v", err)
	}

	q := `
	query($login: String!, $number: Int!) {
		organization(login: $login) {
			projectV2(number: $number) {
				id
				title
				number
				fields(first: 20) {
					totalCount
					nodes {
						... on ProjectV2Field { id name dataType }
						... on ProjectV2SingleSelectField { id name dataType options { id name } }
					}
				}
				items(first: 20) {
					totalCount
					nodes {
						id
						type
						content {
							... on DraftIssue { title body }
						}
					}
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"login":  "tester",
		"number": float64(proj.Number),
	})
	org := data["organization"].(map[string]any)
	projData := org["projectV2"].(map[string]any)

	if projData["title"] != "My Board" {
		t.Errorf("title: got %v, want 'My Board'", projData["title"])
	}
	if projData["number"] != float64(proj.Number) {
		t.Errorf("number: got %v, want %d", projData["number"], proj.Number)
	}
	if projData["id"] == nil || projData["id"] == "" {
		t.Error("id should be non-empty")
	}

	// Assert fields.
	fields := projData["fields"].(map[string]any)
	fieldCount := fields["totalCount"].(float64)
	if fieldCount != 2 {
		t.Errorf("fields.totalCount: got %v, want 2", fieldCount)
	}
	fieldNodes := fields["nodes"].([]any)
	if len(fieldNodes) != 2 {
		t.Fatalf("expected 2 field nodes, got %d", len(fieldNodes))
	}
	f0 := fieldNodes[0].(map[string]any)
	if f0["name"] != "Status" {
		t.Errorf("fields[0].name: got %v, want Status", f0["name"])
	}
	if f0["dataType"] != "SINGLE_SELECT" {
		t.Errorf("fields[0].dataType: got %v, want SINGLE_SELECT", f0["dataType"])
	}
	// Verify SINGLE_SELECT options are present.
	opts, ok := f0["options"].([]any)
	if !ok || len(opts) != 2 {
		t.Errorf("fields[0].options: expected 2 options, got %v", f0["options"])
	}
	f1 := fieldNodes[1].(map[string]any)
	if f1["name"] != "Priority" {
		t.Errorf("fields[1].name: got %v, want Priority", f1["name"])
	}

	// Assert items.
	items := projData["items"].(map[string]any)
	itemCount := items["totalCount"].(float64)
	if itemCount != 2 {
		t.Errorf("items.totalCount: got %v, want 2", itemCount)
	}
	itemNodes := items["nodes"].([]any)
	if len(itemNodes) != 2 {
		t.Fatalf("expected 2 item nodes, got %d", len(itemNodes))
	}
	i0 := itemNodes[0].(map[string]any)
	if i0["type"] != "DRAFT_ISSUE" {
		t.Errorf("items[0].type: got %v, want DRAFT_ISSUE", i0["type"])
	}
	content0 := i0["content"].(map[string]any)
	if content0["title"] != "Task A" {
		t.Errorf("items[0].content.title: got %v, want 'Task A'", content0["title"])
	}
	i1 := itemNodes[1].(map[string]any)
	content1 := i1["content"].(map[string]any)
	if content1["title"] != "Task B" {
		t.Errorf("items[1].content.title: got %v, want 'Task B'", content1["title"])
	}
}

func TestGraphQL_RepositoryProjectsV2ReturnsOnlyLinkedProjects(t *testing.T) {
	svc, mux, _, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "tester",
		Name:       "repo-projects",
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	linked, err := svc.CreateProject(ctx, "tester", "Linked Project")
	if err != nil {
		t.Fatalf("CreateProject(linked): %v", err)
	}
	unlinked, err := svc.CreateProject(ctx, "tester", "Unlinked Project")
	if err != nil {
		t.Fatalf("CreateProject(unlinked): %v", err)
	}
	if err := svc.LinkProjectToRepo(ctx, linked.ID, repo.ID); err != nil {
		t.Fatalf("LinkProjectToRepo: %v", err)
	}
	if err := svc.CreateProjectField(ctx, &db.ProjectField{
		ProjectID: linked.ID,
		Name:      "Status",
		DataType:  "SINGLE_SELECT",
		Options:   `[{"id":"todo","name":"Todo"}]`,
	}); err != nil {
		t.Fatalf("CreateProjectField(linked): %v", err)
	}
	linked.Readme = "linked readme"
	linked.ShortDescription = "linked short description"
	linked.Public = true
	if err := svc.DB.Save(&linked).Error; err != nil {
		t.Fatalf("Save(linked): %v", err)
	}
	linkedItem := &db.ProjectItem{
		ProjectID:  linked.ID,
		Type:       "DRAFT_ISSUE",
		DraftTitle: "Linked draft",
		DraftBody:  "linked body",
	}
	if err := svc.CreateProjectItem(ctx, linkedItem); err != nil {
		t.Fatalf("CreateProjectItem(linked): %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "tester",
		Name:       "other-repo",
	}); err != nil {
		t.Fatalf("CreateRepo(other): %v", err)
	}

	q := `
	query($owner: String!, $name: String!) {
		repository(owner: $owner, name: $name) {
			projectsV2(first: 20) {
				nodes {
					title
					number
					closed
					resourcePath
					url
					shortDescription
					public
					readme
					owner { login }
					fields(first: 20) {
						totalCount
					}
					items(first: 20) {
						totalCount
					}
				}
			}
		}
	}`
	data := doGql(t, mux, q, map[string]any{
		"owner": "tester",
		"name":  "repo-projects",
	})
	repository := data["repository"].(map[string]any)
	projects := repository["projectsV2"].(map[string]any)
	nodes := projects["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("expected 1 linked project, got %d (%v)", len(nodes), nodes)
	}
	project := nodes[0].(map[string]any)
	if project["title"] != linked.Title {
		t.Fatalf("title = %v, want %q", project["title"], linked.Title)
	}
	if project["resourcePath"] != "/tester/repo-projects/projects/1" {
		t.Fatalf("resourcePath = %v, want /tester/repo-projects/projects/1", project["resourcePath"])
	}
	if project["url"] != "https://localhost:8080/tester/repo-projects/projects/1" {
		t.Fatalf("url = %v, want https://localhost:8080/tester/repo-projects/projects/1", project["url"])
	}
	if project["shortDescription"] != linked.ShortDescription {
		t.Fatalf("shortDescription = %v, want %q", project["shortDescription"], linked.ShortDescription)
	}
	if project["public"] != linked.Public {
		t.Fatalf("public = %v, want %v", project["public"], linked.Public)
	}
	if project["readme"] != linked.Readme {
		t.Fatalf("readme = %v, want %q", project["readme"], linked.Readme)
	}
	owner := project["owner"].(map[string]any)
	if owner["login"] != "tester" {
		t.Fatalf("owner.login = %v, want tester", owner["login"])
	}
	fields := project["fields"].(map[string]any)
	if fields["totalCount"] != float64(1) {
		t.Fatalf("fields.totalCount = %v, want 1", fields["totalCount"])
	}
	items := project["items"].(map[string]any)
	if items["totalCount"] != float64(1) {
		t.Fatalf("items.totalCount = %v, want 1", items["totalCount"])
	}
	for _, node := range nodes {
		if node.(map[string]any)["title"] == unlinked.Title {
			t.Fatalf("unexpected unlinked project in repository.projectsV2: %v", node)
		}
	}
}

// TestProjectV2ItemFieldValuePersistence tests the full cycle of:
// 1. Creating a project with a status field
// 2. Creating an item with field values
// 3. Querying project items and verifying fieldValues are returned
// 4. Querying projectItems and verifying status is derived
func TestProjectV2ItemFieldValuePersistence(t *testing.T) {
	svc, mux, u, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ctx := context.Background()

	// Create a repository and issue
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: u.Login,
		Name:       "fieldvalue-test-repo",
		AutoInit:   true,
	})
	require.NoError(t, err)

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: repo.FullName,
		Title:        "Test Issue for FieldValue",
		Body:         "test body",
		AuthorLogin:  u.Login,
	})
	require.NoError(t, err)

	// Create a project with a status field
	proj, err := svc.CreateProject(ctx, u.Login, "Test Board FieldValue")
	require.NoError(t, err)

	statusField := &db.ProjectField{
		ProjectID: proj.ID,
		Name:      "Status",
		DataType:  "SINGLE_SELECT",
		Options:   `["Todo", "In Progress", "Done"]`,
	}
	require.NoError(t, svc.DB.Create(statusField).Error)

	// Create project item directly with status field value
	issueNodeID := fmt.Sprintf("Issue_%d", issue.ID)
	item := &db.ProjectItem{
		ProjectID:   proj.ID,
		Type:        "ISSUE",
		ContentID:   issueNodeID,
		FieldValues: fmt.Sprintf(`{"%d": "option-in-progress"}`, statusField.ID),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	require.NoError(t, svc.DB.Create(item).Error)

	// Query projectV2 items and verify fieldValues.nodes contains the value
	query := `
	query($login: String!, $number: Int!) {
		user(login: $login) {
			projectV2(number: $number) {
				id
				items(first: 10) {
					nodes {
						id
						fieldValues(first: 10) {
							nodes {
								field { id }
								value
							}
						}
					}
				}
			}
		}
	}`
	queryData := doGql(t, mux, query, map[string]any{
		"login":  u.Login,
		"number": float64(proj.Number),
	})
	userData := queryData["user"].(map[string]any)
	projData := userData["projectV2"].(map[string]any)
	itemsData := projData["items"].(map[string]any)
	itemNodes := itemsData["nodes"].([]any)

	require.NotEmpty(t, itemNodes, "should have at least one item")
	itemNode := itemNodes[0].(map[string]any)
	itemFieldValues := itemNode["fieldValues"].(map[string]any)
	itemFvNodes := itemFieldValues["nodes"].([]any)

	// Verify fieldValues are returned in the query
	foundInQuery := false
	for _, node := range itemFvNodes {
		n := node.(map[string]any)
		field := n["field"].(map[string]any)
		value := n["value"]
		expectedFieldID := fmt.Sprintf("ProjectField_%d", statusField.ID)
		if field["id"] == expectedFieldID && value == "option-in-progress" {
			foundInQuery = true
			break
		}
	}
	require.True(t, foundInQuery, "query should return updated status field value in fieldValues.nodes")

	// Query projectItemsGQL and verify status projection
	query2 := `
	query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) {
			issue(number: $number) {
				projectItems(first: 10) {
					nodes {
						id
						status { optionId name }
					}
				}
			}
		}
	}`
	queryData2 := doGql(t, mux, query2, map[string]any{
		"owner":  u.Login,
		"name":   repo.Name,
		"number": float64(issue.Number),
	})
	repoData := queryData2["repository"].(map[string]any)
	issueResult := repoData["issue"].(map[string]any)
	projectItems := issueResult["projectItems"].(map[string]any)
	statusNodes := projectItems["nodes"].([]any)

	require.NotEmpty(t, statusNodes, "should have at least one project item")
	statusNode := statusNodes[0].(map[string]any)
	status := statusNode["status"]
	require.NotNil(t, status, "status should not be nil")
	statusMap := status.(map[string]any)
	require.Equal(t, "option-in-progress", statusMap["optionId"], "status optionId should match the updated value")
}
