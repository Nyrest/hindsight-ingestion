// Package models defines the GORM persistence models.
//
// JSON blobs are stored in text columns rather than native JSON types so that
// SQLite, PostgreSQL and MySQL behave identically.
package models

import (
	"time"
)

// Credential statuses.
const (
	CredentialActive         = "active"
	CredentialReauthRequired = "reauth_required"
	CredentialPendingOAuth   = "pending_oauth"
	CredentialError          = "error"
)

// Credential holds connection/authentication information only.
type Credential struct {
	ID   string `gorm:"primaryKey;size:36"`
	Name string `gorm:"size:255;not null"`
	Type string `gorm:"size:64;not null;index"`

	// ConfigJSON holds non-sensitive fields (endpoint, region, ...).
	ConfigJSON string `gorm:"type:text"`
	// EncryptedSecret holds an AES-256-GCM sealed JSON object with secrets,
	// custom header values and OAuth tokens.
	EncryptedSecret string `gorm:"type:text"`
	// HeaderNamesJSON lists custom header names in order (values are secret).
	HeaderNamesJSON string `gorm:"type:text"`
	ProxyMode       string `gorm:"size:16;not null;default:global"`
	ProxyJSON       string `gorm:"type:text"`

	Status         string `gorm:"size:32;not null;default:active"`
	StatusMessage  string `gorm:"type:text"`
	OAuthExpiresAt *time.Time
	OAuthState     string `gorm:"size:128;index"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// File policy modes.
const (
	FilePolicyGlobal   = "global"
	FilePolicyOverride = "override"
)

// Task is one source → Hindsight synchronization definition.
type Task struct {
	ID      string `gorm:"primaryKey;size:36"`
	Name    string `gorm:"size:255;not null"`
	Enabled bool   `gorm:"not null;default:true"`

	SourceType         string `gorm:"size:64;not null"`
	SourceCredentialID string `gorm:"size:36;index"`
	SourceConfigJSON   string `gorm:"type:text"`
	SourceFilterJSON   string `gorm:"type:text"`

	DestinationCredentialID string `gorm:"size:36;index"`
	DestinationBankID       string `gorm:"size:255"`

	RetainStrategy       string `gorm:"size:255"`
	ObservationScopeMode string `gorm:"size:16;not null;default:global"`
	ObservationScopeJSON string `gorm:"type:text"`

	CustomTagsJSON     string `gorm:"type:text"`
	CustomMetadataJSON string `gorm:"type:text"`

	FilePolicyMode          string `gorm:"size:16;not null;default:global"`
	FilePolicyJSON          string `gorm:"type:text"`
	InlineMultimodalMode    string `gorm:"size:16;not null;default:global"`
	InlineMultimodalEnabled bool   `gorm:"not null;default:false"`

	CronExpression string `gorm:"size:128;not null"`
	CronTimezone   string `gorm:"size:64;not null;default:UTC"`

	ConfigRevision    int64 `gorm:"not null;default:1"`
	PolicyRevision    int64 `gorm:"not null;default:1"`
	ReconcileRequired bool  `gorm:"not null;default:false"`
	DestinationLocked bool  `gorm:"not null;default:false"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TaskState is the durable incremental-sync state of a Task.
type TaskState struct {
	TaskID string `gorm:"primaryKey;size:36"`

	CommittedCursorJSON string `gorm:"type:text"`
	ScanGeneration      int64  `gorm:"not null;default:0"`

	LastStartedAt       *time.Time
	LastSuccessAt       *time.Time
	LastFullReconcileAt *time.Time
}

// TaskItem is one row of the synchronization ledger.
type TaskItem struct {
	TaskID       string `gorm:"primaryKey;size:36"`
	SourceItemID string `gorm:"primaryKey;size:512"`

	SourceRevision string `gorm:"size:512"`
	SourceName     string `gorm:"type:text"`
	SourcePath     string `gorm:"type:text"`

	// DesiredMatch is true when the item is inside the configured scope
	// (filters + file policy) at its last observation.
	DesiredMatch bool `gorm:"not null;default:false"`

	DestinationDocumentID string `gorm:"type:text"`
	// PreviousDocumentID remains tracked until an identity replacement succeeds.
	PreviousDocumentID string `gorm:"type:text"`
	DestinationPresent bool   `gorm:"not null;default:false"`

	// TargetFingerprint is what the destination should reflect; SyncedFingerprint
	// is what it reflects after the last confirmed write.
	TargetFingerprint string `gorm:"size:128"`
	SyncedFingerprint string `gorm:"size:128"`
	NeedsImageRetry   bool   `gorm:"not null;default:false"`

	LastSeenGeneration int64 `gorm:"not null;default:0;index"`
	LastSyncedAt       *time.Time
	LastError          string `gorm:"type:text"`
}

// TaskRun statuses.
const (
	RunPending           = "pending"
	RunRunning           = "running"
	RunWaitingOperations = "waiting_operations"
	RunSucceeded         = "succeeded"
	RunFailed            = "failed"
	RunInterrupted       = "interrupted"
	RunCancelled         = "cancelled"
)

// Run trigger types and sync modes.
const (
	TriggerScheduled = "scheduled"
	TriggerManual    = "manual"

	SyncIncremental = "incremental"
	SyncFull        = "full"
	SyncReingest    = "reingest"
)

// TaskRun is the execution history of a Task.
type TaskRun struct {
	ID     string `gorm:"primaryKey;size:36"`
	TaskID string `gorm:"size:36;not null;index"`

	TriggerType string `gorm:"size:16;not null"`
	Status      string `gorm:"size:32;not null;index"`
	SyncMode    string `gorm:"size:16;not null"`

	ScheduledFor *time.Time
	StartedAt    *time.Time `gorm:"index"`
	FinishedAt   *time.Time

	DiscoveredCount int `gorm:"not null;default:0"`
	CreatedCount    int `gorm:"not null;default:0"`
	UpdatedCount    int `gorm:"not null;default:0"`
	DeletedCount    int `gorm:"not null;default:0"`
	UnchangedCount  int `gorm:"not null;default:0"`
	SkippedCount    int `gorm:"not null;default:0"`
	FailedCount     int `gorm:"not null;default:0"`

	CursorBefore string `gorm:"type:text"`
	CursorAfter  string `gorm:"type:text"`

	ErrorMessage string `gorm:"type:text"`
	LogJSON      string `gorm:"type:text"`
}

// HindsightOperation tracks an asynchronous Hindsight operation of a run.
type HindsightOperation struct {
	ID                string `gorm:"primaryKey;size:36"`
	RunID             string `gorm:"size:36;not null;index"`
	RemoteOperationID string `gorm:"size:128;index"`

	Type       string `gorm:"size:32;not null"`
	Status     string `gorm:"size:32;not null"`
	RetryCount int    `gorm:"not null;default:0"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Setting is a key/value global setting.
type Setting struct {
	Key             string `gorm:"primaryKey;size:128"`
	Value           string `gorm:"type:text"`
	EncryptedSecret string `gorm:"type:text"`
}

// All returns every model for migration.
func All() []any {
	return []any{
		&Credential{}, &Task{}, &TaskState{}, &TaskItem{},
		&TaskRun{}, &HindsightOperation{}, &Setting{},
	}
}
