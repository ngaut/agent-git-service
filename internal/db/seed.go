package db

import (
	"fmt"

	"gorm.io/gorm"
)

// Seed inserts default data needed for the server to operate.
// When login and token are both empty, the legacy testadmin / mytoken
// values are used so that existing acceptance tests keep working.
// It returns an error if exactly one of login/token is provided,
// since a partial pair would seed unusable credentials.
func Seed(database *gorm.DB, login, token string) error {
	if (login == "") != (token == "") {
		return fmt.Errorf("ADMIN_LOGIN and ADMIN_TOKEN must both be set or both be empty (login_set=%t, token_set=%t)", login != "", token != "")
	}
	if login == "" && token == "" {
		login = "testadmin"
		token = "mytoken"
	}

	admin := ensureUser(database, login, login, "", TypeUser, true)
	org := ensureUser(database, "testorg", "Test Org", "", TypeOrganization, false)
	ensureOrgOwner(database, org.ID, admin.ID)

	// ensure token exists
	var tok Token
	if database.Where("value = ?", token).Take(&tok).Error != nil {
		database.Create(&Token{UserID: admin.ID, Value: token})
	}
	return nil
}

func ensureOrgOwner(database *gorm.DB, orgID, userID uint) {
	if orgID == 0 || userID == 0 {
		return
	}

	var membership OrganizationMember
	if err := database.First(&membership, "organization_id = ? AND user_id = ?", orgID, userID).Error; err == nil {
		if membership.Role != OrganizationRoleOwner {
			database.Model(&membership).Update("role", OrganizationRoleOwner)
		}
		return
	}

	database.Create(&OrganizationMember{
		OrganizationID: orgID,
		UserID:         userID,
		Role:           OrganizationRoleOwner,
	})
}

func ensureUser(database *gorm.DB, login, name, email, typ string, admin bool) User {
	var u User
	if database.First(&u, "login = ?", login).Error == nil {
		if admin && !u.SiteAdmin {
			database.Model(&u).Update("site_admin", true)
			u.SiteAdmin = true
		}
		if u.UserKind == "" {
			database.Model(&u).Update("user_kind", UserKindHuman)
			u.UserKind = UserKindHuman
		}
		return u
	}
	u = User{Login: login, Name: name, Email: email, Type: typ, SiteAdmin: admin, UserKind: UserKindHuman}
	database.Create(&u)
	return u
}
