package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/randutil"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	agentSuffixLen        = 6
	maxLoginLen           = 39
	maxAgentAttempts      = 10
	agentSwitchSessionTTL = 12 * time.Hour
)

var agentLoginPrefixRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}$`)

type AgentRegistrationResult struct {
	Login        string
	Token        string
	RepoFullName string
}

type AgentInviteRepoGrant struct {
	RepoFullName string `json:"repo_full_name"`
	Permission   string `json:"permission"`
}

type AgentInviteTeamGrant struct {
	Org      string `json:"org"`
	TeamSlug string `json:"team_slug"`
	Role     string `json:"role"`
}

type CreateAgentInviteInput struct {
	RepoGrants []AgentInviteRepoGrant
	TeamGrants []AgentInviteTeamGrant
}

type BoundAgentTokenStatus struct {
	State     string
	CreatedAt *time.Time
}

type BoundAgentAccessSummary struct {
	Repos []AgentInviteRepoGrant
	Teams []AgentInviteTeamGrant
}

type BoundAgent struct {
	Agent         db.User
	BoundAt       time.Time
	TokenStatus   BoundAgentTokenStatus
	AccessSummary BoundAgentAccessSummary
}

type AgentSwitchSessionResult struct {
	Agent db.User
	Token db.Token
}

// RegisterAgent creates a new agent account, issues a token, and creates a default repo.
func (s *Service) RegisterAgent(ctx context.Context, prefixLogin, defaultRepoName string) (AgentRegistrationResult, error) {
	prefix := strings.ToLower(strings.TrimSpace(prefixLogin))
	if prefix == "" {
		return AgentRegistrationResult{}, fmt.Errorf("%w: prefix_login is required", ErrValidation)
	}
	if !agentLoginPrefixRE.MatchString(prefix) {
		return AgentRegistrationResult{}, fmt.Errorf("%w: invalid prefix_login", ErrValidation)
	}
	maxPrefixLen := maxLoginLen - 1 - agentSuffixLen
	if maxPrefixLen < 1 || len(prefix) > maxPrefixLen {
		return AgentRegistrationResult{}, fmt.Errorf("%w: prefix_login too long", ErrValidation)
	}

	repoName := strings.TrimSpace(defaultRepoName)
	if repoName == "" {
		return AgentRegistrationResult{}, fmt.Errorf("%w: default_repo_name is required", ErrValidation)
	}
	if strings.Contains(repoName, "/") {
		return AgentRegistrationResult{}, fmt.Errorf("%w: default_repo_name is invalid", ErrValidation)
	}

	var created db.User
	var tok db.Token

	for attempt := 0; attempt < maxAgentAttempts; attempt++ {
		login := prefix + "-" + randutil.Hex(agentSuffixLen)
		err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
			u := db.User{
				Login:    login,
				Name:     login,
				Type:     db.TypeUser,
				UserKind: db.UserKindAgent,
			}
			if err := tx.Create(&u).Error; err != nil {
				if isDuplicateErr(err) {
					return ErrConflict
				}
				return err
			}
			t, err := issueUserTokenTx(tx, u.ID, time.Now(), "agent", nil)
			if err != nil {
				return err
			}
			created = u
			tok = t
			return nil
		})
		if err == nil {
			break
		}
		if errors.Is(err, ErrConflict) {
			time.Sleep(retryDelay(attempt))
			continue
		}
		return AgentRegistrationResult{}, err
	}
	if created.ID == 0 {
		return AgentRegistrationResult{}, fmt.Errorf("%w: failed to allocate login after retries", ErrConflict)
	}

	agentCtx := ContextWithUser(ctx, created)
	repo, err := s.CreateRepo(agentCtx, CreateRepoInput{
		OwnerLogin:    created.Login,
		Name:          repoName,
		Description:   "",
		Private:       true,
		HasIssues:     true,
		HasWiki:       true,
		DefaultBranch: "main",
	})
	if err != nil {
		_ = s.DBForCtx(ctx).Where("user_id = ?", created.ID).Delete(&db.Token{}).Error
		_ = s.DBForCtx(ctx).Delete(&created).Error
		return AgentRegistrationResult{}, err
	}

	return AgentRegistrationResult{
		Login:        created.Login,
		Token:        tok.Value,
		RepoFullName: repo.FullName,
	}, nil
}

func normalizeAgentInviteInput(ctx context.Context, s *Service, human db.User, input CreateAgentInviteInput) ([]AgentInviteRepoGrant, []AgentInviteTeamGrant, error) {
	humanCtx := ContextWithUser(ctx, human)
	normalizedRepos := make([]AgentInviteRepoGrant, 0, len(input.RepoGrants))
	seenRepos := map[string]struct{}{}
	for _, grant := range input.RepoGrants {
		fullName := strings.TrimSpace(grant.RepoFullName)
		if fullName == "" {
			return nil, nil, fmt.Errorf("%w: repo_full_name is required", ErrValidation)
		}
		permission, ok := NormalizeGrantPermission(grant.Permission)
		if !ok {
			return nil, nil, fmt.Errorf("%w: %s", ErrValidation, GrantPermissionValidationMessage)
		}
		repo, err := repoByFullNameTx(s.DBForCtx(ctx), fullName)
		if err != nil {
			return nil, nil, err
		}
		viewerPerm, err := s.HasRepoAccess(humanCtx, repo.ID, human.ID)
		if err != nil {
			return nil, nil, err
		}
		allowed := viewerPerm.AtLeast(RepoPermissionAdmin)
		if !allowed && repo.Owner.Type == db.TypeOrganization {
			isOrgAdmin, err := s.IsOrgAdmin(humanCtx, repo.OwnerID, human.ID)
			if err != nil {
				return nil, nil, err
			}
			allowed = isOrgAdmin
		}
		if !allowed {
			return nil, nil, fmt.Errorf("%w: admin repo access required for %s", ErrForbidden, fullName)
		}
		if _, ok := seenRepos[repo.FullName]; ok {
			continue
		}
		seenRepos[repo.FullName] = struct{}{}
		normalizedRepos = append(normalizedRepos, AgentInviteRepoGrant{RepoFullName: repo.FullName, Permission: permission})
	}

	normalizedTeams := make([]AgentInviteTeamGrant, 0, len(input.TeamGrants))
	seenTeams := map[string]struct{}{}
	for _, grant := range input.TeamGrants {
		orgLogin := strings.TrimSpace(grant.Org)
		teamSlug := strings.TrimSpace(grant.TeamSlug)
		if orgLogin == "" || teamSlug == "" {
			return nil, nil, fmt.Errorf("%w: org and team_slug are required", ErrValidation)
		}
		role, ok := normalizeTeamMemberRoleValue(grant.Role)
		if !ok {
			return nil, nil, fmt.Errorf("%w: team role must be member or maintainer", ErrValidation)
		}
		org, err := s.GetUser(humanCtx, orgLogin)
		if err != nil {
			return nil, nil, err
		}
		if org.Type != db.TypeOrganization {
			return nil, nil, fmt.Errorf("%w: %s is not an organization", ErrValidation, orgLogin)
		}
		team, err := s.GetTeam(humanCtx, org.ID, teamSlug)
		if err != nil {
			return nil, nil, err
		}
		canManage, _, err := s.CanManageTeamMembership(humanCtx, org.ID, team.ID, human.ID)
		if err != nil {
			return nil, nil, err
		}
		if !canManage {
			return nil, nil, fmt.Errorf("%w: team membership admin permission required for %s/%s", ErrForbidden, orgLogin, teamSlug)
		}
		key := org.Login + "/" + team.Slug
		if _, ok := seenTeams[key]; ok {
			continue
		}
		seenTeams[key] = struct{}{}
		normalizedTeams = append(normalizedTeams, AgentInviteTeamGrant{Org: org.Login, TeamSlug: team.Slug, Role: role})
	}

	return normalizedRepos, normalizedTeams, nil
}

func marshalAgentInviteGrants(repoGrants []AgentInviteRepoGrant, teamGrants []AgentInviteTeamGrant) (string, string, error) {
	repoJSON, err := json.Marshal(repoGrants)
	if err != nil {
		return "", "", err
	}
	teamJSON, err := json.Marshal(teamGrants)
	if err != nil {
		return "", "", err
	}
	return string(repoJSON), string(teamJSON), nil
}

func splitRepoFullName(fullName string) (string, string, bool) {
	owner, repo, ok := strings.Cut(strings.TrimSpace(fullName), "/")
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	if !ok || owner == "" || repo == "" {
		return "", "", false
	}
	return owner, repo, true
}

func repoByFullNameTx(tx *gorm.DB, fullName string) (db.Repository, error) {
	owner, repoName, ok := splitRepoFullName(fullName)
	if !ok {
		return db.Repository{}, fmt.Errorf("%w: invalid repo_full_name", ErrValidation)
	}
	var repo db.Repository
	err := preloadRepoFull(tx).
		Joins("JOIN users owner ON owner.id = repositories.owner_id").
		Where("owner.login = ? AND repositories.name = ?", owner, repoName).
		First(&repo).Error
	return repo, wrapErr(err)
}

func getUserTx(tx *gorm.DB, login string) (db.User, error) {
	var user db.User
	err := tx.First(&user, "login = ?", login).Error
	return user, wrapErr(err)
}

func getTeamTx(tx *gorm.DB, orgID uint, slug string) (db.Team, error) {
	var team db.Team
	err := tx.First(&team, "organization_id = ? AND slug = ?", orgID, slug).Error
	return team, wrapErr(err)
}

func applyAgentInviteGrantsTx(tx *gorm.DB, s *Service, invite db.AgentInvite, human db.User, agent db.User) error {
	var repoGrants []AgentInviteRepoGrant
	if strings.TrimSpace(invite.RepoGrantsJSON) != "" {
		if err := json.Unmarshal([]byte(invite.RepoGrantsJSON), &repoGrants); err != nil {
			return fmt.Errorf("%w: invalid repo grant payload", ErrValidation)
		}
	}
	for _, grant := range repoGrants {
		permission, ok := NormalizeGrantPermission(grant.Permission)
		if !ok {
			return fmt.Errorf("%w: %s", ErrValidation, GrantPermissionValidationMessage)
		}
		repo, err := repoByFullNameTx(tx, grant.RepoFullName)
		if err != nil {
			return err
		}
		collab := db.Collaborator{RepositoryID: repo.ID, UserID: agent.ID, Permission: permission}
		if err := upsertCollaboratorTx(tx, &collab); err != nil {
			return err
		}
		orgID, err := repoOrganizationIDTx(tx, repo.ID)
		if err != nil {
			return err
		}
		if orgID != 0 {
			if err := syncOutsideCollaboratorForOrgTx(tx, orgID, agent.ID); err != nil {
				return err
			}
		}
	}

	var teamGrants []AgentInviteTeamGrant
	if strings.TrimSpace(invite.TeamGrantsJSON) != "" {
		if err := json.Unmarshal([]byte(invite.TeamGrantsJSON), &teamGrants); err != nil {
			return fmt.Errorf("%w: invalid team grant payload", ErrValidation)
		}
	}
	for _, grant := range teamGrants {
		role, ok := normalizeTeamMemberRoleValue(grant.Role)
		if !ok {
			return fmt.Errorf("%w: team role must be member or maintainer", ErrValidation)
		}
		org, err := getUserTx(tx, strings.TrimSpace(grant.Org))
		if err != nil {
			return err
		}
		if org.Type != db.TypeOrganization {
			return fmt.Errorf("%w: %s is not an organization", ErrValidation, grant.Org)
		}
		team, err := getTeamTx(tx, org.ID, strings.TrimSpace(grant.TeamSlug))
		if err != nil {
			return err
		}
		var membershipCount int64
		if err := tx.Model(&db.OrganizationMember{}).
			Where("organization_id = ? AND user_id = ?", org.ID, agent.ID).
			Count(&membershipCount).Error; err != nil {
			return wrapErr(err)
		}
		if membershipCount == 0 {
			humanCtx := ContextWithDB(ContextWithUser(context.Background(), human), tx)
			canManage, canInviteUnaffiliated, err := s.CanManageTeamMembership(humanCtx, org.ID, team.ID, human.ID)
			if err != nil {
				return err
			}
			if !canManage {
				return fmt.Errorf("%w: team membership admin permission required for %s/%s", ErrForbidden, org.Login, team.Slug)
			}
			if !canInviteUnaffiliated {
				return fmt.Errorf("%w: org admin access required for unaffiliated invite to %s/%s", ErrForbidden, org.Login, team.Slug)
			}
			if _, err := ensureOrgMembershipTx(tx, org.ID, agent.ID, db.OrganizationRoleMember); err != nil {
				return err
			}
		}
		if err := ensureTeamMemberTx(tx, team.ID, agent.ID, role); err != nil {
			return err
		}
	}
	return nil
}

func latestTokenForUserTx(tx *gorm.DB, userID uint) (db.Token, error) {
	var tok db.Token
	err := tx.Where("user_id = ?", userID).Order("created_at DESC, id DESC").First(&tok).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return db.Token{}, nil
	}
	return tok, err
}

func boundAgentAccessSummaryTx(tx *gorm.DB, agentID uint) (BoundAgentAccessSummary, error) {
	summary := BoundAgentAccessSummary{Repos: []AgentInviteRepoGrant{}, Teams: []AgentInviteTeamGrant{}}
	var repoRows []struct {
		FullName   string
		Permission string
	}
	if err := tx.Table("collaborators").
		Select("repositories.full_name, collaborators.permission").
		Joins("JOIN repositories ON repositories.id = collaborators.repository_id").
		Where("collaborators.user_id = ?", agentID).
		Order("repositories.full_name ASC").
		Scan(&repoRows).Error; err != nil {
		return summary, err
	}
	for _, row := range repoRows {
		summary.Repos = append(summary.Repos, AgentInviteRepoGrant{RepoFullName: row.FullName, Permission: row.Permission})
	}

	var teamRows []struct {
		OrgLogin string
		TeamSlug string
		Role     string
	}
	if err := tx.Table("team_members").
		Select("orgs.login as org_login, teams.slug as team_slug, team_members.role").
		Joins("JOIN teams ON teams.id = team_members.team_id").
		Joins("JOIN users orgs ON orgs.id = teams.organization_id").
		Where("team_members.user_id = ?", agentID).
		Order("orgs.login ASC, teams.slug ASC").
		Scan(&teamRows).Error; err != nil {
		return summary, err
	}
	for _, row := range teamRows {
		summary.Teams = append(summary.Teams, AgentInviteTeamGrant{Org: row.OrgLogin, TeamSlug: row.TeamSlug, Role: row.Role})
	}
	return summary, nil
}

// CreateAgentInvite creates a binding invite for the current human user.
func (s *Service) CreateAgentInvite(ctx context.Context, input CreateAgentInviteInput) (db.AgentInvite, error) {
	human, err := s.GetCurrentUser(ctx)
	if err != nil {
		return db.AgentInvite{}, err
	}
	if human.UserKind != db.UserKindHuman {
		return db.AgentInvite{}, fmt.Errorf("%w: only human accounts can create invites", ErrForbidden)
	}
	repoGrants, teamGrants, err := normalizeAgentInviteInput(ctx, s, human, input)
	if err != nil {
		return db.AgentInvite{}, err
	}
	repoJSON, teamJSON, err := marshalAgentInviteGrants(repoGrants, teamGrants)
	if err != nil {
		return db.AgentInvite{}, err
	}

	var invite db.AgentInvite
	for attempt := 0; attempt < maxAgentAttempts; attempt++ {
		token := randutil.Hex(32)
		invite = db.AgentInvite{
			Token:          token,
			HumanUserID:    human.ID,
			RepoGrantsJSON: repoJSON,
			TeamGrantsJSON: teamJSON,
		}
		if err := s.DBForCtx(ctx).Create(&invite).Error; err != nil {
			if isDuplicateErr(err) {
				continue
			}
			return db.AgentInvite{}, err
		}
		return invite, nil
	}
	return db.AgentInvite{}, fmt.Errorf("%w: failed to allocate invite token", ErrConflict)
}

// ConfirmAgentBinding binds the authenticated agent to the invite's human owner.
func (s *Service) ConfirmAgentBinding(ctx context.Context, inviteToken string) (db.AgentBinding, error) {
	inviteToken = strings.TrimSpace(inviteToken)
	if inviteToken == "" {
		return db.AgentBinding{}, fmt.Errorf("%w: invite_token is required", ErrValidation)
	}

	agent, err := s.GetCurrentUser(ctx)
	if err != nil {
		return db.AgentBinding{}, err
	}
	if agent.UserKind != db.UserKindAgent {
		return db.AgentBinding{}, fmt.Errorf("%w: only agent accounts can confirm binding", ErrForbidden)
	}

	var binding db.AgentBinding
	err = s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		var invite db.AgentInvite
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&invite, "token = ?", inviteToken).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: invalid invite token", ErrValidation)
			}
			return err
		}
		if invite.ExpiresAt != nil && invite.ExpiresAt.Before(time.Now().UTC()) {
			return fmt.Errorf("%w: invite expired", ErrValidation)
		}
		if invite.ConsumedAt != nil {
			return fmt.Errorf("%w: invite already consumed", ErrConflict)
		}

		var existing db.AgentBinding
		if err := tx.First(&existing, "agent_user_id = ?", agent.ID).Error; err == nil {
			return fmt.Errorf("%w: agent already bound", ErrConflict)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var human db.User
		if err := tx.First(&human, invite.HumanUserID).Error; err != nil {
			return wrapErr(err)
		}
		if human.UserKind != db.UserKindHuman {
			return fmt.Errorf("%w: invite owner is not a human account", ErrValidation)
		}
		if human.ID == agent.ID {
			return fmt.Errorf("%w: cannot bind agent to itself", ErrValidation)
		}

		binding = db.AgentBinding{
			HumanUserID: human.ID,
			AgentUserID: agent.ID,
		}
		if err := tx.Create(&binding).Error; err != nil {
			return err
		}
		orgIDs := map[uint]struct{}{}
		var adminTeamIDs []uint
		if err := tx.
			Table("teams").
			Select("teams.id").
			Joins("JOIN team_members tm ON tm.team_id = teams.id").
			Where("tm.user_id = ? AND teams.slug = ?", agent.ID, adminsTeamSlug).
			Pluck("teams.id", &adminTeamIDs).Error; err != nil {
			return err
		}
		if len(adminTeamIDs) > 0 {
			var teams []db.Team
			if err := tx.Select("id", "organization_id").Where("id IN ?", adminTeamIDs).Find(&teams).Error; err != nil {
				return err
			}
			for _, team := range teams {
				orgIDs[team.OrganizationID] = struct{}{}
			}
		}
		var collaboratorOrgIDs []uint
		if err := tx.
			Table("repositories").
			Select("DISTINCT repositories.owner_id").
			Joins("JOIN users ON users.id = repositories.owner_id").
			Joins("JOIN collaborators c ON c.repository_id = repositories.id").
			Where("users.type = ? AND c.user_id = ? AND c.permission = ?", db.TypeOrganization, agent.ID, "admin").
			Pluck("repositories.owner_id", &collaboratorOrgIDs).Error; err != nil {
			return err
		}
		for _, orgID := range collaboratorOrgIDs {
			orgIDs[orgID] = struct{}{}
		}
		for orgID := range orgIDs {
			if _, err := ensureAdminsPrincipalsTx(tx, orgID, agent.ID, human.ID); err != nil {
				return err
			}
		}
		if err := applyAgentInviteGrantsTx(tx, s, invite, human, agent); err != nil {
			return err
		}
		consumedAt := time.Now().UTC()
		updates := map[string]any{
			"consumed_at":               consumedAt,
			"consumed_by_agent_user_id": agent.ID,
		}
		if err := tx.Model(&db.AgentInvite{}).Where("id = ?", invite.ID).Updates(updates).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return db.AgentBinding{}, err
	}
	return binding, nil
}

// ListBoundAgents returns bound agents for the given human user.
func (s *Service) ListBoundAgents(ctx context.Context, humanID uint) ([]BoundAgent, error) {
	var bindings []db.AgentBinding
	if err := s.DBForCtx(ctx).
		Preload("AgentUser").
		Where("human_user_id = ?", humanID).
		Order("created_at DESC, id DESC").
		Find(&bindings).Error; err != nil {
		return nil, err
	}
	out := make([]BoundAgent, 0, len(bindings))
	for _, b := range bindings {
		tok, err := latestTokenForUserTx(s.DBForCtx(ctx), b.AgentUserID)
		if err != nil {
			return nil, err
		}
		summary, err := boundAgentAccessSummaryTx(s.DBForCtx(ctx), b.AgentUserID)
		if err != nil {
			return nil, err
		}
		var createdAt *time.Time
		if tok.ID != 0 {
			createdAt = &tok.CreatedAt
		}
		out = append(out, BoundAgent{
			Agent:         b.AgentUser,
			BoundAt:       b.CreatedAt,
			TokenStatus:   BoundAgentTokenStatus{State: "active", CreatedAt: createdAt},
			AccessSummary: summary,
		})
	}
	return out, nil
}

// RenameBoundAgent updates the display name for a bound agent.
func (s *Service) RenameBoundAgent(ctx context.Context, humanID uint, agentLogin, name string) (db.User, error) {
	agentLogin = strings.TrimSpace(agentLogin)
	name = strings.TrimSpace(name)
	if agentLogin == "" || name == "" {
		return db.User{}, fmt.Errorf("%w: agent_login and name are required", ErrValidation)
	}
	var agent db.User
	err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&agent, "login = ?", agentLogin).Error; err != nil {
			return wrapErr(err)
		}
		var binding db.AgentBinding
		if err := tx.First(&binding, "human_user_id = ? AND agent_user_id = ?", humanID, agent.ID).Error; err != nil {
			return wrapErr(err)
		}
		if err := tx.Model(&db.User{}).Where("id = ?", agent.ID).Update("name", name).Error; err != nil {
			return err
		}
		agent.Name = name
		return nil
	})
	if err != nil {
		return db.User{}, err
	}
	return agent, nil
}

// ResetAgentToken revokes all tokens for the bound agent and issues a new one.
func (s *Service) ResetAgentToken(ctx context.Context, humanID uint, agentLogin string) (db.Token, error) {
	agentLogin = strings.TrimSpace(agentLogin)
	if agentLogin == "" {
		return db.Token{}, fmt.Errorf("%w: agent_login is required", ErrValidation)
	}

	var agent db.User
	if err := s.DBForCtx(ctx).First(&agent, "login = ?", agentLogin).Error; err != nil {
		return db.Token{}, wrapErr(err)
	}

	var binding db.AgentBinding
	if err := s.DBForCtx(ctx).First(&binding, "human_user_id = ? AND agent_user_id = ?", humanID, agent.ID).Error; err != nil {
		return db.Token{}, wrapErr(err)
	}

	var tok db.Token
	if err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", agent.ID).Delete(&db.Token{}).Error; err != nil {
			return err
		}
		t, err := issueUserTokenTx(tx, agent.ID, time.Now(), "reset", nil)
		if err != nil {
			return err
		}
		tok = t
		return nil
	}); err != nil {
		return db.Token{}, err
	}
	return tok, nil
}

// CreateAgentSwitchSession issues a temporary console session token for a bound
// agent without revoking the agent's existing long-lived tokens.
func (s *Service) CreateAgentSwitchSession(ctx context.Context, humanID uint, agentLogin string) (AgentSwitchSessionResult, error) {
	agentLogin = strings.TrimSpace(agentLogin)
	if agentLogin == "" {
		return AgentSwitchSessionResult{}, fmt.Errorf("%w: agent_login is required", ErrValidation)
	}

	var agent db.User
	if err := s.DBForCtx(ctx).First(&agent, "login = ?", agentLogin).Error; err != nil {
		return AgentSwitchSessionResult{}, wrapErr(err)
	}

	var binding db.AgentBinding
	if err := s.DBForCtx(ctx).First(&binding, "human_user_id = ? AND agent_user_id = ?", humanID, agent.ID).Error; err != nil {
		return AgentSwitchSessionResult{}, wrapErr(err)
	}

	expiresAt := time.Now().UTC().Add(agentSwitchSessionTTL)
	tok, err := s.CreateUserToken(ctx, agent.ID, "agent-switch-session", &expiresAt)
	if err != nil {
		return AgentSwitchSessionResult{}, err
	}

	return AgentSwitchSessionResult{Agent: agent, Token: tok}, nil
}

// RefreshAgentSwitchSession rotates an existing valid switch-session token into a
// fresh one while preserving the agent's long-lived tokens.
func (s *Service) RefreshAgentSwitchSession(ctx context.Context, currentAgentID uint, currentToken, agentLogin string) (AgentSwitchSessionResult, error) {
	currentToken = strings.TrimSpace(currentToken)
	agentLogin = strings.TrimSpace(agentLogin)
	if currentToken == "" {
		return AgentSwitchSessionResult{}, fmt.Errorf("%w: current switch-session token is required", ErrValidation)
	}
	if agentLogin == "" {
		return AgentSwitchSessionResult{}, fmt.Errorf("%w: agent_login is required", ErrValidation)
	}

	var result AgentSwitchSessionResult
	if err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		var agent db.User
		if err := tx.First(&agent, "id = ? AND login = ?", currentAgentID, agentLogin).Error; err != nil {
			return wrapErr(err)
		}

		var current db.Token
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "value = ? AND user_id = ?", currentToken, agent.ID).Error; err != nil {
			return wrapErr(err)
		}
		if current.Name != "agent-switch-session" {
			return fmt.Errorf("%w: token is not a switch session", ErrForbidden)
		}
		if current.ExpiresAt == nil || !current.ExpiresAt.After(time.Now().UTC()) {
			return fmt.Errorf("%w: switch session expired", ErrUnauthorized)
		}
		var binding db.AgentBinding
		if err := tx.First(&binding, "agent_user_id = ?", agent.ID).Error; err != nil {
			return wrapErr(err)
		}

		expiresAt := time.Now().UTC().Add(agentSwitchSessionTTL)
		next, err := issueUserTokenTx(tx, agent.ID, time.Now(), "agent-switch-session", &expiresAt)
		if err != nil {
			return err
		}
		if err := checkAffected(tx.Where("id = ? AND value = ? AND user_id = ?", current.ID, currentToken, agent.ID).Delete(&db.Token{})); err != nil {
			return err
		}
		result = AgentSwitchSessionResult{Agent: agent, Token: next}
		_ = binding
		return nil
	}); err != nil {
		return AgentSwitchSessionResult{}, err
	}
	return result, nil
}

func boundHumanIDForAgentQuery(q *gorm.DB, agentID uint) (uint, bool, error) {
	var binding db.AgentBinding
	if err := q.Select("human_user_id").First(&binding, "agent_user_id = ?", agentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, false, nil
		}
		if isMissingTableErr(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return binding.HumanUserID, true, nil
}
