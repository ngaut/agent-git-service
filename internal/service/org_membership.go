package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// OrganizationMembershipView is the REST-facing org membership state for a user.
type OrganizationMembershipView struct {
	User  db.User
	Role  string
	State string
}

const organizationMembershipRoleValidationError = "role must be member or admin"

func normalizeOrganizationRole(role string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", db.OrganizationRoleMember:
		return db.OrganizationRoleMember, true
	case db.OrganizationRoleOwner:
		return db.OrganizationRoleOwner, true
	default:
		return "", false
	}
}

func mergeOrganizationRole(existing, requested string) string {
	if existing == db.OrganizationRoleOwner || requested == db.OrganizationRoleOwner {
		return db.OrganizationRoleOwner
	}
	return db.OrganizationRoleMember
}

func countOrganizationOwnersTx(tx *gorm.DB, orgID uint) (int64, error) {
	var count int64
	err := tx.Model(&db.OrganizationMember{}).
		Where("organization_id = ? AND role = ?", orgID, db.OrganizationRoleOwner).
		Count(&count).Error
	return count, err
}

func removeOrganizationTeamMembershipsTx(tx *gorm.DB, orgID, userID uint) error {
	var teamIDs []uint
	if err := tx.Model(&db.Team{}).
		Where("organization_id = ?", orgID).
		Pluck("id", &teamIDs).Error; err != nil {
		return err
	}
	if len(teamIDs) == 0 {
		return nil
	}
	return tx.Where("user_id = ? AND team_id IN ?", userID, teamIDs).
		Delete(&db.TeamMember{}).Error
}

func removeOrgMemberTx(tx *gorm.DB, orgID, userID uint) error {
	var membership db.OrganizationMember
	if err := tx.Where("organization_id = ? AND user_id = ?", orgID, userID).
		First(&membership).Error; err != nil {
		return err
	}

	if membership.Role == db.OrganizationRoleOwner {
		ownerCount, err := countOrganizationOwnersTx(tx, orgID)
		if err != nil {
			return err
		}
		if ownerCount <= 1 {
			return fmt.Errorf("%w: cannot remove the last organization owner", ErrConflict)
		}
	}

	if err := removeOrganizationTeamMembershipsTx(tx, orgID, userID); err != nil {
		return err
	}
	if err := tx.Where("organization_id = ? AND user_id = ?", orgID, userID).
		Delete(&db.OrganizationMember{}).Error; err != nil {
		return err
	}
	if err := tx.Where("organization_id = ? AND invitee_id = ?", orgID, userID).
		Delete(&db.OrganizationInvitation{}).Error; err != nil {
		return err
	}
	return syncOutsideCollaboratorForOrgTx(tx, orgID, userID)
}

func organizationMembershipResponseRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case db.OrganizationRoleOwner, db.OrganizationInvitationRoleAdmin:
		return "admin"
	default:
		return "member"
	}
}

func normalizeOrganizationMembershipRequestRole(role string) (membershipRole string, invitationRole string, responseRole string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", "member":
		return db.OrganizationRoleMember, db.OrganizationInvitationRoleDirectMember, "member", true
	case "admin":
		return db.OrganizationRoleOwner, db.OrganizationInvitationRoleAdmin, "admin", true
	default:
		return "", "", "", false
	}
}

func normalizeOrganizationMembersRoleFilter(role string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", "all":
		return "all", true
	case "admin":
		return "admin", true
	case "member":
		return "member", true
	default:
		return "", false
	}
}

// orgMembershipChange records what an org-membership tx actually did.
// Used by callers that emit audit events to suppress false-positive
// "member added" rows on role-update or no-op paths.
type orgMembershipChange int

const (
	orgMembershipUnchanged orgMembershipChange = iota
	orgMembershipCreated
	orgMembershipRoleUpdated
)

func ensureOrgMembershipTx(tx *gorm.DB, orgID, userID uint, role string) (orgMembershipChange, error) {
	role, ok := normalizeOrganizationRole(role)
	if !ok {
		return orgMembershipUnchanged, ErrValidation
	}
	if orgID == 0 || userID == 0 {
		return orgMembershipUnchanged, ErrValidation
	}

	member := db.OrganizationMember{
		OrganizationID: orgID,
		UserID:         userID,
		Role:           role,
	}

	var existing db.OrganizationMember
	if err := tx.First(&existing, "organization_id = ? AND user_id = ?", orgID, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "organization_id"}, {Name: "user_id"}},
				DoUpdates: clause.AssignmentColumns([]string{"role", "updated_at"}),
			}).Create(&member).Error; err != nil {
				return orgMembershipUnchanged, err
			}
			return orgMembershipCreated, deleteOutsideCollaboratorTx(tx, orgID, userID)
		}
		return orgMembershipUnchanged, err
	}

	mergedRole := mergeOrganizationRole(existing.Role, role)
	if mergedRole == existing.Role {
		return orgMembershipUnchanged, deleteOutsideCollaboratorTx(tx, orgID, userID)
	}
	if err := tx.Model(&db.OrganizationMember{}).
		Where("organization_id = ? AND user_id = ?", orgID, userID).
		Update("role", mergedRole).Error; err != nil {
		return orgMembershipUnchanged, err
	}
	return orgMembershipRoleUpdated, deleteOutsideCollaboratorTx(tx, orgID, userID)
}

func setOrgMembershipRoleTx(tx *gorm.DB, orgID, userID uint, role string) (orgMembershipChange, error) {
	role, ok := normalizeOrganizationRole(role)
	if !ok {
		return orgMembershipUnchanged, ErrValidation
	}
	if orgID == 0 || userID == 0 {
		return orgMembershipUnchanged, ErrValidation
	}

	var membership db.OrganizationMember
	if err := tx.Where("organization_id = ? AND user_id = ?", orgID, userID).First(&membership).Error; err != nil {
		return orgMembershipUnchanged, err
	}
	if membership.Role == role {
		return orgMembershipUnchanged, deleteOutsideCollaboratorTx(tx, orgID, userID)
	}

	if membership.Role == db.OrganizationRoleOwner && role != db.OrganizationRoleOwner {
		ownerCount, err := countOrganizationOwnersTx(tx, orgID)
		if err != nil {
			return orgMembershipUnchanged, err
		}
		if ownerCount <= 1 {
			return orgMembershipUnchanged, fmt.Errorf("%w: cannot remove the last organization owner", ErrConflict)
		}
	}

	if err := tx.Model(&db.OrganizationMember{}).
		Where("organization_id = ? AND user_id = ?", orgID, userID).
		Update("role", role).Error; err != nil {
		return orgMembershipUnchanged, err
	}
	return orgMembershipRoleUpdated, deleteOutsideCollaboratorTx(tx, orgID, userID)
}

// AddOrgMember adds or promotes a user in an organization. Audit
// emission is gated on whether a row was actually inserted —
// promotions and idempotent re-saves do NOT write org.add_member, so
// the audit log keeps the truthful "membership add/remove" minimum
// subset Phase B promised on #1296.
func (s *Service) AddOrgMember(ctx context.Context, orgID, userID uint, role string) error {
	var change orgMembershipChange
	if err := wrapErr(s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		change, err = ensureOrgMembershipTx(tx, orgID, userID, role)
		return err
	})); err != nil {
		return err
	}
	if change != orgMembershipCreated {
		return nil
	}
	var target db.User
	_ = s.DBForCtx(ctx).First(&target, "id = ?", userID).Error
	_ = s.LogAudit(ctx, AuditEvent{
		OrganizationID: &orgID,
		UserID:         &userID,
		Action:         AuditActionOrgAddMember,
		TargetLogin:    target.Login,
		Details:        "role=" + role,
	})
	return nil
}

// SetOrgMembership sets a user's org membership role to member/admin GitHub semantics.
// Existing members are updated in place; non-members are invited and returned as pending.
func (s *Service) SetOrgMembership(ctx context.Context, orgID uint, username, role string, inviterID uint) (OrganizationMembershipView, error) {
	if orgID == 0 || strings.TrimSpace(username) == "" {
		return OrganizationMembershipView{}, ErrValidation
	}

	membershipRole, invitationRole, responseRole, ok := normalizeOrganizationMembershipRequestRole(role)
	if !ok {
		return OrganizationMembershipView{}, fmt.Errorf("%w: %s", ErrValidation, organizationMembershipRoleValidationError)
	}

	user, err := s.GetUser(ctx, username)
	if err != nil {
		return OrganizationMembershipView{}, err
	}
	if user.Type != db.TypeUser || user.IsAnonymous {
		return OrganizationMembershipView{}, ErrNotFound
	}

	// SetOrgMembership's active path only ever updates an existing
	// member's role — setOrgMembershipRoleTx returns ErrNotFound when
	// no membership exists, falling through to the invitation branch.
	// So neither change outcome here is a "member added"; emitting
	// org.add_member would be a false positive. The role-update
	// vocabulary lands in a future PR.
	err = wrapErr(s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		_, err := setOrgMembershipRoleTx(tx, orgID, user.ID, membershipRole)
		return err
	}))
	switch {
	case err == nil:
		return OrganizationMembershipView{
			User:  user,
			Role:  responseRole,
			State: "active",
		}, nil
	case !errors.Is(err, ErrNotFound):
		return OrganizationMembershipView{}, err
	}

	if inviterID == 0 {
		return OrganizationMembershipView{}, ErrValidation
	}

	if _, err := s.CreateOrganizationInvitation(ctx, CreateOrganizationInvitationInput{
		OrganizationID: orgID,
		InviteeID:      user.ID,
		InviterID:      inviterID,
		Role:           invitationRole,
	}); err != nil {
		return OrganizationMembershipView{}, err
	}

	return OrganizationMembershipView{
		User:  user,
		Role:  responseRole,
		State: "pending",
	}, nil
}

// GetOrgMember returns an organization membership record by org/user.
func (s *Service) GetOrgMember(ctx context.Context, orgID, userID uint) (db.OrganizationMember, error) {
	var member db.OrganizationMember
	err := s.DBForCtx(ctx).
		Where("organization_id = ? AND user_id = ?", orgID, userID).
		First(&member).Error
	return member, wrapErr(err)
}

// ListOrgMembers returns active organization members with their REST-facing roles.
func (s *Service) ListOrgMembers(ctx context.Context, orgID uint, roleFilter string) ([]OrganizationMembershipView, error) {
	if orgID == 0 {
		return []OrganizationMembershipView{}, nil
	}
	roleFilter, ok := normalizeOrganizationMembersRoleFilter(roleFilter)
	if !ok {
		return nil, ErrValidation
	}

	var members []db.OrganizationMember
	query := s.DBForCtx(ctx).
		Where("organization_id = ?", orgID).
		Preload("User")
	switch roleFilter {
	case "admin":
		query = query.Where("role = ?", db.OrganizationRoleOwner)
	case "member":
		query = query.Where("role = ?", db.OrganizationRoleMember)
	}
	if err := query.Find(&members).Error; err != nil {
		return nil, wrapErr(err)
	}

	sort.Slice(members, func(i, j int) bool {
		leftLogin := strings.ToLower(strings.TrimSpace(members[i].User.Login))
		rightLogin := strings.ToLower(strings.TrimSpace(members[j].User.Login))
		if leftLogin != rightLogin {
			return leftLogin < rightLogin
		}
		return members[i].UserID < members[j].UserID
	})

	views := make([]OrganizationMembershipView, 0, len(members))
	for _, member := range members {
		views = append(views, OrganizationMembershipView{
			User:  member.User,
			Role:  organizationMembershipResponseRole(member.Role),
			State: "active",
		})
	}
	return views, nil
}

// GetOrgMembership returns the active or pending organization membership for a user login.
func (s *Service) GetOrgMembership(ctx context.Context, orgID uint, username string) (OrganizationMembershipView, error) {
	if orgID == 0 || strings.TrimSpace(username) == "" {
		return OrganizationMembershipView{}, ErrValidation
	}

	user, err := s.GetUser(ctx, username)
	if err != nil {
		return OrganizationMembershipView{}, err
	}
	if user.Type != db.TypeUser || user.IsAnonymous {
		return OrganizationMembershipView{}, ErrNotFound
	}

	member, err := s.GetOrgMember(ctx, orgID, user.ID)
	switch {
	case err == nil:
		return OrganizationMembershipView{
			User:  user,
			Role:  organizationMembershipResponseRole(member.Role),
			State: "active",
		}, nil
	case !errors.Is(err, ErrNotFound):
		return OrganizationMembershipView{}, err
	}

	var invite db.OrganizationInvitation
	err = activeOrganizationInvitationQuery(s.DBForCtx(ctx)).
		Where("organization_id = ? AND invitee_id = ?", orgID, user.ID).
		First(&invite).Error
	if err != nil {
		return OrganizationMembershipView{}, wrapErr(err)
	}

	return OrganizationMembershipView{
		User:  user,
		Role:  organizationMembershipResponseRole(invite.Role),
		State: "pending",
	}, nil
}

// RemoveOrgMember removes an active organization member and cleans up org-scoped team memberships.
func (s *Service) RemoveOrgMember(ctx context.Context, orgID uint, username string) error {
	if orgID == 0 || strings.TrimSpace(username) == "" {
		return ErrValidation
	}

	user, err := s.GetUser(ctx, username)
	if err != nil {
		return err
	}
	if user.Type != db.TypeUser || user.IsAnonymous {
		return ErrNotFound
	}

	if err := wrapErr(s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		return removeOrgMemberTx(tx, orgID, user.ID)
	})); err != nil {
		return err
	}
	uid := user.ID
	_ = s.LogAudit(ctx, AuditEvent{
		OrganizationID: &orgID,
		UserID:         &uid,
		Action:         AuditActionOrgRemoveMember,
		TargetLogin:    user.Login,
	})
	return nil
}

// RemoveOrgMembership removes an active membership or revokes a pending invitation for the user.
func (s *Service) RemoveOrgMembership(ctx context.Context, orgID uint, username string) error {
	if orgID == 0 || strings.TrimSpace(username) == "" {
		return ErrValidation
	}

	user, err := s.GetUser(ctx, username)
	if err != nil {
		return err
	}
	if user.Type != db.TypeUser || user.IsAnonymous {
		return ErrNotFound
	}

	removedActiveMember := false
	if err := wrapErr(s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		if err := removeOrgMemberTx(tx, orgID, user.ID); err == nil {
			removedActiveMember = true
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) && !errors.Is(err, ErrNotFound) {
			return err
		}

		result := activeOrganizationInvitationQuery(tx).
			Where("organization_id = ? AND invitee_id = ?", orgID, user.ID).
			Delete(&db.OrganizationInvitation{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})); err != nil {
		return err
	}
	if !removedActiveMember {
		return nil
	}
	uid := user.ID
	_ = s.LogAudit(ctx, AuditEvent{
		OrganizationID: &orgID,
		UserID:         &uid,
		Action:         AuditActionOrgRemoveMember,
		TargetLogin:    user.Login,
	})
	return nil
}
