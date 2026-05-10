package db

import "gorm.io/gorm"

// MigrateTeamMemberLookupIndexes ensures lookup indexes exist for access-control
// queries that filter team_members by user_id.
func MigrateTeamMemberLookupIndexes(database *gorm.DB) error {
	if !database.Migrator().HasTable(&TeamMember{}) {
		return nil
	}
	if database.Migrator().HasIndex(&TeamMember{}, "idx_team_members_user") {
		return nil
	}
	return database.Migrator().CreateIndex(&TeamMember{}, "idx_team_members_user")
}
