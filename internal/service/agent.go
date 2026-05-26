package service

import (
	"context"
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
	agentSuffixLen   = 6
	maxLoginLen      = 39
	maxAgentAttempts = 10
)

var agentLoginPrefixRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}$`)

type AgentRegistrationResult struct {
	Login        string
	Token        string
	RepoFullName string
}

type BoundAgent struct {
	Agent   db.User
	Token   db.Token
	BoundAt time.Time
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
				Login:       login,
				Name:        login,
				Type:        db.TypeUser,
				UserKind:    db.UserKindAgent,
				IsAnonymous: false,
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
		if errors.Is(err, ErrConflict) || isSQLiteLockErr(err) {
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

// CreateAgentInvite creates a binding invite for the current human user.
func (s *Service) CreateAgentInvite(ctx context.Context) (db.AgentInvite, error) {
	human, err := s.GetCurrentUser(ctx)
	if err != nil {
		return db.AgentInvite{}, err
	}
	if human.UserKind != db.UserKindHuman {
		return db.AgentInvite{}, fmt.Errorf("%w: only human accounts can create invites", ErrForbidden)
	}

	var invite db.AgentInvite
	for attempt := 0; attempt < maxAgentAttempts; attempt++ {
		token := randutil.Hex(32)
		invite = db.AgentInvite{
			Token:       token,
			HumanUserID: human.ID,
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
		var tok db.Token
		_ = s.DBForCtx(ctx).
			Where("user_id = ?", b.AgentUserID).
			Order("created_at DESC, id DESC").
			First(&tok).Error
		out = append(out, BoundAgent{
			Agent:   b.AgentUser,
			Token:   tok,
			BoundAt: b.CreatedAt,
		})
	}
	return out, nil
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

func (s *Service) boundHumanIDForAgent(ctx context.Context, agentID uint) (uint, bool, error) {
	return boundHumanIDForAgentQuery(s.DBForCtx(ctx), agentID)
}
