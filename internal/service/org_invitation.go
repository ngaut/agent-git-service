package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gh-server/internal/db"

	"gorm.io/gorm"
)

const (
	defaultOrganizationInvitationTTL          = 7 * 24 * time.Hour
	organizationInvitationRoleValidationError = "role must be direct_member or admin"
)

// CreateOrganizationInvitationInput describes a pending org invitation.
type CreateOrganizationInvitationInput struct {
	OrganizationID uint
	InviteeID      uint
	InviterID      uint
	Role           string
	TeamIDs        []uint
	ExpiresAt      *time.Time
}

func normalizeOrganizationInvitationRole(role string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", db.OrganizationInvitationRoleDirectMember:
		return db.OrganizationInvitationRoleDirectMember, true
	case db.OrganizationInvitationRoleAdmin:
		return db.OrganizationInvitationRoleAdmin, true
	default:
		return "", false
	}
}

func organizationInvitationMembershipRole(role string) (string, bool) {
	normalized, ok := normalizeOrganizationInvitationRole(role)
	if !ok {
		return "", false
	}
	if normalized == db.OrganizationInvitationRoleAdmin {
		return db.OrganizationRoleOwner, true
	}
	return db.OrganizationRoleMember, true
}

func normalizeTeamMemberRoleValue(role string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", "member":
		return "member", true
	case "maintainer":
		return "maintainer", true
	default:
		return "", false
	}
}

func mergeTeamMemberRole(existing, requested string) string {
	if existing == "maintainer" || requested == "maintainer" {
		return "maintainer"
	}
	return "member"
}

type organizationInvitationTeamAssignment struct {
	TeamID uint   `json:"id"`
	Role   string `json:"role,omitempty"`
}

func ensureTeamMemberTx(tx *gorm.DB, teamID, userID uint, role string) error {
	role, ok := normalizeTeamMemberRoleValue(role)
	if !ok {
		return ErrValidation
	}
	if teamID == 0 || userID == 0 {
		return ErrValidation
	}

	var existing db.TeamMember
	err := tx.First(&existing, "team_id = ? AND user_id = ?", teamID, userID).Error
	if err == nil {
		mergedRole := mergeTeamMemberRole(existing.Role, role)
		if mergedRole == existing.Role {
			return nil
		}
		return tx.Model(&db.TeamMember{}).
			Where("team_id = ? AND user_id = ?", teamID, userID).
			Update("role", mergedRole).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	member := db.TeamMember{
		TeamID: teamID,
		UserID: userID,
		Role:   role,
	}
	return tx.Create(&member).Error
}

func normalizeOrganizationInvitationTeamIDs(teamIDs []uint) []uint {
	if len(teamIDs) == 0 {
		return nil
	}

	seen := make(map[uint]struct{}, len(teamIDs))
	normalized := make([]uint, 0, len(teamIDs))
	for _, teamID := range teamIDs {
		if teamID == 0 {
			continue
		}
		if _, ok := seen[teamID]; ok {
			continue
		}
		seen[teamID] = struct{}{}
		normalized = append(normalized, teamID)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func normalizeOrganizationInvitationTeamAssignments(assignments []organizationInvitationTeamAssignment) []organizationInvitationTeamAssignment {
	if len(assignments) == 0 {
		return nil
	}

	mergedByTeam := make(map[uint]string, len(assignments))
	for _, assignment := range assignments {
		if assignment.TeamID == 0 {
			continue
		}
		role, ok := normalizeTeamMemberRoleValue(assignment.Role)
		if !ok {
			role = "member"
		}
		if existing, exists := mergedByTeam[assignment.TeamID]; exists {
			mergedByTeam[assignment.TeamID] = mergeTeamMemberRole(existing, role)
			continue
		}
		mergedByTeam[assignment.TeamID] = role
	}
	if len(mergedByTeam) == 0 {
		return nil
	}

	teamIDs := make([]uint, 0, len(mergedByTeam))
	for teamID := range mergedByTeam {
		teamIDs = append(teamIDs, teamID)
	}
	sort.Slice(teamIDs, func(i, j int) bool { return teamIDs[i] < teamIDs[j] })

	normalized := make([]organizationInvitationTeamAssignment, 0, len(teamIDs))
	for _, teamID := range teamIDs {
		normalized = append(normalized, organizationInvitationTeamAssignment{
			TeamID: teamID,
			Role:   mergedByTeam[teamID],
		})
	}
	return normalized
}

func encodeOrganizationInvitationTeamAssignments(assignments []organizationInvitationTeamAssignment) (string, error) {
	normalized := normalizeOrganizationInvitationTeamAssignments(assignments)
	if len(normalized) == 0 {
		return "[]", nil
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func decodeOrganizationInvitationTeamAssignments(raw string) ([]organizationInvitationTeamAssignment, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}

	var assignments []organizationInvitationTeamAssignment
	if err := json.Unmarshal([]byte(raw), &assignments); err == nil {
		return normalizeOrganizationInvitationTeamAssignments(assignments), nil
	}

	var teamIDs []uint
	if err := json.Unmarshal([]byte(raw), &teamIDs); err != nil {
		return nil, err
	}
	normalizedIDs := normalizeOrganizationInvitationTeamIDs(teamIDs)
	if len(normalizedIDs) == 0 {
		return nil, nil
	}
	assignments = make([]organizationInvitationTeamAssignment, 0, len(normalizedIDs))
	for _, teamID := range normalizedIDs {
		assignments = append(assignments, organizationInvitationTeamAssignment{
			TeamID: teamID,
			Role:   "member",
		})
	}
	return assignments, nil
}

func encodeOrganizationInvitationTeamIDs(teamIDs []uint) (string, error) {
	if len(teamIDs) == 0 {
		return "[]", nil
	}
	payload, err := json.Marshal(teamIDs)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func decodeOrganizationInvitationTeamIDs(raw string) ([]uint, error) {
	assignments, err := decodeOrganizationInvitationTeamAssignments(raw)
	if err != nil {
		return nil, err
	}
	if len(assignments) == 0 {
		return nil, nil
	}
	teamIDs := make([]uint, 0, len(assignments))
	for _, assignment := range assignments {
		teamIDs = append(teamIDs, assignment.TeamID)
	}
	return teamIDs, nil
}

// DecodeOrganizationInvitationTeamIDs parses the stored team id payload for REST serialization.
func DecodeOrganizationInvitationTeamIDs(raw string) ([]uint, error) {
	return decodeOrganizationInvitationTeamIDs(raw)
}

func normalizeOrganizationInvitationExpiry(expiresAt *time.Time) (*time.Time, error) {
	now := time.Now().UTC()
	if expiresAt == nil {
		defaultExpiry := now.Add(defaultOrganizationInvitationTTL)
		return &defaultExpiry, nil
	}

	normalized := expiresAt.UTC()
	if !normalized.After(now) {
		return nil, ErrValidation
	}
	return &normalized, nil
}

func activeOrganizationInvitationQuery(tx *gorm.DB) *gorm.DB {
	return tx.Where("expires_at IS NULL OR expires_at > ?", time.Now().UTC())
}

func preloadOrganizationInvitation(tx *gorm.DB) *gorm.DB {
	return tx.Preload("Organization").Preload("Invitee").Preload("Inviter")
}

func orgMembershipExistsTx(tx *gorm.DB, orgID, userID uint) (bool, error) {
	var count int64
	err := tx.Model(&db.OrganizationMember{}).
		Where("organization_id = ? AND user_id = ?", orgID, userID).
		Count(&count).Error
	return count > 0, err
}

func (s *Service) validateOrganizationInvitationTeams(ctx context.Context, orgID uint, teamIDs []uint) ([]uint, error) {
	normalized := normalizeOrganizationInvitationTeamIDs(teamIDs)
	if len(normalized) == 0 {
		return nil, nil
	}

	var count int64
	err := s.DBForCtx(ctx).
		Model(&db.Team{}).
		Where("organization_id = ? AND id IN ?", orgID, normalized).
		Count(&count).Error
	if err != nil {
		return nil, wrapErr(err)
	}
	if count != int64(len(normalized)) {
		return nil, ErrNotFound
	}
	return normalized, nil
}

func mergeOrganizationInvitationTeamAssignment(assignments []organizationInvitationTeamAssignment, teamID uint, role string) []organizationInvitationTeamAssignment {
	merged := append([]organizationInvitationTeamAssignment(nil), assignments...)
	for i := range merged {
		if merged[i].TeamID != teamID {
			continue
		}
		merged[i].Role = mergeTeamMemberRole(merged[i].Role, role)
		return normalizeOrganizationInvitationTeamAssignments(merged)
	}
	merged = append(merged, organizationInvitationTeamAssignment{TeamID: teamID, Role: role})
	return normalizeOrganizationInvitationTeamAssignments(merged)
}

// PendingTeamMembershipRole returns a pending membership role from an org invitation.
func (s *Service) PendingTeamMembershipRole(ctx context.Context, orgID, teamID, userID uint) (string, bool, error) {
	if orgID == 0 || teamID == 0 || userID == 0 {
		return "", false, ErrValidation
	}

	var inv db.OrganizationInvitation
	err := activeOrganizationInvitationQuery(s.DBForCtx(ctx)).
		Where("organization_id = ? AND invitee_id = ?", orgID, userID).
		First(&inv).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		return "", false, wrapErr(err)
	}

	assignments, err := decodeOrganizationInvitationTeamAssignments(inv.TeamIDsJSON)
	if err != nil {
		return "", false, wrapErr(err)
	}
	for _, assignment := range assignments {
		if assignment.TeamID == teamID {
			return assignment.Role, true, nil
		}
	}
	return "", false, nil
}

// UpsertTeamPendingOrganizationInvitation ensures a pending org invitation contains the given team assignment.
func (s *Service) UpsertTeamPendingOrganizationInvitation(ctx context.Context, orgID, teamID, inviteeID, inviterID uint, role string) error {
	if orgID == 0 || teamID == 0 || inviteeID == 0 || inviterID == 0 {
		return ErrValidation
	}
	role, ok := normalizeTeamMemberRoleValue(role)
	if !ok {
		return ErrValidation
	}

	now := time.Now().UTC()
	defaultExpiry := now.Add(defaultOrganizationInvitationTTL)
	err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		var org db.User
		if err := tx.Select("id", "type").First(&org, "id = ?", orgID).Error; err != nil {
			return err
		}
		if org.Type != db.TypeOrganization {
			return ErrNotFound
		}

		var team db.Team
		if err := tx.Select("id", "organization_id").First(&team, "id = ?", teamID).Error; err != nil {
			return err
		}
		if team.OrganizationID != orgID {
			return ErrNotFound
		}

		var invitee db.User
		if err := tx.Select("id", "type", "is_anonymous").First(&invitee, "id = ?", inviteeID).Error; err != nil {
			return err
		}
		if invitee.Type != db.TypeUser || invitee.IsAnonymous {
			return ErrValidation
		}

		if err := tx.Select("id").First(&db.User{}, "id = ?", inviterID).Error; err != nil {
			return err
		}

		var existing db.OrganizationInvitation
		err := tx.Where("organization_id = ? AND invitee_id = ?", orgID, inviteeID).
			First(&existing).Error
		switch {
		case err == nil:
			assignments, err := decodeOrganizationInvitationTeamAssignments(existing.TeamIDsJSON)
			if err != nil {
				return err
			}
			merged := mergeOrganizationInvitationTeamAssignment(assignments, teamID, role)
			teamIDsJSON, err := encodeOrganizationInvitationTeamAssignments(merged)
			if err != nil {
				return err
			}
			existing.InviterID = inviterID
			existing.TeamIDsJSON = teamIDsJSON
			if existing.ExpiresAt == nil || !existing.ExpiresAt.After(now) {
				existing.ExpiresAt = &defaultExpiry
			}
			return tx.Save(&existing).Error
		case errors.Is(err, gorm.ErrRecordNotFound):
			teamIDsJSON, err := encodeOrganizationInvitationTeamAssignments([]organizationInvitationTeamAssignment{
				{TeamID: teamID, Role: role},
			})
			if err != nil {
				return err
			}
			inv := db.OrganizationInvitation{
				OrganizationID: orgID,
				InviteeID:      inviteeID,
				InviterID:      inviterID,
				Role:           db.OrganizationInvitationRoleDirectMember,
				TeamIDsJSON:    teamIDsJSON,
				ExpiresAt:      &defaultExpiry,
			}
			return tx.Create(&inv).Error
		default:
			return err
		}
	})
	return wrapErr(err)
}

// CreateOrganizationInvitation creates or refreshes a pending org invitation.
func (s *Service) CreateOrganizationInvitation(ctx context.Context, in CreateOrganizationInvitationInput) (db.OrganizationInvitation, error) {
	if in.OrganizationID == 0 || in.InviteeID == 0 || in.InviterID == 0 {
		return db.OrganizationInvitation{}, ErrValidation
	}

	role, ok := normalizeOrganizationInvitationRole(in.Role)
	if !ok {
		return db.OrganizationInvitation{}, fmt.Errorf("%w: %s", ErrValidation, organizationInvitationRoleValidationError)
	}

	teamIDs, err := s.validateOrganizationInvitationTeams(ctx, in.OrganizationID, in.TeamIDs)
	if err != nil {
		return db.OrganizationInvitation{}, err
	}
	teamIDsJSON, err := encodeOrganizationInvitationTeamIDs(teamIDs)
	if err != nil {
		return db.OrganizationInvitation{}, err
	}
	expiresAt, err := normalizeOrganizationInvitationExpiry(in.ExpiresAt)
	if err != nil {
		return db.OrganizationInvitation{}, err
	}

	var invitationID uint
	err = s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		var org db.User
		if err := tx.Select("id", "type").First(&org, "id = ?", in.OrganizationID).Error; err != nil {
			return err
		}
		if org.Type != db.TypeOrganization {
			return ErrNotFound
		}

		var invitee db.User
		if err := tx.Select("id", "type", "is_anonymous").First(&invitee, "id = ?", in.InviteeID).Error; err != nil {
			return err
		}
		if invitee.Type != db.TypeUser || invitee.IsAnonymous {
			return ErrValidation
		}

		if err := tx.Select("id").First(&db.User{}, "id = ?", in.InviterID).Error; err != nil {
			return err
		}

		isMember, err := orgMembershipExistsTx(tx, in.OrganizationID, in.InviteeID)
		if err != nil {
			return err
		}
		if isMember {
			return ErrConflict
		}

		var existing db.OrganizationInvitation
		err = tx.Where("organization_id = ? AND invitee_id = ?", in.OrganizationID, in.InviteeID).
			First(&existing).Error
		switch {
		case err == nil:
			existing.InviterID = in.InviterID
			existing.Role = role
			existing.TeamIDsJSON = teamIDsJSON
			existing.ExpiresAt = expiresAt
			if err := tx.Save(&existing).Error; err != nil {
				return err
			}
			invitationID = existing.ID
			return nil
		case errors.Is(err, gorm.ErrRecordNotFound):
			inv := db.OrganizationInvitation{
				OrganizationID: in.OrganizationID,
				InviteeID:      in.InviteeID,
				InviterID:      in.InviterID,
				Role:           role,
				TeamIDsJSON:    teamIDsJSON,
				ExpiresAt:      expiresAt,
			}
			if err := tx.Create(&inv).Error; err != nil {
				return err
			}
			invitationID = inv.ID
			return nil
		default:
			return err
		}
	})
	if err != nil {
		return db.OrganizationInvitation{}, wrapErr(err)
	}

	inv, err := s.GetOrganizationInvitation(ctx, invitationID)
	if err != nil {
		return db.OrganizationInvitation{}, err
	}
	return *inv, nil
}

// ListOrganizationInvitations lists all pending invitations for an organization.
func (s *Service) ListOrganizationInvitations(ctx context.Context, orgID uint) ([]db.OrganizationInvitation, error) {
	var invitations []db.OrganizationInvitation
	err := preloadOrganizationInvitation(activeOrganizationInvitationQuery(s.DBForCtx(ctx))).
		Where("organization_id = ?", orgID).
		Order("created_at DESC, id DESC").
		Find(&invitations).Error
	return invitations, wrapErr(err)
}

// ListUserOrganizationInvitations lists pending org invitations for a user.
func (s *Service) ListUserOrganizationInvitations(ctx context.Context, userID uint) ([]db.OrganizationInvitation, error) {
	var invitations []db.OrganizationInvitation
	err := preloadOrganizationInvitation(activeOrganizationInvitationQuery(s.DBForCtx(ctx))).
		Where("invitee_id = ?", userID).
		Order("created_at DESC, id DESC").
		Find(&invitations).Error
	return invitations, wrapErr(err)
}

// GetOrganizationInvitation fetches a specific pending org invitation.
func (s *Service) GetOrganizationInvitation(ctx context.Context, inviteID uint) (*db.OrganizationInvitation, error) {
	var invitation db.OrganizationInvitation
	err := preloadOrganizationInvitation(activeOrganizationInvitationQuery(s.DBForCtx(ctx))).
		Where("id = ?", inviteID).
		First(&invitation).Error
	if err != nil {
		return nil, wrapErr(err)
	}
	return &invitation, nil
}

// AcceptOrganizationInvitation adds the user to the org and invited teams.
func (s *Service) AcceptOrganizationInvitation(ctx context.Context, inviteID, userID uint) error {
	inv, err := s.GetOrganizationInvitation(ctx, inviteID)
	if err != nil {
		return err
	}
	if inv.InviteeID != userID {
		return ErrUnauthorized
	}
	membershipRole, ok := organizationInvitationMembershipRole(inv.Role)
	if !ok {
		return fmt.Errorf("%w: invitation role %q is invalid", ErrInvalidState, inv.Role)
	}

	assignments, err := decodeOrganizationInvitationTeamAssignments(inv.TeamIDsJSON)
	if err != nil {
		return err
	}
	teamIDs := make([]uint, 0, len(assignments))
	roleByTeamID := make(map[uint]string, len(assignments))
	for _, assignment := range assignments {
		teamIDs = append(teamIDs, assignment.TeamID)
		roleByTeamID[assignment.TeamID] = assignment.Role
	}

	err = s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := ensureOrgMembershipTx(tx, inv.OrganizationID, userID, membershipRole); err != nil {
			return err
		}

		if len(teamIDs) > 0 {
			var teams []db.Team
			if err := tx.Select("id").
				Where("organization_id = ? AND id IN ?", inv.OrganizationID, teamIDs).
				Find(&teams).Error; err != nil {
				return err
			}
			for _, team := range teams {
				role := roleByTeamID[team.ID]
				if role == "" {
					role = "member"
				}
				if err := ensureTeamMemberTx(tx, team.ID, userID, role); err != nil {
					return err
				}
			}
		}

		return tx.Delete(&db.OrganizationInvitation{}, inv.ID).Error
	})
	return wrapErr(err)
}

// DeclineOrganizationInvitation removes a pending org invitation for the invitee.
func (s *Service) DeclineOrganizationInvitation(ctx context.Context, inviteID, userID uint) error {
	inv, err := s.GetOrganizationInvitation(ctx, inviteID)
	if err != nil {
		return err
	}
	if inv.InviteeID != userID {
		return ErrUnauthorized
	}
	return wrapErr(s.DBForCtx(ctx).Delete(&db.OrganizationInvitation{}, inviteID).Error)
}

// RevokeOrganizationInvitation deletes a pending org invitation by org admin action.
func (s *Service) RevokeOrganizationInvitation(ctx context.Context, orgID, inviteID uint) error {
	tx := activeOrganizationInvitationQuery(s.DBForCtx(ctx)).
		Where("id = ? AND organization_id = ?", inviteID, orgID).
		Delete(&db.OrganizationInvitation{})
	if tx.Error != nil {
		return wrapErr(tx.Error)
	}
	if tx.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
