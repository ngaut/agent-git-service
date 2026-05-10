package service

import (
	"strings"

	"gh-server/internal/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func ensureRepoRedirectTx(tx *gorm.DB, oldFullName string, repoID uint) error {
	oldFullName = strings.TrimSpace(oldFullName)
	if oldFullName == "" || repoID == 0 {
		return nil
	}

	redir := db.RepoRedirect{OldFullName: oldFullName, RepoID: repoID}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "old_full_name"}},
		DoNothing: true,
	}).Create(&redir).Error
}
