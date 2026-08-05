package db

import "time"

// User represents a GitHub user or organization account.
type User struct {
	ID    uint   `gorm:"primaryKey;autoIncrement"`
	Login string `gorm:"uniqueIndex;size:255;not null"`
	Name  string `gorm:"size:255"`
	Email string `gorm:"size:255"`
	Bio   string `gorm:"size:1024"`
	Type  string `gorm:"size:50;default:'User'"` // "User" or "Organization"
	// Status controls whether the account may approve or receive OAuth grants.
	Status string `gorm:"size:20;not null;default:'active'"` // active, banned, suspended, deleted
	// UserKind distinguishes human accounts from agent accounts.
	UserKind                    string `gorm:"size:16;not null;default:'human'"`
	SiteAdmin                   bool   `gorm:"default:false"`
	DefaultRepositoryPermission string `gorm:"size:20;not null;default:'none'"`
	CreatedAt                   time.Time
	UpdatedAt                   time.Time
}

// Token represents an OAuth access token.
type Token struct {
	ID         uint   `gorm:"primaryKey;autoIncrement"`
	UserID     uint   `gorm:"index"`
	User       User   `gorm:"foreignKey:UserID"`
	Name       string `gorm:"size:255"`
	Value      string `gorm:"uniqueIndex;size:255;not null"`
	CreatedAt  time.Time
	LastUsedAt *time.Time `gorm:"index"`
	ExpiresAt  *time.Time `gorm:"index"`
}

// UserIdentity links an external identity provider subject (for example, an OIDC sub).
// to a local user.
type UserIdentity struct {
	ID        uint   `gorm:"primaryKey;autoIncrement"`
	UserID    uint   `gorm:"index;not null"`
	User      User   `gorm:"foreignKey:UserID"`
	Provider  string `gorm:"size:32;not null;uniqueIndex:uidx_provider_subject"`
	Subject   string `gorm:"size:255;not null;uniqueIndex:uidx_provider_subject"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// DeviceCodeState represents the state of an OAuth device code.
type DeviceCodeState string

const (
	DeviceCodeStatePending  DeviceCodeState = "pending"
	DeviceCodeStateApproved DeviceCodeState = "approved"
	DeviceCodeStateExpired  DeviceCodeState = "expired"
	DeviceCodeStateRejected DeviceCodeState = "rejected"
)

// DeviceCode is used for the OAuth device flow.
type DeviceCode struct {
	ID          uint            `gorm:"primaryKey;autoIncrement"`
	DeviceCode  string          `gorm:"uniqueIndex;size:64;not null"`
	UserCode    string          `gorm:"size:10;not null"`
	State       DeviceCodeState `gorm:"size:16;not null;default:'pending'"`
	AccessToken string          `gorm:"size:255"` // populated after approval
	ExpiresAt   time.Time
	ApprovedBy  *uint      `gorm:"index"` // UserID of approving user, nil if not approved
	ApprovedAt  *time.Time // timestamp of approval
	CreatedAt   time.Time
}

// DeviceCodeAuditLog records audit events for OAuth device code flow.
type DeviceCodeAuditLog struct {
	ID           uint      `gorm:"primaryKey;autoIncrement"`
	DeviceCodeID uint      `gorm:"index;not null"`
	DeviceCode   string    `gorm:"size:64;not null"`
	Event        string    `gorm:"size:32;not null"` // created, verified, approved, rejected, expired, exchanged
	UserID       *uint     `gorm:"index"`            // user who performed the action (if applicable)
	UserLogin    string    `gorm:"size:255"`         // login of user (for logging even if UserID is null)
	Details      string    `gorm:"size:1024"`        // additional details (IP, user agent, etc.)
	CreatedAt    time.Time `gorm:"index"`
}

// AuthorizationCode stores OAuth authorization codes for PKCE token exchange.
type AuthorizationCode struct {
	ID                  uint       `gorm:"primaryKey;autoIncrement"`
	Code                string     `gorm:"uniqueIndex;size:64;not null"`
	UserID              *uint      `gorm:"index"`
	User                User       `gorm:"foreignKey:UserID"`
	RedirectURI         string     `gorm:"size:2048;not null"`
	CodeChallenge       string     `gorm:"size:128"`
	CodeChallengeMethod string     `gorm:"size:16"`
	ExpiresAt           time.Time  `gorm:"index;not null"`
	UsedAt              *time.Time `gorm:"index"`
	CreatedAt           time.Time
}

// SSHKey represents a user SSH key.
type SSHKey struct {
	ID        uint   `gorm:"primaryKey;autoIncrement"`
	UserID    uint   `gorm:"index"`
	User      User   `gorm:"foreignKey:UserID"`
	Title     string `gorm:"size:255"`
	Key       string `gorm:"type:text"`
	CreatedAt time.Time
}

// SSHSigningKey represents a user SSH signing key.
type SSHSigningKey struct {
	ID        uint   `gorm:"primaryKey;autoIncrement"`
	UserID    uint   `gorm:"index"`
	User      User   `gorm:"foreignKey:UserID"`
	Title     string `gorm:"size:255"`
	Key       string `gorm:"type:text"`
	CreatedAt time.Time
}

// GPGKey represents a user GPG key.
type GPGKey struct {
	ID        uint   `gorm:"primaryKey;autoIncrement"`
	UserID    uint   `gorm:"index"`
	User      User   `gorm:"foreignKey:UserID"`
	KeyID     string `gorm:"size:20"`
	PublicKey string `gorm:"type:text"`
	CreatedAt time.Time
}
