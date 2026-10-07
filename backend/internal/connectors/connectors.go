// Package connectors defines the generic source connector contract and the
// normalized types shared by all sources.
package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"time"
)

// Incremental modes.
const (
	IncrementalHighWater  = "high_water_mark"
	IncrementalDeltaToken = "delta_token"
	IncrementalSyncToken  = "sync_token"
	IncrementalInventory  = "inventory"
)

// Deletion modes.
const (
	DeletionFullReconcile  = "full_reconcile"
	DeletionScanGeneration = "scan_generation"
	DeletionDelta          = "delta"
)

// Capabilities describe what a source connector supports.
type Capabilities struct {
	IncrementalMode        string `json:"incrementalMode"`
	DeletionMode           string `json:"deletionMode"`
	SupportsFiles          bool   `json:"supportsFiles"`
	SupportsOAuth          bool   `json:"supportsOAuth"`
	SupportsAdvancedFilter bool   `json:"supportsAdvancedFilter"`
}

// Field types for schema-driven forms.
const (
	FieldString   = "string"
	FieldPassword = "password"
	FieldNumber   = "number"
	FieldBoolean  = "boolean"
	FieldSelect   = "select"
	FieldURL      = "url"
	FieldTextarea = "textarea"
)

// Field changes either reset the source cursor or reconcile the existing scope.
const (
	// EffectRebaseline resets the incremental cursor and forces a full scan.
	EffectRebaseline = "rebaseline"
	// EffectReconcile forces a full reconciliation on the next run.
	EffectReconcile = "reconcile"
)

// Option is a select option.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// FieldSpec describes a configuration field.
type FieldSpec struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Secret      bool     `json:"secret"`
	Placeholder string   `json:"placeholder,omitempty"`
	Help        string   `json:"help,omitempty"`
	Default     any      `json:"default,omitempty"`
	Options     []Option `json:"options,omitempty"`
	Browse      bool     `json:"browse,omitempty"`
	// BrowseKind restricts the browse picker to items of this kind.
	BrowseKind string `json:"browseKind,omitempty"`
	// Effect is applied when the field changes on an existing task.
	Effect string `json:"-"`
}

// Filter value types.
const (
	FilterString   = "string"
	FilterBoolean  = "boolean"
	FilterNumber   = "number"
	FilterDatetime = "datetime"
	FilterEnum     = "enum"
)

// Filter operators.
const (
	OpEquals      = "equals"
	OpNotEquals   = "notEquals"
	OpContains    = "contains"
	OpIn          = "in"
	OpNotIn       = "notIn"
	OpGreaterThan = "greaterThan"
	OpLessThan    = "lessThan"
)

// FilterFieldSpec describes a filterable attribute of source items.
type FilterFieldSpec struct {
	Key       string   `json:"key"`
	Label     string   `json:"label"`
	Type      string   `json:"type"`
	Operators []string `json:"operators"`
	Options   []Option `json:"options,omitempty"`
}

// Default operator sets per filter type.
var (
	StringOps   = []string{OpEquals, OpNotEquals, OpContains, OpIn, OpNotIn}
	BooleanOps  = []string{OpEquals, OpNotEquals}
	NumberOps   = []string{OpEquals, OpNotEquals, OpGreaterThan, OpLessThan, OpIn, OpNotIn}
	DatetimeOps = []string{OpGreaterThan, OpLessThan}
	EnumOps     = []string{OpEquals, OpNotEquals, OpIn, OpNotIn}
)

// FilterRule is one structured filter condition.
type FilterRule struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    any    `json:"value"`
}

// Filter is a Task's source filter.
type Filter struct {
	Mode          string       `json:"mode"` // simple | advanced
	Rules         []FilterRule `json:"rules"`
	AdvancedQuery string       `json:"advancedQuery"`
}

// CredentialType describes a credential kind.
type CredentialType struct {
	Type        string      `json:"type"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	OAuth       bool        `json:"oauth"`
	Fields      []FieldSpec `json:"fields"`
}

// SourceInfo describes a source connector for the UI and engine.
type SourceInfo struct {
	Type           string            `json:"type"`
	Name           string            `json:"name"`
	CredentialType string            `json:"credentialType"`
	Capabilities   Capabilities      `json:"capabilities"`
	BrowseKinds    []string          `json:"browseKinds"`
	Fields         []FieldSpec       `json:"fields"`
	FilterFields   []FilterFieldSpec `json:"filterFields"`
}

// OAuthToken is a stored OAuth token.
type OAuthToken struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	TokenType    string    `json:"tokenType"`
	Expiry       time.Time `json:"expiry"`
}

// Credential is a decrypted credential handed to connectors. It must never be
// logged.
type Credential struct {
	ID      string
	Name    string
	Type    string
	Config  map[string]any    // non-sensitive values
	Secrets map[string]string // decrypted secret values
	Headers map[string]string // custom headers
	OAuth   *OAuthToken
	// AccessToken, when set, returns a currently valid OAuth access token
	// (refreshing as needed). Connectors should prefer it over OAuth for
	// requests made during long runs.
	AccessToken func(ctx context.Context) (string, error)
}

// String returns a configuration value as a string ("" when missing). Secret
// fields are looked up in Secrets.
func (c Credential) String(key string) string {
	if v, ok := c.Secrets[key]; ok {
		return v
	}
	return AsString(c.Config[key])
}

// Bool returns a configuration value as a bool.
func (c Credential) Bool(key string) bool { return AsBool(c.Config[key]) }

// LogValue prevents accidental credential logging through slog.
func (c Credential) LogValue() any { return "credential:" + c.ID }

// Item kinds.
const (
	KindPage       = "page"
	KindDocument   = "document"
	KindFile       = "file"
	KindFolder     = "folder"
	KindDataSource = "data_source"
	KindNotebook   = "notebook"
	KindBucket     = "bucket"
	KindBank       = "bank"
	KindDrive      = "drive"
	KindSite       = "site"
)

// SourceItem is the normalized description of a source object.
type SourceItem struct {
	ID         string
	Name       string
	Path       string
	Kind       string // page | document | file
	Revision   string
	ModifiedAt time.Time
	MIMEType   string
	Size       int64
	// Metadata is added to the Hindsight document (provider-specific keys).
	Metadata map[string]string
	// Tags are provider-specific tags added to the Hindsight document.
	Tags []string
	// Attributes are typed values used for filter evaluation.
	Attributes map[string]any
	// Deleted marks a delta-reported deletion (or scope exit).
	Deleted bool
	// Opaque carries connector-private data from Scan to OpenContent.
	Opaque json.RawMessage
}

// ScanRequest asks a connector to enumerate items.
type ScanRequest struct {
	Credential Credential
	Config     map[string]any
	Filter     Filter
	// Cursor is the last committed cursor; empty for a full scan.
	Cursor json.RawMessage
	// Full requests a complete inventory regardless of the cursor.
	Full bool
	// Emit is called for every observed item. Returning an error aborts.
	Emit func(SourceItem) error
	// Log records a run log line.
	Log func(level, msg string)
}

// ScanResult is returned after a scan finished without error.
type ScanResult struct {
	// Cursor to commit once all destination writes succeeded.
	Cursor json.RawMessage
	// Complete is true when every in-scope item was emitted (full inventory),
	// which enables deletion of items not observed.
	Complete bool
}

// ContentRequest asks for an item's content.
type ContentRequest struct {
	Credential Credential
	Config     map[string]any
	Item       SourceItem
}

// SourceContent is either text (Markdown) or a streamed file.
type SourceContent struct {
	// Text content, used when Body is nil.
	Text string
	// Body streams file content; the engine closes it.
	Body     io.ReadCloser
	FileName string
	MIMEType string
	Size     int64
	// Context is a human-readable description passed to Hindsight.
	Context   string
	Timestamp time.Time
}

// BrowseRequest navigates a credential's resources.
type BrowseRequest struct {
	ParentID string         `json:"parentId"`
	Kind     string         `json:"kind"`
	Config   map[string]any `json:"config,omitempty"`
}

// BrowseItem is one entry in a browse listing.
type BrowseItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Path        string `json:"path"`
	HasChildren bool   `json:"hasChildren"`
	Selectable  bool   `json:"selectable"`
}

// Breadcrumb is a navigation crumb.
type Breadcrumb struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// BrowseResult is a browse listing.
type BrowseResult struct {
	Items       []BrowseItem `json:"items"`
	ParentID    string       `json:"parentId"`
	Breadcrumbs []Breadcrumb `json:"breadcrumbs"`
}

// SourceConnector is implemented by every source.
type SourceConnector interface {
	Info() SourceInfo
	// ValidateCredential checks connectivity and returns a short description.
	ValidateCredential(ctx context.Context, cred Credential) (string, error)
	// ValidateConfig checks a task's source configuration.
	ValidateConfig(cfg map[string]any, filter Filter) error
	Browse(ctx context.Context, cred Credential, req BrowseRequest) (BrowseResult, error)
	Scan(ctx context.Context, req ScanRequest) (ScanResult, error)
	OpenContent(ctx context.Context, req ContentRequest) (SourceContent, error)
}

// ErrSkip marks an item that cannot be ingested for a permanent reason
// (unsupported, too large, empty). Such items are skipped, not failed.
var ErrSkip = errors.New("skipped")

// SkipError wraps ErrSkip with a reason.
type SkipError struct{ Reason string }

func (e *SkipError) Error() string { return "skipped: " + e.Reason }
func (e *SkipError) Unwrap() error { return ErrSkip }

// Skip returns a SkipError.
func Skip(reason string) error { return &SkipError{Reason: reason} }

var (
	sources     = map[string]SourceConnector{}
	credentials = map[string]CredentialType{}
)

// RegisterSource registers a source connector.
func RegisterSource(c SourceConnector) { sources[c.Info().Type] = c }

// RegisterCredentialType registers a credential type.
func RegisterCredentialType(t CredentialType) { credentials[t.Type] = t }

// Source returns the connector for a source type.
func Source(t string) (SourceConnector, bool) {
	c, ok := sources[t]
	return c, ok
}

// CredentialTypeOf returns a registered credential type.
func CredentialTypeOf(t string) (CredentialType, bool) {
	c, ok := credentials[t]
	return c, ok
}

// Sources lists source connectors sorted in the recommended order.
func Sources() []SourceInfo {
	out := make([]SourceInfo, 0, len(sources))
	for _, s := range sources {
		out = append(out, s.Info())
	}
	sort.Slice(out, func(i, j int) bool { return order(out[i].Type) < order(out[j].Type) })
	return out
}

// CredentialTypes lists credential types sorted in the recommended order.
func CredentialTypes() []CredentialType {
	out := make([]CredentialType, 0, len(credentials))
	for _, c := range credentials {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return order(out[i].Type) < order(out[j].Type) })
	return out
}

func order(t string) int {
	for i, v := range []string{"notion", "siyuan", "s3", "webdav", "google_drive", "onedrive", "hindsight"} {
		if v == t {
			return i
		}
	}
	return 100
}
