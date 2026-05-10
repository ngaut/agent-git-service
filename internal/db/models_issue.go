package db

import "time"

// Milestone represents a GitHub milestone on a repository.
type Milestone struct {
	ID           uint       `gorm:"primaryKey;autoIncrement"`
	RepositoryID uint       `gorm:"not null;uniqueIndex:idx_ms_repo_number"`
	Repository   Repository `gorm:"foreignKey:RepositoryID"`
	Number       int        `gorm:"not null;uniqueIndex:idx_ms_repo_number"`
	Title        string     `gorm:"size:255;not null"`
	Description  string     `gorm:"type:text"`
	State        string     `gorm:"size:20;default:'open'"` // open, closed
	CreatorID    uint       `gorm:"index"`
	Creator      User       `gorm:"foreignKey:CreatorID"`
	DueOn        *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ClosedAt     *time.Time
}

// Issue represents a GitHub issue.
type Issue struct {
	ID               uint       `gorm:"primaryKey;autoIncrement"`
	Number           int        `gorm:"not null;uniqueIndex:idx_issue_repo_number"`
	RepositoryID     uint       `gorm:"index;uniqueIndex:idx_issue_repo_number;index:idx_issue_repo_created,priority:1;index:idx_issue_repo_state_created,priority:1"`
	Repository       Repository `gorm:"foreignKey:RepositoryID"`
	Title            string     `gorm:"size:1024;not null"`
	Body             LargeText
	State            string     `gorm:"size:20;default:'open';index:idx_issue_repo_state_created,priority:2;index:idx_issue_milestone_state,priority:2"`
	AuthorID         uint       `gorm:"index"`
	Author           User       `gorm:"foreignKey:AuthorID"`
	Labels           []Label    `gorm:"many2many:issue_labels;"`
	AssigneeLogins   string     `gorm:"size:2048"` // comma-separated user logins
	MilestoneID      *uint      `gorm:"index:idx_issue_milestone_state,priority:1"`
	Milestone        *Milestone `gorm:"foreignKey:MilestoneID"`
	Locked           bool       `gorm:"default:false"`
	ActiveLockReason string     `gorm:"size:30"` // OFF_TOPIC, RESOLVED, SPAM, TOO_HEATED
	IsPinned         bool       `gorm:"default:false"`
	StateReason      string     `gorm:"size:30"` // COMPLETED, NOT_PLANNED, REOPENED
	CreatedAt        time.Time  `gorm:"index:idx_issue_repo_created,priority:2,sort:desc;index:idx_issue_repo_state_created,priority:3,sort:desc"`
	UpdatedAt        time.Time
	ClosedAt         *time.Time
	ClosedByLogin    string `gorm:"size:255"`
	// Embedding stores a TiDB VECTOR(dims) for semantic search.
	// Managed via raw SQL (InitVector); GORM skips this field.
	Embedding string `gorm:"-" json:"-"`
}

// IssueComment represents a comment on an issue.
type IssueComment struct {
	ID           uint       `gorm:"primaryKey;autoIncrement"`
	RepositoryID uint       `gorm:"index;index:idx_comment_repo_issue"`
	Repository   Repository `gorm:"foreignKey:RepositoryID"`
	IssueNumber  int        `gorm:"index:idx_comment_repo_issue"`
	Body         LargeText
	AuthorID     uint `gorm:"index"`
	Author       User `gorm:"foreignKey:AuthorID"`
	IsPinned     bool `gorm:"default:false"`
	PinnedAt     *time.Time
	// InReplyToID points to the parent comment for threaded replies (nullable for top-level comments)
	InReplyToID *uint `gorm:"index"`
	// ThreadRootID points to the root comment of the thread for efficient thread fetching (nullable for top-level comments)
	ThreadRootID *uint     `gorm:"index:idx_comment_thread"`
	CreatedAt    time.Time `gorm:"index:idx_comment_thread,priority:3,sort:asc"`
	UpdatedAt    time.Time
}

// LinkedBranch represents a git branch linked to an issue.
type LinkedBranch struct {
	ID           uint       `gorm:"primaryKey;autoIncrement"`
	RepositoryID uint       `gorm:"index"`
	Repository   Repository `gorm:"foreignKey:RepositoryID"`
	IssueID      uint       `gorm:"index"`
	Issue        Issue      `gorm:"foreignKey:IssueID"`
	BranchName   string     `gorm:"size:255"`
	CreatedAt    time.Time
}

// ReviewRequest represents a pending review request on a pull request.
type ReviewRequest struct {
	ID            uint        `gorm:"primaryKey;autoIncrement"`
	PullRequestID uint        `gorm:"index"`
	PullRequest   PullRequest `gorm:"foreignKey:PullRequestID"`
	Login         string      `gorm:"size:255;not null"` // reviewer login
	TeamSlug      string      `gorm:"size:255"`          // team slug (if team review)
	CreatedAt     time.Time
}

// PullRequestReview represents a submitted review on a pull request.
type PullRequestReview struct {
	ID            uint        `gorm:"primaryKey;autoIncrement"`
	PullRequestID uint        `gorm:"index"`
	PullRequest   PullRequest `gorm:"foreignKey:PullRequestID"`
	AuthorLogin   string      `gorm:"size:255;not null"`
	State         string      `gorm:"size:30;not null"` // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED, PENDING
	Body          LargeText
	CommitSHA     string `gorm:"size:40"`
	SubmittedAt   *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// PRReviewComment represents a line-level comment on a pull request review.
type PRReviewComment struct {
	ID                  uint               `gorm:"primaryKey;autoIncrement"`
	PullRequestReviewID *uint              `gorm:"index"`
	PullRequestReview   *PullRequestReview `gorm:"foreignKey:PullRequestReviewID"`
	PullRequestID       uint               `gorm:"index"`
	PullRequest         PullRequest        `gorm:"foreignKey:PullRequestID"`
	InReplyToID         *uint              `gorm:"index"` // parent comment ID for threaded replies
	AuthorLogin         string             `gorm:"size:255;not null"`
	Body                LargeText
	CommitID            string `gorm:"size:40"`
	Path                string `gorm:"size:1024"`
	Line                int
	OriginalLine        int
	StartLine           *int   // non-nil for multi-line comments
	Side                string `gorm:"size:10"`                // LEFT, RIGHT
	SubjectType         string `gorm:"size:10;default:'line'"` // line, file
	DiffHunk            string `gorm:"type:text"`
	IsResolved          bool   `gorm:"default:false"` // for thread resolution
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// PullRequest represents a GitHub pull request.
type PullRequest struct {
	ID                       uint       `gorm:"primaryKey;autoIncrement"`
	Number                   int        `gorm:"not null;uniqueIndex:idx_pr_repo_number"`
	RepositoryID             uint       `gorm:"index;uniqueIndex:idx_pr_repo_number"`
	Repository               Repository `gorm:"foreignKey:RepositoryID"`
	HeadRepositoryID         uint       `gorm:"index"`
	HeadRepository           Repository `gorm:"foreignKey:HeadRepositoryID"`
	Title                    string     `gorm:"size:1024;not null"`
	Body                     LargeText
	State                    string     `gorm:"size:20;default:'open';index:idx_pr_milestone_state_merged,priority:2"`
	AuthorID                 uint       `gorm:"index"`
	Author                   User       `gorm:"foreignKey:AuthorID"`
	Labels                   []Label    `gorm:"many2many:pr_labels;"`
	AssigneeLogins           string     `gorm:"size:2048"` // comma-separated user logins
	MilestoneID              *uint      `gorm:"index:idx_pr_milestone_state_merged,priority:1"`
	Milestone                *Milestone `gorm:"foreignKey:MilestoneID"`
	HeadRef                  string     `gorm:"size:255"`
	HeadSHA                  string     `gorm:"size:40"`
	BaseRef                  string     `gorm:"size:255;default:'main'"`
	BaseSHA                  string     `gorm:"size:40"`
	Draft                    bool       `gorm:"default:false"`
	MaintainerCanModify      bool       `gorm:"default:true"`
	Merged                   bool       `gorm:"default:false;index:idx_pr_milestone_state_merged,priority:3"`
	MergeCommitSHA           string     `gorm:"size:40"`
	AutoMerge                bool       `gorm:"default:false"`
	AutoMergeMethod          string     `gorm:"size:20"` // MERGE, SQUASH, REBASE
	AutoMergeCommitHeadline  string     `gorm:"size:1024"`
	AutoMergeCommitBody      LargeText
	AutoMergeAuthorEmail     string `gorm:"size:255"`
	AutoMergeExpectedHeadSHA string `gorm:"size:40"`
	AutoMergeEnabledByLogin  string `gorm:"size:255"`
	MergedByLogin            string `gorm:"size:255"`
	CreatedAt                time.Time
	UpdatedAt                time.Time
	ClosedAt                 *time.Time
	MergedAt                 *time.Time
	// CommitMessages stores concatenated commit messages for search (indexed).
	CommitMessages string `gorm:"type:text"`
	// Filenames stores comma-separated list of changed filenames for search.
	Filenames string `gorm:"type:text"`
	// Embedding stores a TiDB VECTOR(dims) for semantic search.
	// Managed via raw SQL (InitVector); GORM skips this field.
	Embedding string `gorm:"-" json:"-"`
}
