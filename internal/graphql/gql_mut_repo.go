package graphql

import (
	"context"
	"strings"

	"github.com/ngaut/agent-git-service/internal/service"
)

func (s *Server) doCreateRepository(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	name := strFrom(inp, "name")
	description := strFrom(inp, "description")
	visibility := strFrom(inp, "visibility")
	ownerID := strFrom(inp, "ownerId")
	_, ownerIDPresent := inp["ownerId"]
	isPrivate := strings.EqualFold(visibility, "PRIVATE") || strings.EqualFold(visibility, "INTERNAL")
	hasIssues := true
	hasWiki := true
	_, hasIssuesSet := inp["hasIssuesEnabled"]
	_, hasWikiSet := inp["hasWikiEnabled"]
	if v, ok := inp["hasIssuesEnabled"].(bool); ok {
		hasIssues = v
	}
	if v, ok := inp["hasWikiEnabled"].(bool); ok {
		hasWiki = v
	}

	// Resolve owner login from the node ID.
	// If ownerId was provided but is not a valid string, reject rather than
	// silently falling back to the authenticated user.
	var ownerLogin string
	if ownerID != "" {
		ownerLogin = s.resolveUserLoginByNodeID(ctx, ownerID)
		if ownerLogin == "" {
			return errResp("owner not found")
		}
	} else if ownerIDPresent {
		return errResp("owner not found")
	} else {
		u, err := s.Svc.GetCurrentUser(ctx)
		if err != nil {
			return errResp("authentication required")
		}
		ownerLogin = u.Login
	}

	rep, err := s.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    ownerLogin,
		Name:          name,
		Description:   description,
		Visibility:    strings.ToLower(visibility),
		Private:       isPrivate,
		HasIssues:     hasIssues,
		HasIssuesSet:  hasIssuesSet,
		HasWiki:       hasWiki,
		HasWikiSet:    hasWikiSet,
		DefaultBranch: "main",
	})
	if err != nil {
		return errResp(err.Error())
	}
	return wrap("createRepository", map[string]any{
		"repository": s.repoGQL(ctx, rep),
	})
}

// doCloneTemplateRepository handles the cloneTemplateRepository mutation.
// It creates a new repo (ignoring the template since we don't have real template content)
// and returns the new repo's id, name, owner, and url.
func (s *Server) doCloneTemplateRepository(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	name := strFrom(inp, "name")
	description := strFrom(inp, "description")
	visibility := strFrom(inp, "visibility")
	ownerID := strFrom(inp, "ownerId")
	_, ownerIDPresent := inp["ownerId"]
	isPrivate := strings.EqualFold(visibility, "PRIVATE") || strings.EqualFold(visibility, "INTERNAL")

	var ownerLogin string
	if ownerID != "" {
		ownerLogin = s.resolveUserLoginByNodeID(ctx, ownerID)
		if ownerLogin == "" {
			return errResp("owner not found")
		}
	} else if ownerIDPresent {
		return errResp("owner not found")
	} else {
		u, err := s.Svc.GetCurrentUser(ctx)
		if err != nil {
			return errResp("authentication required")
		}
		ownerLogin = u.Login
	}

	rep, err := s.Svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    ownerLogin,
		Name:          name,
		Description:   description,
		Visibility:    strings.ToLower(visibility),
		Private:       isPrivate,
		HasIssues:     true,
		HasWiki:       true,
		AutoInit:      true,
		DefaultBranch: "main",
	})
	if err != nil {
		return errResp(err.Error())
	}
	return wrap("cloneTemplateRepository", map[string]any{
		"repository": s.repoGQL(ctx, rep),
	})
}

// doUpdateRepository handles the updateRepository mutation.
// It updates repo settings (wiki, issues, homepage) by node ID.
func (s *Server) doUpdateRepository(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	repoID := strFrom(inp, "repositoryId")
	fullName := s.resolveRepoByNodeID(ctx, repoID)
	if fullName == "" {
		return errResp("repo not found")
	}

	update := service.UpdateRepoInput{}
	if v, ok := inp["hasWikiEnabled"].(bool); ok {
		update.HasWiki = &v
	}
	if v, ok := inp["hasIssuesEnabled"].(bool); ok {
		update.HasIssues = &v
	}
	if raw, ok := inp["homepageUrl"]; ok {
		update.Homepage.Set = true
		if raw != nil {
			if v, ok := raw.(string); ok {
				update.Homepage.Value = &v
			}
		}
	}

	if _, err := s.Svc.UpdateRepo(ctx, fullName, update); err != nil {
		return errResp(err.Error())
	}
	rep, err := s.Svc.GetRepo(ctx, fullName)
	if err != nil {
		return errResp(err.Error())
	}
	return wrap("updateRepository", map[string]any{
		"repository": s.repoGQL(ctx, rep),
	})
}

func (s *Server) doArchiveRepo(ctx context.Context, req gqlRequest, archive bool) map[string]any {
	inp := inputMap(req)
	repoID := strFrom(inp, "repositoryId")
	fullName := s.resolveRepoByNodeID(ctx, repoID)
	if fullName == "" {
		return errResp("repo not found")
	}
	archived := archive
	if _, err := s.Svc.UpdateRepo(ctx, fullName, service.UpdateRepoInput{Archived: &archived}); err != nil {
		return errResp(err.Error())
	}
	rep, err := s.Svc.GetRepo(ctx, fullName)
	if err != nil {
		return errResp(err.Error())
	}
	return wrap("archiveRepository", map[string]any{
		"repository": s.repoGQL(ctx, rep),
	})
}
