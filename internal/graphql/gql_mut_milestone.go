package graphql

import (
	"context"
	"strings"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

// doCreateMilestone handles the createMilestone GraphQL mutation.
func (s *Server) doCreateMilestone(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	repoID := strFrom(inp, "repositoryId")
	if repoID == "" {
		return errResp("repositoryId is required")
	}

	fullName := s.resolveRepoByNodeID(ctx, repoID)
	if fullName == "" {
		return errResp("repository not found")
	}

	title := strFrom(inp, "title")
	if title == "" {
		return errResp("title is required")
	}

	description := strFrom(inp, "description")
	state := strFrom(inp, "state")

	// Validate state if provided
	if state != "" {
		state = strings.TrimSpace(strings.ToLower(state))
		switch state {
		case "open", "closed":
		default:
			return errResp("state must be one of: open, closed")
		}
	}

	// Parse dueOn if provided
	var dueOn *time.Time
	if rawDueOn, ok := inp["dueOn"]; ok && rawDueOn != nil {
		if dueOnStr, ok := rawDueOn.(string); ok && dueOnStr != "" {
			parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(dueOnStr))
			if err != nil {
				return errResp("dueOn must be ISO 8601 format")
			}
			dueOn = &parsed
		}
	}

	m, err := s.Svc.CreateMilestone(ctx, fullName, title, description, state)
	if err != nil {
		return errResp(err.Error())
	}

	// Update dueOn if provided (separate call like REST does)
	if dueOn != nil {
		m, err = s.Svc.UpdateMilestone(ctx, fullName, m.Number, service.UpdateMilestoneInput{
			DueOn: dueOn,
		})
		if err != nil {
			return errResp(err.Error())
		}
	}

	return wrap("createMilestone", map[string]any{
		"milestone": s.milestoneGQL(m),
	})
}

// doUpdateMilestone handles the updateMilestone GraphQL mutation.
func (s *Server) doUpdateMilestone(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	milestoneID := strFrom(inp, "milestoneId")
	if milestoneID == "" {
		return errResp("milestoneId is required")
	}

	dbID := parseNodeID(milestoneID, "Milestone")
	if dbID == 0 {
		return errResp("invalid milestone ID")
	}

	m, err := s.Svc.GetMilestoneByID(ctx, dbID)
	if err != nil {
		return errResp(err.Error())
	}

	// Build update input
	update := service.UpdateMilestoneInput{}

	if rawTitle, ok := inp["title"]; ok {
		if title, ok := rawTitle.(string); ok {
			update.Title = &title
		}
	}

	if rawDesc, ok := inp["description"]; ok {
		if desc, ok := rawDesc.(string); ok {
			update.Description = &desc
		}
	}

	if rawState, ok := inp["state"]; ok {
		if state, ok := rawState.(string); ok && state != "" {
			state = strings.TrimSpace(strings.ToLower(state))
			switch state {
			case "open", "closed":
			default:
				return errResp("state must be one of: open, closed")
			}
			update.State = &state
		}
	}

	if rawDueOn, ok := inp["dueOn"]; ok {
		if dueOnStr, ok := rawDueOn.(string); ok && dueOnStr != "" {
			parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(dueOnStr))
			if err != nil {
				return errResp("dueOn must be ISO 8601 format")
			}
			update.DueOn = &parsed
		} else if rawDueOn == nil {
			// Allow clearing dueOn with null
			var nilTime *time.Time
			update.DueOn = nilTime
		}
	}

	updated, err := s.Svc.UpdateMilestone(ctx, m.Repository.FullName, m.Number, update)
	if err != nil {
		return errResp(err.Error())
	}

	return wrap("updateMilestone", map[string]any{
		"milestone": s.milestoneGQL(updated),
	})
}

// doDeleteMilestone handles the deleteMilestone GraphQL mutation.
func (s *Server) doDeleteMilestone(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	milestoneID := strFrom(inp, "milestoneId")
	if milestoneID == "" {
		return errResp("milestoneId is required")
	}

	dbID := parseNodeID(milestoneID, "Milestone")
	if dbID == 0 {
		return errResp("invalid milestone ID")
	}

	m, err := s.Svc.GetMilestoneByID(ctx, dbID)
	if err != nil {
		return errResp(err.Error())
	}

	if err := s.Svc.DeleteMilestone(ctx, m.Repository.FullName, m.Number); err != nil {
		return errResp(err.Error())
	}

	return wrap("deleteMilestone", nil)
}

// doCloseMilestone handles the closeMilestone GraphQL mutation.
func (s *Server) doCloseMilestone(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	milestoneID := strFrom(inp, "milestoneId")
	if milestoneID == "" {
		return errResp("milestoneId is required")
	}

	dbID := parseNodeID(milestoneID, "Milestone")
	if dbID == 0 {
		return errResp("invalid milestone ID")
	}

	m, err := s.Svc.GetMilestoneByID(ctx, dbID)
	if err != nil {
		return errResp(err.Error())
	}

	state := db.StateClosed
	updated, err := s.Svc.UpdateMilestone(ctx, m.Repository.FullName, m.Number, service.UpdateMilestoneInput{
		State: &state,
	})
	if err != nil {
		return errResp(err.Error())
	}

	return wrap("closeMilestone", map[string]any{
		"milestone": s.milestoneGQL(updated),
	})
}

// doReopenMilestone handles the reopenMilestone GraphQL mutation.
func (s *Server) doReopenMilestone(ctx context.Context, req gqlRequest) map[string]any {
	inp := inputMap(req)
	milestoneID := strFrom(inp, "milestoneId")
	if milestoneID == "" {
		return errResp("milestoneId is required")
	}

	dbID := parseNodeID(milestoneID, "Milestone")
	if dbID == 0 {
		return errResp("invalid milestone ID")
	}

	m, err := s.Svc.GetMilestoneByID(ctx, dbID)
	if err != nil {
		return errResp(err.Error())
	}

	state := db.StateOpen
	updated, err := s.Svc.UpdateMilestone(ctx, m.Repository.FullName, m.Number, service.UpdateMilestoneInput{
		State: &state,
	})
	if err != nil {
		return errResp(err.Error())
	}

	return wrap("reopenMilestone", map[string]any{
		"milestone": s.milestoneGQL(updated),
	})
}
