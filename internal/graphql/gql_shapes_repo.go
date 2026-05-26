package graphql

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func stringOrNil(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func (s *Server) userGQL(ctx context.Context, u db.User) map[string]any {
	var email any
	if viewer, ok := service.UserFromContext(ctx); ok && viewer.ID == u.ID {
		email = u.Email
	} else {
		email = nil
	}
	return map[string]any{
		"id":         gqlID("User", u.ID),
		"login":      u.Login,
		"name":       u.Name,
		"email":      email,
		"avatarUrl":  fmt.Sprintf("%s/avatars/%s", s.Svc.BaseURL, u.Login),
		"url":        fmt.Sprintf("%s/%s", s.Svc.HTMLBaseURL(), u.Login),
		"createdAt":  u.CreatedAt.Format(time.RFC3339),
		"updatedAt":  u.UpdatedAt.Format(time.RFC3339),
		"__typename": "User",
	}
}

func (s *Server) repoGQL(ctx context.Context, rep db.Repository, cachedUserNodes ...[]any) map[string]any {
	defBranch := map[string]any{"name": rep.DefaultBranch, "id": gqlID("Ref", rep.ID)}

	var userNodes []any
	if len(cachedUserNodes) > 0 && cachedUserNodes[0] != nil {
		userNodes = cachedUserNodes[0]
	} else {
		userNodes = s.allUserNodes(ctx)
	}

	starCount := s.Svc.StarCount(ctx, rep.ID)

	return map[string]any{
		"id":                 gqlID("Repository", rep.ID),
		"databaseId":         rep.ID,
		"name":               rep.Name,
		"nameWithOwner":      rep.FullName,
		"description":        rep.Description,
		"isPrivate":          rep.Private,
		"visibility":         repoVisibility(rep),
		"isFork":             rep.Fork,
		"isArchived":         rep.Archived,
		"isDisabled":         rep.Disabled,
		"isEmpty":            s.Svc.IsRepoEmpty(ctx, rep.FullName),
		"isInOrganization":   rep.Owner.Type == db.TypeOrganization,
		"isTemplate":         rep.IsTemplate,
		"isMirror":           rep.IsMirror,
		"fullDatabaseId":     fmt.Sprintf("%d", rep.ID),
		"hasIssuesEnabled":   rep.HasIssues,
		"hasWikiEnabled":     rep.HasWiki,
		"hasProjectsEnabled": rep.HasProjects,
		"defaultBranchRef":   defBranch,
		"owner":              map[string]any{"id": gqlID("User", rep.Owner.ID), "login": rep.Owner.Login},
		"url":                fmt.Sprintf("%s/%s", s.Svc.HTMLBaseURL(), rep.FullName),
		"sshUrl":             fmt.Sprintf("git@%s:%s.git", s.sshHost(), rep.FullName),
		"homepageUrl":        stringOrNil(rep.Homepage),
		"mirrorUrl":          nil,
		"assignableUsers":    gqlConn(userNodes),
		"suggestedActors":    gqlConn(userNodes),
		"labels":             s.labelsToGQL(ctx, rep.Labels),
		"milestones":         s.repoMilestonesConn(ctx, rep.ID),
		"projectsV2":         s.repoProjectsConn(ctx, rep),
		"issues":             s.repoIssueCountConn(ctx, rep.ID),
		"pullRequests":       s.repoPRCountConn(ctx, rep.ID),
		"repositoryTopics":   repoTopicsConn(rep.Topics),
		"codeOfConduct":      nil,
		"contactLinks":       []any{},
		"licenseInfo":        nil,
		"fundingLinks":       []any{},
		"latestRelease":      s.latestReleaseGQL(ctx, rep),
		"primaryLanguage":    primaryLanguageGQL(rep.Language),
		"languages":          languagesConn(rep.Language),
		// watchers / stargazers / stargazerCount all render the star count;
		// query once and reuse so rendering a single repo issues 1 SELECT
		// against the stars table instead of 3.
		"watchers":            map[string]any{"totalCount": starCount},
		"stargazers":          map[string]any{"totalCount": starCount},
		"stargazerCount":      starCount,
		"forkCount":           s.Svc.ForkCount(ctx, rep.ID),
		"diskUsage":           s.Svc.GitDiskUsageKB(ctx, rep.FullName),
		"createdAt":           rep.CreatedAt.Format(time.RFC3339),
		"updatedAt":           rep.UpdatedAt.Format(time.RFC3339),
		"pushedAt":            s.repoPushedAt(rep),
		"parent":              s.parentGQL(ctx, rep.Parent),
		"mergeCommitAllowed":  rep.AllowMergeCommit,
		"rebaseMergeAllowed":  rep.AllowRebaseMerge,
		"squashMergeAllowed":  rep.AllowSquashMerge,
		"deleteBranchOnMerge": rep.DeleteBranchOnMerge,
		"viewerPermission":    s.viewerPermission(ctx, rep),
		"autoMergeAllowed":    rep.AllowAutoMerge,
		"vulnerabilityAlerts": s.repoDependabotAlertsConn(ctx, rep.ID),
		"__typename":          "Repository",
	}
}

// allUserNodes fetches all users once and returns them as GraphQL nodes.
// Use this to avoid N+1 queries when building multiple repo shapes.
func (s *Server) allUserNodes(ctx context.Context) []any {
	users, _ := s.Svc.ListAllUsers(ctx)
	nodes := make([]any, len(users))
	for i, u := range users {
		nodes[i] = map[string]any{
			"id":         gqlID("User", u.ID),
			"login":      u.Login,
			"name":       u.Name,
			"__typename": "User",
		}
	}
	return nodes
}

func (s *Server) milestoneGQL(m db.Milestone) map[string]any {
	dueOn := ""
	if m.DueOn != nil {
		dueOn = m.DueOn.Format(time.RFC3339)
	}
	state := "OPEN"
	if m.State == db.StateClosed {
		state = "CLOSED"
	}
	return map[string]any{
		"id":          gqlID("Milestone", m.ID),
		"number":      m.Number,
		"title":       m.Title,
		"description": m.Description,
		"state":       state,
		"dueOn":       dueOn,
		"__typename":  "Milestone",
	}
}

// releaseAssetsGQL converts release assets to a GraphQL connection.
func (s *Server) releaseAssetsGQL(ctx context.Context, release db.Release) map[string]any {
	nodes := make([]any, len(release.Assets))
	for i, a := range release.Assets {
		nodes[i] = map[string]any{
			"id":          gqlID("ReleaseAsset", a.ID),
			"name":        a.Name,
			"size":        a.Size,
			"contentType": a.ContentType,
			"url":         fmt.Sprintf("%s/api/v3/repos/%s/releases/assets/%d", s.Svc.BaseURL, release.Repository.FullName, a.ID),
			"downloadUrl": fmt.Sprintf("%s/api/v3/repos/%s/releases/assets/%d/download", s.Svc.BaseURL, release.Repository.FullName, a.ID),
			"createdAt":   a.CreatedAt.Format(time.RFC3339),
			"updatedAt":   a.UpdatedAt.Format(time.RFC3339),
			"__typename":  "ReleaseAsset",
		}
	}
	return gqlConn(nodes)
}

func (s *Server) parentGQL(ctx context.Context, parent *db.Repository) any {
	if parent == nil {
		return nil
	}
	return map[string]any{
		"id":            gqlID("Repository", parent.ID),
		"name":          parent.Name,
		"nameWithOwner": parent.FullName,
		"owner":         map[string]any{"login": parent.Owner.Login},
		"defaultBranchRef": map[string]any{
			"name": parent.DefaultBranch,
		},
	}
}

// repoVisibility returns the GraphQL visibility enum for the repository.
func repoVisibility(rep db.Repository) string {
	switch strings.ToLower(strings.TrimSpace(rep.Visibility)) {
	case "internal":
		return "INTERNAL"
	case "private":
		return "PRIVATE"
	case "public":
		return "PUBLIC"
	default:
		if rep.Private {
			return "PRIVATE"
		}
		return "PUBLIC"
	}
}

// repoMilestonesConn returns the milestones connection for a repo.
func (s *Server) repoMilestonesConn(ctx context.Context, repoID uint) map[string]any {
	milestones, _, err := s.Svc.ListMilestonesByRepoID(ctx, repoID, "all", "", "", 1, 0)
	if err != nil || len(milestones) == 0 {
		return emptyConn()
	}
	nodes := make([]any, len(milestones))
	for i, m := range milestones {
		nodes[i] = s.milestoneGQL(m)
	}
	return gqlConn(nodes)
}

// repoProjectsConn returns the projectsV2 connection for a repository.
func (s *Server) repoProjectsConn(ctx context.Context, repo db.Repository) map[string]any {
	projects, err := s.Svc.ListProjectsForRepo(ctx, repo.ID)
	if err != nil || len(projects) == 0 {
		return emptyConn()
	}
	nodes := make([]any, len(projects))
	for i, p := range projects {
		projectNode := s.projectGQL(ctx, p)
		projectNode["resourcePath"] = fmt.Sprintf("/%s/projects/%d", repo.FullName, p.Number)
		projectNode["url"] = fmt.Sprintf("%s/%s/projects/%d", s.Svc.HTMLBaseURL(), repo.FullName, p.Number)
		nodes[i] = projectNode
	}
	return gqlConn(nodes)
}

// repoIssueCountConn returns an issues connection with just the totalCount populated.
func (s *Server) repoIssueCountConn(ctx context.Context, repoID uint) map[string]any {
	return gqlCountConn(s.Svc.CountIssuesByRepoID(ctx, repoID))
}

// repoPRCountConn returns a pullRequests connection with just the totalCount populated.
func (s *Server) repoPRCountConn(ctx context.Context, repoID uint) map[string]any {
	return gqlCountConn(s.Svc.CountPRsByRepoID(ctx, repoID))
}

// repoTopicsConn builds the repositoryTopics connection from a comma-separated topic string.
func repoTopicsConn(topics string) map[string]any {
	if topics == "" {
		return emptyConn()
	}
	parts := strings.Split(topics, ",")
	nodes := make([]any, 0, len(parts))
	for _, t := range parts {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		nodes = append(nodes, map[string]any{
			"topic": map[string]any{"name": t},
		})
	}
	if len(nodes) == 0 {
		return emptyConn()
	}
	return gqlConn(nodes)
}

// primaryLanguageGQL returns the primaryLanguage field from the stored language string.
func primaryLanguageGQL(lang string) any {
	if lang == "" {
		return nil
	}
	return map[string]any{"name": lang}
}

// languagesConn builds the languages connection from the stored language string.
func languagesConn(lang string) map[string]any {
	if lang == "" {
		return emptyConn()
	}
	return gqlConn([]any{
		map[string]any{"name": lang, "size": 0},
	})
}

// viewerPermission computes the viewer's permission level on a repository.
func (s *Server) viewerPermission(ctx context.Context, rep db.Repository) string {
	u, err := s.Svc.GetCurrentUser(ctx)
	if err != nil {
		return "READ"
	}
	perm, err := s.Svc.HasRepoAccess(ctx, rep.ID, u.ID)
	if err != nil {
		return "READ"
	}
	switch perm {
	case service.RepoPermissionAdmin:
		return "ADMIN"
	case service.RepoPermissionMaintain:
		return "MAINTAIN"
	case service.RepoPermissionWrite:
		return "WRITE"
	case service.RepoPermissionTriage:
		return "TRIAGE"
	case service.RepoPermissionRead:
		return "READ"
	default:
		return "READ"
	}
}

// repoPushedAt returns the pushedAt timestamp for a repository, falling back to UpdatedAt.
func (s *Server) repoPushedAt(rep db.Repository) string {
	if rep.PushedAt != nil {
		return rep.PushedAt.Format(time.RFC3339)
	}
	return rep.UpdatedAt.Format(time.RFC3339)
}

// latestReleaseGQL returns the latest non-draft release for a repository, or nil.
func (s *Server) latestReleaseGQL(ctx context.Context, rep db.Repository) any {
	releases, err := s.Svc.ListReleases(ctx, rep.FullName)
	if err != nil || len(releases) == 0 {
		return nil
	}
	// ListReleases returns newest-first; find the first non-draft release.
	for _, r := range releases {
		if !r.Draft {
			publishedAt := ""
			if r.PublishedAt != nil {
				publishedAt = r.PublishedAt.Format(time.RFC3339)
			}
			return map[string]any{
				"id":           gqlID("Release", r.ID),
				"name":         r.Name,
				"tagName":      r.TagName,
				"tag":          map[string]any{"name": r.TagName},
				"isDraft":      r.Draft,
				"isPrerelease": r.PreRelease,
				"isLatest":     true,
				"publishedAt":  publishedAt,
				"url":          fmt.Sprintf("%s/%s/releases/tag/%s", s.Svc.HTMLBaseURL(), rep.FullName, r.TagName),
			}
		}
	}
	return nil
}
