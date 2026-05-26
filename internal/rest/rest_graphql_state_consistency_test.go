package rest_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

const (
	gqlCreateIssueMutation = `
mutation($input: CreateIssueInput!) {
	createIssue(input: $input) {
		issue { id number state stateReason closedAt }
	}
}`

	gqlCloseIssueMutation = `
mutation($input: CloseIssueInput!) {
	closeIssue(input: $input) {
		issue { id state stateReason closedAt }
	}
}`

	gqlReopenIssueMutation = `
mutation($input: ReopenIssueInput!) {
	reopenIssue(input: $input) {
		issue { id state stateReason closedAt }
	}
}`

	gqlCreatePRMutation = `
mutation($input: CreatePullRequestInput!) {
	createPullRequest(input: $input) {
		pullRequest { id number state merged isDraft closedAt }
	}
}`

	gqlClosePRMutation = `
mutation($input: ClosePullRequestInput!) {
	closePullRequest(input: $input) {
		pullRequest { id state merged closedAt }
	}
}`

	gqlReopenPRMutation = `
mutation($input: ReopenPullRequestInput!) {
	reopenPullRequest(input: $input) {
		pullRequest { id state merged closedAt }
	}
}`

	gqlMergePRMutation = `
mutation($input: MergePullRequestInput!) {
	mergePullRequest(input: $input) {
		pullRequest { id merged state }
	}
}`

	gqlIssueStateQuery = `
query($owner: String!, $name: String!, $number: Int!) {
	repository(owner: $owner, name: $name) {
		issue(number: $number) { id state stateReason closedAt }
	}
}`

	gqlPRStateQuery = `
query($owner: String!, $name: String!, $number: Int!) {
	repository(owner: $owner, name: $name) {
		pullRequest(number: $number) {
			id
			state
			merged
			mergedAt
			closedAt
			mergeCommit { oid }
			mergedBy { login }
		}
	}
}`
)

func TestGraphQLWriteRESTRead_StateParity(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	t.Run("IssueState", func(t *testing.T) {
		repo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: h.User.Login,
			Name:       "gql-rest-issue-repo",
			AutoInit:   true,
		})
		require.NoError(t, err)
		repoNodeID := fmt.Sprintf("Repository_%d", repo.ID)

		createData := h.DoGraphQL(t, gqlCreateIssueMutation, map[string]any{
			"input": map[string]any{
				"repositoryId": repoNodeID,
				"title":        "GQL Issue",
				"body":         "body",
			},
		})
		createIssue := createData["createIssue"].(map[string]any)["issue"].(map[string]any)
		issueID := createIssue["id"].(string)
		issueNumber := int(createIssue["number"].(float64))

		require.Equal(t, "OPEN", createIssue["state"])
		require.Nil(t, createIssue["stateReason"])
		requireGraphQLTimeField(t, createIssue, "closedAt", false)

		restIssue := fetchRESTIssue(t, h, repo.FullName, issueNumber)
		require.Equal(t, "open", restIssue["state"])
		require.Nil(t, restIssue["state_reason"])
		requireRESTTimeField(t, restIssue, "closed_at", false)

		closeData := h.DoGraphQL(t, gqlCloseIssueMutation, map[string]any{
			"input": map[string]any{
				"issueId":     issueID,
				"stateReason": db.StateReasonNotPlanned,
			},
		})
		closedIssue := closeData["closeIssue"].(map[string]any)["issue"].(map[string]any)
		require.Equal(t, "CLOSED", closedIssue["state"])
		require.Equal(t, db.StateReasonNotPlanned, closedIssue["stateReason"])
		requireGraphQLTimeField(t, closedIssue, "closedAt", true)

		restClosed := fetchRESTIssue(t, h, repo.FullName, issueNumber)
		require.Equal(t, "closed", restClosed["state"])
		require.Equal(t, db.StateReasonNotPlanned, restClosed["state_reason"])
		requireRESTTimeField(t, restClosed, "closed_at", true)

		reopenData := h.DoGraphQL(t, gqlReopenIssueMutation, map[string]any{
			"input": map[string]any{
				"issueId": issueID,
			},
		})
		reopenedIssue := reopenData["reopenIssue"].(map[string]any)["issue"].(map[string]any)
		require.Equal(t, "OPEN", reopenedIssue["state"])
		require.Equal(t, db.StateReasonReopened, reopenedIssue["stateReason"])
		requireGraphQLTimeField(t, reopenedIssue, "closedAt", false)

		restReopened := fetchRESTIssue(t, h, repo.FullName, issueNumber)
		require.Equal(t, "open", restReopened["state"])
		require.Equal(t, db.StateReasonReopened, restReopened["state_reason"])
		requireRESTTimeField(t, restReopened, "closed_at", false)
	})

	t.Run("PRStateAndMerge", func(t *testing.T) {
		repo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: h.User.Login,
			Name:       "gql-rest-pr-repo",
			AutoInit:   true,
		})
		require.NoError(t, err)
		fullName := repo.FullName
		require.NoError(t, h.Svc.Git.CreateBranch(ctx, fullName, "feature", "main"))
		_, err = h.Svc.Git.WriteFile(ctx, fullName, "feature", "feature.txt", "add feature", []byte("feature content\n"))
		require.NoError(t, err)

		repoNodeID := fmt.Sprintf("Repository_%d", repo.ID)
		createData := h.DoGraphQL(t, gqlCreatePRMutation, map[string]any{
			"input": map[string]any{
				"repositoryId": repoNodeID,
				"title":        "GQL PR",
				"body":         "body",
				"headRefName":  "feature",
				"baseRefName":  "main",
			},
		})
		createPR := createData["createPullRequest"].(map[string]any)["pullRequest"].(map[string]any)
		prID := createPR["id"].(string)
		prNumber := int(createPR["number"].(float64))

		require.Equal(t, "OPEN", createPR["state"])
		require.Equal(t, false, createPR["merged"])
		requireGraphQLTimeField(t, createPR, "closedAt", false)

		restPR := fetchRESTPR(t, h, repo.FullName, prNumber)
		require.Equal(t, "open", restPR["state"])
		require.Equal(t, false, restPR["merged"])
		requireRESTTimeField(t, restPR, "closed_at", false)
		requireRESTTimeField(t, restPR, "merged_at", false)

		closeData := h.DoGraphQL(t, gqlClosePRMutation, map[string]any{
			"input": map[string]any{
				"pullRequestId": prID,
			},
		})
		closedPR := closeData["closePullRequest"].(map[string]any)["pullRequest"].(map[string]any)
		require.Equal(t, "CLOSED", closedPR["state"])
		require.Equal(t, false, closedPR["merged"])
		requireGraphQLTimeField(t, closedPR, "closedAt", true)

		restClosed := fetchRESTPR(t, h, repo.FullName, prNumber)
		require.Equal(t, "closed", restClosed["state"])
		require.Equal(t, false, restClosed["merged"])
		requireRESTTimeField(t, restClosed, "closed_at", true)
		requireRESTTimeField(t, restClosed, "merged_at", false)

		reopenData := h.DoGraphQL(t, gqlReopenPRMutation, map[string]any{
			"input": map[string]any{
				"pullRequestId": prID,
			},
		})
		reopenedPR := reopenData["reopenPullRequest"].(map[string]any)["pullRequest"].(map[string]any)
		require.Equal(t, "OPEN", reopenedPR["state"])
		require.Equal(t, false, reopenedPR["merged"])
		requireGraphQLTimeField(t, reopenedPR, "closedAt", false)

		restReopened := fetchRESTPR(t, h, repo.FullName, prNumber)
		require.Equal(t, "open", restReopened["state"])
		require.Equal(t, false, restReopened["merged"])
		requireRESTTimeField(t, restReopened, "closed_at", false)

		mergeHeadline := fmt.Sprintf("merge-commit: PR #%d via MERGE", prNumber)
		mergeData := h.DoGraphQL(t, gqlMergePRMutation, map[string]any{
			"input": map[string]any{
				"pullRequestId":  prID,
				"mergeMethod":    "MERGE",
				"commitHeadline": mergeHeadline,
			},
		})
		mergedPR := mergeData["mergePullRequest"].(map[string]any)["pullRequest"].(map[string]any)
		require.Equal(t, "MERGED", mergedPR["state"])
		require.Equal(t, true, mergedPR["merged"])

		prQuery := h.DoGraphQL(t, gqlPRStateQuery, map[string]any{
			"owner":  h.User.Login,
			"name":   repo.Name,
			"number": prNumber,
		})
		prNode := prQuery["repository"].(map[string]any)["pullRequest"].(map[string]any)
		require.Equal(t, "MERGED", prNode["state"])
		require.Equal(t, true, prNode["merged"])
		requireGraphQLTimeField(t, prNode, "mergedAt", true)
		requireGraphQLTimeField(t, prNode, "closedAt", true)

		mergeCommit, ok := prNode["mergeCommit"].(map[string]any)
		require.True(t, ok, "mergeCommit should be present after merge")
		mergeOID, ok := mergeCommit["oid"].(string)
		require.True(t, ok, "mergeCommit.oid should be a string")
		require.NotEmpty(t, mergeOID)

		mergedBy, ok := prNode["mergedBy"].(map[string]any)
		require.True(t, ok, "mergedBy should be present after merge")
		require.Equal(t, h.User.Login, mergedBy["login"])

		restMerged := fetchRESTPR(t, h, repo.FullName, prNumber)
		require.Equal(t, "closed", restMerged["state"])
		require.Equal(t, true, restMerged["merged"])
		requireRESTTimeField(t, restMerged, "closed_at", true)
		requireRESTTimeField(t, restMerged, "merged_at", true)
		require.Equal(t, mergeOID, restMerged["merge_commit_sha"])

		restMergedBy, ok := restMerged["merged_by"].(map[string]any)
		require.True(t, ok, "merged_by should be present after merge")
		require.Equal(t, h.User.Login, restMergedBy["login"])
	})
}

func TestRESTWriteGraphQLRead_StateParity(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()

	t.Run("IssueState", func(t *testing.T) {
		repo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: h.User.Login,
			Name:       "rest-gql-issue-repo",
			AutoInit:   true,
		})
		require.NoError(t, err)

		createResp := h.DoRESTJSON(t, "POST", fmt.Sprintf("/api/v3/repos/%s/issues", repo.FullName), map[string]any{
			"title": "REST Issue",
			"body":  "body",
		})
		require.Equal(t, 201, createResp.Code)
		createdIssue := testharness.DecodeJSON(t, createResp)
		issueNumber := int(createdIssue["number"].(float64))
		require.Equal(t, "open", createdIssue["state"])

		issueNode := fetchGraphQLIssue(t, h, repo.FullName, issueNumber)
		require.Equal(t, "OPEN", issueNode["state"])
		require.Nil(t, issueNode["stateReason"])
		requireGraphQLTimeField(t, issueNode, "closedAt", false)

		closeResp := h.DoRESTJSON(t, "PATCH", fmt.Sprintf("/api/v3/repos/%s/issues/%d", repo.FullName, issueNumber), map[string]any{
			"state": "closed",
		})
		require.Equal(t, 200, closeResp.Code)

		closedNode := fetchGraphQLIssue(t, h, repo.FullName, issueNumber)
		require.Equal(t, "CLOSED", closedNode["state"])
		require.Equal(t, db.StateReasonCompleted, closedNode["stateReason"])
		requireGraphQLTimeField(t, closedNode, "closedAt", true)

		reopenResp := h.DoRESTJSON(t, "PATCH", fmt.Sprintf("/api/v3/repos/%s/issues/%d", repo.FullName, issueNumber), map[string]any{
			"state": "open",
		})
		require.Equal(t, 200, reopenResp.Code)

		reopenedNode := fetchGraphQLIssue(t, h, repo.FullName, issueNumber)
		require.Equal(t, "OPEN", reopenedNode["state"])
		require.Equal(t, db.StateReasonReopened, reopenedNode["stateReason"])
		requireGraphQLTimeField(t, reopenedNode, "closedAt", false)
	})

	t.Run("PRState", func(t *testing.T) {
		repo, err := h.Svc.CreateRepo(ctx, service.CreateRepoInput{
			OwnerLogin: h.User.Login,
			Name:       "rest-gql-pr-repo",
			AutoInit:   true,
		})
		require.NoError(t, err)
		fullName := repo.FullName
		require.NoError(t, h.Svc.Git.CreateBranch(ctx, fullName, "feature", "main"))
		_, err = h.Svc.Git.WriteFile(ctx, fullName, "feature", "feature.txt", "add feature", []byte("feature content\n"))
		require.NoError(t, err)

		createResp := h.DoRESTJSON(t, "POST", fmt.Sprintf("/api/v3/repos/%s/pulls", repo.FullName), map[string]any{
			"title": "REST PR",
			"body":  "body",
			"head":  "feature",
			"base":  "main",
		})
		require.Equal(t, 201, createResp.Code)
		createdPR := testharness.DecodeJSON(t, createResp)
		prNumber := int(createdPR["number"].(float64))
		require.Equal(t, "open", createdPR["state"])

		prNode := fetchGraphQLPR(t, h, repo.FullName, prNumber)
		require.Equal(t, "OPEN", prNode["state"])
		require.Equal(t, false, prNode["merged"])
		requireGraphQLTimeField(t, prNode, "closedAt", false)
		requireGraphQLTimeField(t, prNode, "mergedAt", false)

		closeResp := h.DoRESTJSON(t, "PATCH", fmt.Sprintf("/api/v3/repos/%s/pulls/%d", repo.FullName, prNumber), map[string]any{
			"state": "closed",
		})
		require.Equal(t, 200, closeResp.Code)

		closedPR := fetchGraphQLPR(t, h, repo.FullName, prNumber)
		require.Equal(t, "CLOSED", closedPR["state"])
		require.Equal(t, false, closedPR["merged"])
		requireGraphQLTimeField(t, closedPR, "closedAt", true)
		requireGraphQLTimeField(t, closedPR, "mergedAt", false)

		reopenResp := h.DoRESTJSON(t, "PATCH", fmt.Sprintf("/api/v3/repos/%s/pulls/%d", repo.FullName, prNumber), map[string]any{
			"state": "open",
		})
		require.Equal(t, 200, reopenResp.Code)

		reopenedPR := fetchGraphQLPR(t, h, repo.FullName, prNumber)
		require.Equal(t, "OPEN", reopenedPR["state"])
		require.Equal(t, false, reopenedPR["merged"])
		requireGraphQLTimeField(t, reopenedPR, "closedAt", false)
	})
}

func fetchRESTIssue(t *testing.T, h *testharness.Harness, repoFullName string, number int) map[string]any {
	t.Helper()
	w := h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/%s/issues/%d", repoFullName, number), nil)
	require.Equal(t, 200, w.Code)
	return testharness.DecodeJSON(t, w)
}

func fetchRESTPR(t *testing.T, h *testharness.Harness, repoFullName string, number int) map[string]any {
	t.Helper()
	w := h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/%s/pulls/%d", repoFullName, number), nil)
	require.Equal(t, 200, w.Code)
	return testharness.DecodeJSON(t, w)
}

func fetchGraphQLIssue(t *testing.T, h *testharness.Harness, repoFullName string, number int) map[string]any {
	t.Helper()
	owner, name := splitFullName(repoFullName)
	data := h.DoGraphQL(t, gqlIssueStateQuery, map[string]any{
		"owner":  owner,
		"name":   name,
		"number": number,
	})
	repo := data["repository"].(map[string]any)
	issue := repo["issue"].(map[string]any)
	return issue
}

func fetchGraphQLPR(t *testing.T, h *testharness.Harness, repoFullName string, number int) map[string]any {
	t.Helper()
	owner, name := splitFullName(repoFullName)
	data := h.DoGraphQL(t, gqlPRStateQuery, map[string]any{
		"owner":  owner,
		"name":   name,
		"number": number,
	})
	repo := data["repository"].(map[string]any)
	pr := repo["pullRequest"].(map[string]any)
	return pr
}

func splitFullName(fullName string) (string, string) {
	for i := range fullName {
		if fullName[i] == '/' {
			return fullName[:i], fullName[i+1:]
		}
	}
	return fullName, ""
}

func requireRESTTimeField(t *testing.T, body map[string]any, key string, wantSet bool) {
	t.Helper()
	v, ok := body[key]
	require.True(t, ok, "missing %s", key)
	if wantSet {
		str, ok := v.(string)
		require.True(t, ok, "expected %s string", key)
		require.NotEmpty(t, str, "expected %s to be set", key)
		return
	}
	require.Nil(t, v, "expected %s to be nil", key)
}

func requireGraphQLTimeField(t *testing.T, body map[string]any, key string, wantSet bool) {
	t.Helper()
	v, ok := body[key]
	require.True(t, ok, "missing %s", key)
	str, ok := v.(string)
	require.True(t, ok, "expected %s string", key)
	if wantSet {
		require.NotEmpty(t, str, "expected %s to be set", key)
		return
	}
	require.Empty(t, str, "expected %s to be empty", key)
}
