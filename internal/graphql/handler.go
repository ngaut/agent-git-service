// Package graphql provides a lightweight GraphQL endpoint.
// It uses operation names and key field detection rather than a full AST
// engine, while still returning properly typed, DB-backed responses.
//
// IMPORTANT: The gh CLI uses shurcooL-graphql which does STRICT unmarshaling.
// Extra fields in the response cause errors. All responses are filtered through
// filterResponse() to only include fields requested in the query.
package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ngaut/agent-git-service/internal/db"
	applog "github.com/ngaut/agent-git-service/internal/logging"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/service"
)

// Server encapsulates all mutable state for the GraphQL handler layer.
// It replaces the former package-level globals so that dependencies are
// explicit and the package is safe for concurrent tests.
type Server struct {
	Svc *service.Service
}

// NewServer creates a Server wired to the given service.
func NewServer(svc *service.Service) *Server {
	return &Server{Svc: svc}
}

type gqlRequest struct {
	Query         string         `json:"query"`
	OperationName string         `json:"operationName"`
	Variables     map[string]any `json:"variables"`
}

// mutationMatcher defines a strategy for matching and handling GraphQL mutations.
// This implements the strategy pattern to unify complex and simple dispatch logic.
type mutationMatcher struct {
	match   func(ast map[string]any, op string, query string) bool
	handler func(context.Context, gqlRequest, map[string]any) map[string]any
}

// Handler dispatches GitHub GraphQL API requests backed by the service layer.
func (s *Server) Handler(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.ErrorContext(r.Context(), "graphql panic", "panic", fmt.Sprintf("%v", rec))
			respond.JSON(w, 500, map[string]any{"data": nil, "errors": []map[string]any{{"message": fmt.Sprintf("%v", rec)}}})
		}
	}()
	var req gqlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.WarnContext(r.Context(), "graphql decode failed", "error", err)
		respond.Error(w, 400, "invalid JSON")
		return
	}

	op := strings.ToLower(req.OperationName)
	if op != "" {
		applog.AddAttrs(r.Context(), slog.String("graphql_operation", op))
	}

	// Parse the query into a field AST, fragments, and __type introspections in one pass.
	ast, fragments, typeIntro := parseQuery(req.Query)
	isMutation := strings.Contains(strings.ToLower(req.Query[:min(len(req.Query), 30)]), "mutation")

	var result map[string]any

	if isMutation {
		result = s.routeMutation(r.Context(), req, op, ast)
	} else {
		result = s.routeQuery(r.Context(), req, op, ast, typeIntro)
	}

	// Filter response to only include requested fields
	if d, ok := result["data"].(map[string]any); ok {
		result["data"] = filterMap(d, ast, fragments)
	}

	respond.JSON(w, 200, result)
}

// astHas returns true if the parsed AST contains the given top-level field.

func astHas(ast map[string]any, field string) bool {
	_, ok := ast[field]
	return ok
}

// astChild returns the child map for a given key, or nil.

func astChild(ast map[string]any, field string) map[string]any {
	if v, ok := ast[field].(map[string]any); ok {
		return v
	}
	return nil
}

// repoMatcherGroups keeps repository mutations in ordered slices so getMutationMatchers can preserve priority.
type repoMatcherGroups struct {
	archive  []mutationMatcher
	core     []mutationMatcher
	security []mutationMatcher
}

// issueMatcherGroups keeps issue-centric mutations in ordered slices to preserve matching priority.
type issueMatcherGroups struct {
	create       []mutationMatcher
	state        []mutationMatcher
	comments     []mutationMatcher
	update       []mutationMatcher
	labels       []mutationMatcher
	deletion     []mutationMatcher
	assignees    []mutationMatcher
	linkedBranch []mutationMatcher
	pin          []mutationMatcher
	transfer     []mutationMatcher
}

// prMatcherGroups keeps pull request mutations in ordered slices to preserve matching priority.
type prMatcherGroups struct {
	create         []mutationMatcher
	requestReviews []mutationMatcher
	state          []mutationMatcher
	draft          []mutationMatcher
	update         []mutationMatcher
	reviews        []mutationMatcher
	autoMerge      []mutationMatcher
	branch         []mutationMatcher
	reviewThread   []mutationMatcher
}

// projectMatcherGroups keeps project mutations in ordered slices to preserve matching priority.
type projectMatcherGroups struct {
	itemBatch []mutationMatcher
	core      []mutationMatcher
}

// milestoneMatcherGroups keeps milestone mutations in ordered slices to preserve matching priority.
type milestoneMatcherGroups struct {
	core []mutationMatcher
}

// buildRepoMatchers builds repository-related matcher groups in original priority order.
func (s *Server) buildRepoMatchers() repoMatcherGroups {
	return repoMatcherGroups{
		archive: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "archiveRepository") && !astHas(ast, "unarchiveRepository")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doArchiveRepo(ctx, req, true)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "unarchiveRepository")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doArchiveRepo(ctx, req, false)
				},
			},
		},
		core: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "createBlob") || has(op, "createblob")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doCreateBlob(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "createTree") || has(op, "createtree")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doCreateTree(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "cloneTemplateRepository")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doCloneTemplateRepository(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "updateRepository")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doUpdateRepository(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "createRepository")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doCreateRepository(ctx, req)
				},
			},
		},
		security: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "dismissRepositoryVulnerabilityAlert")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doDismissRepositoryVulnerabilityAlert(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "resolveRepositoryVulnerabilityAlert")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doResolveRepositoryVulnerabilityAlert(ctx, req)
				},
			},
		},
	}
}

// buildIssueMatchers builds issue-related matcher groups in original priority order.
func (s *Server) buildIssueMatchers() issueMatcherGroups {
	return issueMatcherGroups{
		create: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "createIssue") || has(op, "createissue")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doCreateIssue(ctx, req)
				},
			},
		},
		state: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "closeIssue")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doSetIssueState(ctx, req, db.StateClosed, "closeIssue")
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "reopenIssue")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doSetIssueState(ctx, req, db.StateOpen, "reopenIssue")
				},
			},
		},
		comments: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "addComment")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doAddComment(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "updateIssueComment")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doUpdateComment(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "deleteIssueComment")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doDeleteIssueComment(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "pinIssueComment")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doPinIssueComment(ctx, req, true)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "unpinIssueComment")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doPinIssueComment(ctx, req, false)
				},
			},
		},
		update: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "updateIssue")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doUpdateIssue(ctx, req)
				},
			},
		},
		labels: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "addLabelsToLabelable")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doAddLabels(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "removeLabelsFromLabelable")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doRemoveLabels(ctx, req)
				},
			},
		},
		deletion: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "deleteIssue")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doDeleteIssue(ctx, req)
				},
			},
		},
		assignees: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "replaceActorsForAssignable")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doReplaceAssignees(ctx, req)
				},
			},
		},
		linkedBranch: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "createLinkedBranch")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					result := s.doCreateLinkedBranch(ctx, req)
					// If result contains errors, return it directly without wrapping
					if _, hasErrors := result["errors"]; hasErrors {
						return result
					}
					return wrap("createLinkedBranch", result)
				},
			},
		},
		pin: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "pinIssue")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doPinIssue(ctx, req, true)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "unpinIssue")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doPinIssue(ctx, req, false)
				},
			},
		},
		transfer: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "transferIssue")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doTransferIssue(ctx, req)
				},
			},
		},
	}
}

// buildPRMatchers builds pull request matcher groups in original priority order.
func (s *Server) buildPRMatchers() prMatcherGroups {
	return prMatcherGroups{
		create: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "createPullRequest") || has(op, "createpullrequest")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doCreatePR(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "mergePullRequest") || has(op, "mergepullrequest")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doMergePR(ctx, req)
				},
			},
		},
		requestReviews: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "requestReviews") || astHas(ast, "requestReviewsByLogin")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doRequestReviews(ctx, req)
				},
			},
		},
		state: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "closePullRequest")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doSetPRState(ctx, req, db.StateClosed, "closePullRequest")
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "reopenPullRequest")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doSetPRState(ctx, req, db.StateOpen, "reopenPullRequest")
				},
			},
		},
		draft: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "markPullRequestReadyForReview")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doSetPRDraft(ctx, req, false, "markPullRequestReadyForReview")
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "convertPullRequestToDraft")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doSetPRDraft(ctx, req, true, "convertPullRequestToDraft")
				},
			},
		},
		update: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "updatePullRequest")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doUpdatePR(ctx, req)
				},
			},
		},
		reviews: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "addPullRequestReview")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doAddPRReview(ctx, req)
				},
			},
		},
		autoMerge: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "enablePullRequestAutoMerge")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doSetAutoMerge(ctx, req, true)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "disablePullRequestAutoMerge")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doSetAutoMerge(ctx, req, false)
				},
			},
		},
		branch: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "updatePullRequestBranch")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doUpdatePRBranch(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "revertPullRequest")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doRevertPR(ctx, req)
				},
			},
		},
		reviewThread: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "resolveReviewThread")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doResolveReviewThread(ctx, req, true)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "unresolveReviewThread")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doResolveReviewThread(ctx, req, false)
				},
			},
		},
	}
}

// buildProjectMatchers builds project matcher groups in original priority order.
func (s *Server) buildProjectMatchers() projectMatcherGroups {
	return projectMatcherGroups{
		itemBatch: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "addProjectV2ItemById") || astHas(ast, "deleteProjectV2Item") ||
						strings.Contains(query, "addProjectV2ItemById") || strings.Contains(query, "deleteProjectV2Item")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doProjectItemBatch(ctx, req, ast)
				},
			},
		},
		core: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "createProjectV2")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doCreateProject(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "deleteProjectV2")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doDeleteProject(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "updateProjectV2")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doUpdateProject(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "closeProjectV2")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doCloseProject(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "copyProjectV2")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doCopyProject(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "createProjectV2Field")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doCreateProjectField(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "deleteProjectV2Field")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doDeleteProjectField(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "updateProjectV2ItemFieldValue")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doUpdateProjectItemFieldValue(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "clearProjectV2ItemFieldValue")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doClearProjectItemFieldValue(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "updateProjectV2DraftIssue")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doUpdateProjectDraftIssue(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "addProjectV2DraftIssue")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doAddProjectDraftIssue(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "archiveProjectV2Item")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doArchiveProjectItem(ctx, req, true)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "unarchiveProjectV2Item")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doArchiveProjectItem(ctx, req, false)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "linkProjectV2ToRepository")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doLinkProjectToRepo(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "unlinkProjectV2FromRepository")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doUnlinkProjectFromRepo(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "linkProjectV2ToTeam")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doLinkProjectToTeam(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "unlinkProjectV2FromTeam")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doUnlinkProjectFromTeam(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "markProjectV2AsTemplate")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doMarkProjectAsTemplate(ctx, req, true)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "unmarkProjectV2AsTemplate")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doMarkProjectAsTemplate(ctx, req, false)
				},
			},
		},
	}
}

// buildMilestoneMatchers builds milestone matcher groups in original priority order.
func (s *Server) buildMilestoneMatchers() milestoneMatcherGroups {
	return milestoneMatcherGroups{
		core: []mutationMatcher{
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "createMilestone")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doCreateMilestone(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "updateMilestone")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doUpdateMilestone(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "deleteMilestone")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doDeleteMilestone(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "closeMilestone")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doCloseMilestone(ctx, req)
				},
			},
			{
				match: func(ast map[string]any, op string, query string) bool {
					return astHas(ast, "reopenMilestone")
				},
				handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
					return s.doReopenMilestone(ctx, req)
				},
			},
		},
	}
}

// buildReactionMatchers builds reaction matchers in original priority order.
func (s *Server) buildReactionMatchers() []mutationMatcher {
	return []mutationMatcher{
		{
			match: func(ast map[string]any, op string, query string) bool {
				return astHas(ast, "addReaction")
			},
			handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
				inp := inputMap(req)
				content := strFrom(inp, "content")
				if content == "" {
					content = "THUMBS_UP"
				}
				login := "ghost"
				if u, err := s.Svc.GetCurrentUser(ctx); err == nil {
					login = u.Login
				}
				return wrap("addReaction", map[string]any{
					"reaction": map[string]any{"content": content, "user": map[string]any{"login": login}},
					"subject":  map[string]any{"reactionGroups": defaultReactionGroups()},
				})
			},
		},
		{
			match: func(ast map[string]any, op string, query string) bool {
				return astHas(ast, "removeReaction")
			},
			handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
				login := "ghost"
				if u, err := s.Svc.GetCurrentUser(ctx); err == nil {
					login = u.Login
				}
				return wrap("removeReaction", map[string]any{
					"reaction": map[string]any{"content": "THUMBS_UP", "user": map[string]any{"login": login}},
					"subject":  map[string]any{"reactionGroups": defaultReactionGroups()},
				})
			},
		},
	}
}

// buildLockMatchers builds lockable matchers in original priority order.
func (s *Server) buildLockMatchers() []mutationMatcher {
	return []mutationMatcher{
		{
			match: func(ast map[string]any, op string, query string) bool {
				return astHas(ast, "lockLockable")
			},
			handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
				return s.doLockLockable(ctx, req, true)
			},
		},
		{
			match: func(ast map[string]any, op string, query string) bool {
				return astHas(ast, "unlockLockable")
			},
			handler: func(ctx context.Context, req gqlRequest, ast map[string]any) map[string]any {
				return s.doLockLockable(ctx, req, false)
			},
		},
	}
}

// getMutationMatchers returns all mutation matchers in priority order.
// This unifies the previously split switch+map dispatch into a single strategy pattern.
func (s *Server) getMutationMatchers() []mutationMatcher {
	repo := s.buildRepoMatchers()
	issue := s.buildIssueMatchers()
	pr := s.buildPRMatchers()
	project := s.buildProjectMatchers()
	milestone := s.buildMilestoneMatchers()
	reactions := s.buildReactionMatchers()
	locks := s.buildLockMatchers()

	matchers := []mutationMatcher{}
	// Compound conditions (higher priority - checked first)
	matchers = append(matchers, repo.archive...)
	matchers = append(matchers, issue.create...)
	matchers = append(matchers, pr.create...)
	matchers = append(matchers, project.itemBatch...)
	matchers = append(matchers, pr.requestReviews...)
	// Simple mutations: single AST field → handler
	matchers = append(matchers, repo.core...)
	matchers = append(matchers, pr.state...)
	matchers = append(matchers, issue.state...)
	matchers = append(matchers, pr.draft...)
	matchers = append(matchers, issue.comments...)
	matchers = append(matchers, issue.update...)
	matchers = append(matchers, pr.update...)
	matchers = append(matchers, issue.labels...)
	matchers = append(matchers, issue.deletion...)
	matchers = append(matchers, issue.assignees...)
	matchers = append(matchers, issue.linkedBranch...)
	matchers = append(matchers, pr.reviews...)
	matchers = append(matchers, locks...)
	matchers = append(matchers, pr.autoMerge...)
	matchers = append(matchers, issue.pin...)
	matchers = append(matchers, issue.transfer...)
	matchers = append(matchers, pr.branch...)
	matchers = append(matchers, milestone.core...)
	matchers = append(matchers, project.core...)
	matchers = append(matchers, repo.security...)
	matchers = append(matchers, reactions...)
	matchers = append(matchers, pr.reviewThread...)
	return matchers
}

func (s *Server) routeMutation(ctx context.Context, req gqlRequest, op string, ast map[string]any) map[string]any {
	matchers := s.getMutationMatchers()
	for _, m := range matchers {
		if m.match(ast, op, req.Query) {
			return m.handler(ctx, req, ast)
		}
	}
	return map[string]any{"data": map[string]any{}}
}
