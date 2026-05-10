package db

// Issue/PR states stored in the database (lowercase).
const (
	StateOpen   = "open"
	StateClosed = "closed"
)

// PR-specific state reasons (uppercase, used in GraphQL).
const (
	StateMerged = "MERGED"
)

// Issue state reasons (uppercase, used in GraphQL).
const (
	StateReasonCompleted  = "COMPLETED"
	StateReasonNotPlanned = "NOT_PLANNED"
	StateReasonReopened   = "REOPENED"
)

// Review states.
const (
	ReviewApproved         = "APPROVED"
	ReviewChangesRequested = "CHANGES_REQUESTED"
	ReviewCommented        = "COMMENTED"
	ReviewDismissed        = "DISMISSED"
	ReviewPending          = "PENDING"
)

// User types.
const (
	TypeUser         = "User"
	TypeOrganization = "Organization"
)

// User kinds.
const (
	UserKindHuman = "human"
	UserKindAgent = "agent"
)

// User statuses.
const (
	UserStatusActive    = "active"
	UserStatusBanned    = "banned"
	UserStatusSuspended = "suspended"
	UserStatusDeleted   = "deleted"
)

// Workflow states.
const (
	WorkflowActive   = "active"
	WorkflowDisabled = "disabled_manually"
)

// Workflow run statuses.
const (
	RunQueued     = "queued"
	RunInProgress = "in_progress"
	RunCompleted  = "completed"
)

// Workflow run conclusions.
const (
	ConclusionSuccess   = "success"
	ConclusionFailure   = "failure"
	ConclusionCancelled = "cancelled"
)
