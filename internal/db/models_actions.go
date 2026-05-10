package db

import "time"

// Variable represents an Actions variable.
type Variable struct {
	ID           uint   `gorm:"primaryKey;autoIncrement"`
	OwnerID      uint   `gorm:"uniqueIndex:idx_var_scope"`          // org owner ID (for org-level vars)
	RepositoryID *uint  `gorm:"uniqueIndex:idx_var_scope"`          // nil for org-level vars
	Environment  string `gorm:"size:255;uniqueIndex:idx_var_scope"` // empty for repo/org-level, set for env-level
	Name         string `gorm:"size:255;not null;uniqueIndex:idx_var_scope"`
	Value        string `gorm:"type:text"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Secret represents an Actions secret.
type Secret struct {
	ID              uint   `gorm:"primaryKey;autoIncrement"`
	OwnerID         uint   `gorm:"index"` // org owner ID (for org-level secrets)
	RepositoryID    *uint  // nil for org-level secrets
	Environment     string `gorm:"size:255"` // empty for repo/org-level, set for env-level
	Name            string `gorm:"size:255;not null"`
	Value           string `gorm:"type:text"` // plaintext secret value
	Visibility      string `gorm:"size:50"`   // "all", "private", "selected" (org-level)
	SelectedRepoIDs string `gorm:"type:text"` // comma-separated repo IDs (for selected visibility)
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Environment represents a repository Actions environment.
type Environment struct {
	ID                   uint   `gorm:"primaryKey;autoIncrement"`
	RepositoryID         uint   `gorm:"not null;uniqueIndex:idx_env_repo_name"`
	Name                 string `gorm:"size:255;not null;uniqueIndex:idx_env_repo_name"`
	ProtectionRulesJSON  string `gorm:"type:text"`
	DeploymentPolicyJSON string `gorm:"type:text"`
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// Workflow represents a GitHub Actions workflow file in a repo.
type Workflow struct {
	ID           uint   `gorm:"primaryKey;autoIncrement"`
	RepositoryID uint   `gorm:"not null;uniqueIndex:idx_wf_repo_path"`
	Name         string `gorm:"size:255"`
	Path         string `gorm:"size:512;uniqueIndex:idx_wf_repo_path"` // e.g. ".github/workflows/ci.yml"
	State        string `gorm:"size:50;default:active"`                // active, disabled_manually
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// WorkflowRun represents a single run of a workflow.
type WorkflowRun struct {
	ID           uint   `gorm:"primaryKey;autoIncrement"`
	RepositoryID uint   `gorm:"not null;index"`
	WorkflowID   uint   `gorm:"not null;index"`
	ActorID      *uint  `gorm:"index"`
	Name         string `gorm:"size:255"` // workflow name at time of run
	HeadBranch   string `gorm:"size:255"`
	HeadSHA      string `gorm:"size:64"`
	Status       string `gorm:"size:50;default:completed"` // queued, in_progress, completed
	Conclusion   string `gorm:"size:50;default:success"`   // success, failure, cancelled
	Event        string `gorm:"size:50;default:workflow_dispatch"`
	RunNumber    int
	RunAttempt   int    `gorm:"default:1"`
	LogsURL      string `gorm:"size:512"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// WorkflowRunJob represents a job within a workflow run.
type WorkflowRunJob struct {
	ID          uint   `gorm:"primaryKey;autoIncrement"`
	RunID       uint   `gorm:"not null;index"`
	Name        string `gorm:"size:255"`
	Status      string `gorm:"size:50;default:completed"`
	Conclusion  string `gorm:"size:50;default:success"`
	StartedAt   time.Time
	CompletedAt time.Time
	Logs        []byte
}

// Artifact represents a build artifact from a workflow run.
type Artifact struct {
	ID          uint   `gorm:"primaryKey;autoIncrement"`
	RunID       uint   `gorm:"not null;index"`
	Name        string `gorm:"size:255"`
	SizeInBytes int64
	Expired     bool
	Content     []byte
	ContentType string `gorm:"size:255"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ActionCache represents an Actions cache entry.
type ActionCache struct {
	ID             uint   `gorm:"primaryKey;autoIncrement"`
	RepositoryID   uint   `gorm:"not null;index"`
	Key            string `gorm:"size:512"`
	Ref            string `gorm:"size:255"`
	Version        string `gorm:"size:255"`
	SizeInBytes    int64
	CreatedAt      time.Time
	LastAccessedAt time.Time
}
