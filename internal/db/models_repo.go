package db

import "time"

// Repository represents a GitHub repository.
type Repository struct {
	ID                  uint   `gorm:"primaryKey;autoIncrement"`
	Name                string `gorm:"size:255;not null"`
	FullName            string `gorm:"uniqueIndex;size:512;not null"`
	Description         string `gorm:"size:1024"`
	OwnerID             uint   `gorm:"index"`
	Owner               User   `gorm:"foreignKey:OwnerID"`
	ParentID            *uint
	Parent              *Repository `gorm:"foreignKey:ParentID"`
	Private             bool        `gorm:"default:false"`
	Visibility          string      `gorm:"size:20;default:'public'"` // "public", "private", "internal"
	Fork                bool        `gorm:"default:false"`
	DefaultBranch       string      `gorm:"size:255;default:'main'"`
	Language            string      `gorm:"size:100"`
	License             string      `gorm:"size:100"`
	Archived            bool        `gorm:"default:false"`
	Disabled            bool        `gorm:"default:false"`
	IsTemplate          bool        `gorm:"default:false"`
	IsMirror            bool        `gorm:"default:false"`
	HasWiki             bool        `gorm:"default:true"`
	HasIssues           bool        `gorm:"default:true"`
	HasProjects         bool        `gorm:"default:true"`
	HasDownloads        bool        `gorm:"default:true"`
	HasDiscussions      bool        `gorm:"default:false"`
	Homepage            string      `gorm:"size:1024"`
	AllowMergeCommit    bool        `gorm:"default:true"`
	AllowSquashMerge    bool        `gorm:"default:true"`
	AllowRebaseMerge    bool        `gorm:"default:true"`
	AllowAutoMerge      bool        `gorm:"default:false"`
	AllowUpdateBranch   bool        `gorm:"default:false"`
	DeleteBranchOnMerge bool        `gorm:"default:false"`
	OpenIssueCount      int         `gorm:"default:0"`
	Topics              string      `gorm:"type:text"` // comma-separated topic list
	PushedAt            *time.Time
	Labels              []Label
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// Label represents an issue/PR label owned by a repository.
type Label struct {
	ID           uint       `gorm:"primaryKey;autoIncrement"`
	RepositoryID uint       `gorm:"uniqueIndex:idx_repo_label_name"`
	Repository   Repository `gorm:"foreignKey:RepositoryID"`
	Name         string     `gorm:"size:255;not null;uniqueIndex:idx_repo_label_name;index:idx_labels_name"`
	Color        string     `gorm:"size:6"` // hex without #
	Description  string     `gorm:"size:512"`
	Default      bool       `gorm:"default:false"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// DeployKey is an SSH deploy key attached to a repository.
type DeployKey struct {
	ID           uint       `gorm:"primaryKey;autoIncrement"`
	RepositoryID uint       `gorm:"index"`
	Repository   Repository `gorm:"foreignKey:RepositoryID"`
	Title        string     `gorm:"size:255"`
	Key          string     `gorm:"type:text"`
	ReadOnly     bool       `gorm:"default:true"`
	CreatedAt    time.Time
}

// Autolink represents an autolink reference for a repository.
type Autolink struct {
	ID                 uint   `gorm:"primaryKey;autoIncrement"`
	RepositoryFullName string `gorm:"size:512;uniqueIndex:idx_autolink_repo_prefix"`
	KeyPrefix          string `gorm:"size:255;uniqueIndex:idx_autolink_repo_prefix"`
	URLTemplate        string `gorm:"size:1024"`
	IsAlphanumeric     bool   `gorm:"default:false"`
}

// Ruleset represents a repository ruleset.
type Ruleset struct {
	ID             uint   `gorm:"primaryKey;autoIncrement"`
	RepositoryID   uint   `gorm:"not null;uniqueIndex:idx_ruleset_repo_name"`
	Name           string `gorm:"size:255;uniqueIndex:idx_ruleset_repo_name"`
	Target         string `gorm:"size:50"` // branch, tag
	Enforcement    string `gorm:"size:50"` // active, evaluate, disabled
	ConditionsJSON string `gorm:"type:text"`
	RulesJSON      string `gorm:"type:text"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Star tracks a user starring a repository.
type Star struct {
	ID           uint       `gorm:"primaryKey;autoIncrement"`
	UserID       uint       `gorm:"uniqueIndex:idx_star_user_repo"`
	User         User       `gorm:"foreignKey:UserID"`
	RepositoryID uint       `gorm:"uniqueIndex:idx_star_user_repo"`
	Repository   Repository `gorm:"foreignKey:RepositoryID"`
	CreatedAt    time.Time
}

// CommitStatus tracks an external CI check status on a commit.
type CommitStatus struct {
	ID           uint       `gorm:"primaryKey;autoIncrement"`
	RepositoryID uint       `gorm:"index"`
	Repository   Repository `gorm:"foreignKey:RepositoryID"`
	CommitSHA    string     `gorm:"size:40;index;not null"`
	State        string     `gorm:"size:20;not null"` // error, failure, pending, success
	TargetURL    string     `gorm:"size:1024"`
	Description  string     `gorm:"size:1024"`
	Context      string     `gorm:"size:255;default:'default'"`
	CreatorID    uint       `gorm:"index"`
	Creator      User       `gorm:"foreignKey:CreatorID"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Webhook configures a repository-level integration webhook.
type Webhook struct {
	ID           uint       `gorm:"primaryKey;autoIncrement"`
	RepositoryID uint       `gorm:"index"`
	Repository   Repository `gorm:"foreignKey:RepositoryID"`
	Name         string     `gorm:"size:50;default:'web'"`
	Active       bool       `gorm:"default:true"`
	EventsJSON   string     `gorm:"type:text"` // JSON array of event names
	ConfigJSON   string     `gorm:"type:text"` // JSON object containing url, content_type, secret, insecure_ssl
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// HookDelivery stores one webhook delivery attempt for a repository hook.
type HookDelivery struct {
	ID               uint          `gorm:"primaryKey;autoIncrement"`
	WebhookID        uint          `gorm:"index;not null"`
	Webhook          Webhook       `gorm:"foreignKey:WebhookID"`
	RepositoryID     uint          `gorm:"index;not null"`
	Repository       Repository    `gorm:"foreignKey:RepositoryID"`
	ParentDeliveryID *uint         `gorm:"index"`
	ParentDelivery   *HookDelivery `gorm:"foreignKey:ParentDeliveryID"`
	GUID             string        `gorm:"size:64;index;not null"`
	Event            string        `gorm:"size:64;index;not null"`
	Action           string        `gorm:"size:64"`
	Status           string        `gorm:"size:32;not null;default:'pending'"`
	StatusCode       int
	Redelivery       bool `gorm:"default:false"`
	DurationMillis   int64
	DeliveredAt      *time.Time
	RequestHeaders   string `gorm:"type:text"`
	RequestPayload   LargeText
	ResponseHeaders  string `gorm:"type:text"`
	ResponsePayload  LargeText
	LastError        string `gorm:"type:text"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Deployment tracks a deployment request for a repository.
type Deployment struct {
	ID           uint       `gorm:"primaryKey;autoIncrement"`
	RepositoryID uint       `gorm:"index"`
	Repository   Repository `gorm:"foreignKey:RepositoryID"`
	Ref          string     `gorm:"size:255;not null"`
	Task         string     `gorm:"size:255;default:'deploy'"`
	Environment  string     `gorm:"size:255;default:'production'"`
	Description  string     `gorm:"size:1024"`
	PayloadJSON  string     `gorm:"type:text"`
	CreatorID    uint       `gorm:"index"`
	Creator      User       `gorm:"foreignKey:CreatorID"`
	Transient    bool       `gorm:"default:false"`
	Production   bool       `gorm:"default:false"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// DeploymentStatus tracks the status of a specific deployment.
type DeploymentStatus struct {
	ID             uint       `gorm:"primaryKey;autoIncrement"`
	DeploymentID   uint       `gorm:"index"`
	Deployment     Deployment `gorm:"foreignKey:DeploymentID"`
	State          string     `gorm:"size:20;not null"` // error, failure, inactive, in_progress, queued, pending, success
	Description    string     `gorm:"size:1024"`
	EnvironmentURL string     `gorm:"size:1024"`
	LogURL         string     `gorm:"size:1024"`
	CreatorID      uint       `gorm:"index"`
	Creator        User       `gorm:"foreignKey:CreatorID"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// BranchProtection configures push and merge rules for a specific branch.
type BranchProtection struct {
	ID                       uint       `gorm:"primaryKey;autoIncrement"`
	RepositoryID             uint       `gorm:"uniqueIndex:idx_branch_prot_repo_name"`
	Repository               Repository `gorm:"foreignKey:RepositoryID"`
	BranchName               string     `gorm:"size:255;uniqueIndex:idx_branch_prot_repo_name;not null"`
	RequiredStatusChecksJSON string     `gorm:"type:text"` // JSON object
	EnforceAdmins            bool       `gorm:"default:false"`
	RequiredSignatures       bool       `gorm:"default:false"`
	RequiredPullRequestJSON  string     `gorm:"type:text"` // JSON object
	RestrictionsJSON         string     `gorm:"type:text"` // JSON object
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

// DependabotAlert represents a security vulnerability in a repository's dependencies.
type DependabotAlert struct {
	ID                        uint       `gorm:"primaryKey;autoIncrement"`
	Number                    int        `gorm:"index;not null"` // The alert number
	RepositoryID              uint       `gorm:"index"`
	Repository                Repository `gorm:"foreignKey:RepositoryID"`
	State                     string     `gorm:"size:20;not null"` // open, fixed, dismissed, auto_dismissed
	DependencyJSON            string     `gorm:"type:text"`        // JSON representing the dependency info
	SecurityAdvisoryJSON      string     `gorm:"type:text"`        // JSON for github advisory
	SecurityVulnerabilityJSON string     `gorm:"type:text"`        // JSON for specific vulnerability
	DismissedAt               *time.Time
	DismissedBy               *uint
	DismissedReason           string `gorm:"size:50"`
	FixedAt                   *time.Time
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

// RepositoryInvitation represents an invitation for a user to join a repository as a collaborator.
type RepositoryInvitation struct {
	ID           uint       `gorm:"primaryKey;autoIncrement"`
	RepositoryID uint       `gorm:"uniqueIndex:idx_repo_invite"`
	Repository   Repository `gorm:"foreignKey:RepositoryID"`
	InviteeID    uint       `gorm:"uniqueIndex:idx_repo_invite"`
	Invitee      User       `gorm:"foreignKey:InviteeID"`
	InviterID    uint       `gorm:"index"`
	Inviter      User       `gorm:"foreignKey:InviterID"`
	Permissions  string     `gorm:"size:20;default:'read'"` // read, write, admin (+ compatibility aliases at the API boundary)
	CreatedAt    time.Time
}

// Collaborator represents a user who has been granted access to a repository.
type Collaborator struct {
	RepositoryID uint       `gorm:"primaryKey"`
	Repository   Repository `gorm:"foreignKey:RepositoryID"`
	UserID       uint       `gorm:"primaryKey"`
	User         User       `gorm:"foreignKey:UserID"`
	Permission   string     `gorm:"size:20;not null;default:'read'"` // read, write, admin (+ compatibility aliases at the API boundary)
	CreatedAt    time.Time
}
