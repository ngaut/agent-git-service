package service

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const adminsTeamSlug = "admins"

func ensureAdminsTeamTx(tx *gorm.DB, orgID uint) (db.Team, error) {
	if tx.Dialector.Name() == "mysql" {
		return ensureAdminsTeamMySQLTx(tx, orgID)
	}

	var team db.Team
	if err := tx.First(&team, "organization_id = ? AND slug = ?", orgID, adminsTeamSlug).Error; err == nil {
		return team, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return db.Team{}, err
	}
	// Two callers that race past the First() miss both reach Create(). Use
	// ON CONFLICT DO NOTHING so the loser doesn't propagate a unique-key
	// violation on idx_org_slug all the way back to the REST handler as a
	// 500. After the insert, re-read to pick up the winner's row — Create
	// with DoNothing leaves team.ID zero on the losing path. Sibling
	// ensureAdminsTeamMemberTx / ensureAdminsTeamRepoTx already use this
	// same upsert shape.
	team = db.Team{
		OrganizationID: orgID,
		Name:           adminsTeamSlug,
		Slug:           adminsTeamSlug,
		Privacy:        db.TeamPrivacyClosed,
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&team).Error; err != nil {
		return db.Team{}, err
	}
	if team.ID == 0 {
		if err := tx.First(&team, "organization_id = ? AND slug = ?", orgID, adminsTeamSlug).Error; err != nil {
			return db.Team{}, err
		}
	}
	return team, nil
}

func ensureAdminsTeamMySQLTx(tx *gorm.DB, orgID uint) (db.Team, error) {
	team := db.Team{
		OrganizationID: orgID,
		Name:           adminsTeamSlug,
		Slug:           adminsTeamSlug,
		Privacy:        db.TeamPrivacyClosed,
	}

	execer, ok := tx.Statement.ConnPool.(interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	})
	if !ok {
		return db.Team{}, errors.New("mysql conn pool does not support ExecContext")
	}

	ctx := tx.Statement.Context
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := execer.ExecContext(ctx, `
INSERT INTO teams (organization_id, name, slug, privacy)
VALUES (?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	id = LAST_INSERT_ID(id),
	privacy = VALUES(privacy)
`, team.OrganizationID, team.Name, team.Slug, team.Privacy)
	if err != nil {
		return db.Team{}, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return db.Team{}, err
	}
	team.ID = uint(id)
	return team, nil
}

func ensureAdminsTeamMemberTx(tx *gorm.DB, teamID, userID uint) error {
	member := db.TeamMember{
		TeamID: teamID,
		UserID: userID,
		Role:   "maintainer",
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "team_id"}, {Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"role"}),
	}).Create(&member).Error
}

func ensureAdminsTeamRepoTx(tx *gorm.DB, teamID, repoID uint) error {
	tr := db.TeamRepository{
		TeamID:       teamID,
		RepositoryID: repoID,
		Permission:   "admin",
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "team_id"}, {Name: "repository_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"permission"}),
	}).Create(&tr).Error
}

func ensureAdminsPrincipalsTx(tx *gorm.DB, orgID uint, principalIDs ...uint) (db.Team, error) {
	team, err := ensureAdminsTeamTx(tx, orgID)
	if err != nil {
		return db.Team{}, err
	}

	seen := make(map[uint]struct{}, len(principalIDs))
	for _, principalID := range principalIDs {
		if principalID == 0 {
			continue
		}
		if _, ok := seen[principalID]; ok {
			continue
		}
		seen[principalID] = struct{}{}

		if _, err := ensureOrgMembershipTx(tx, orgID, principalID, db.OrganizationRoleOwner); err != nil {
			return db.Team{}, err
		}
		if err := ensureAdminsTeamMemberTx(tx, team.ID, principalID); err != nil {
			return db.Team{}, err
		}
	}

	return team, nil
}
