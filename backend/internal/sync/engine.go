package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"strings"
	stdsync "sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/hindsight"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/observations"
	"github.com/Nyrest/hindsight-ingestion/internal/settings"
	"github.com/Nyrest/hindsight-ingestion/internal/shared"
)

// Tunables.
var (
	RetainBatchSize      = 25
	RetainBatchMaxBytes  = 4 << 20
	OperationPollInitial = 2 * time.Second
	OperationPollMax     = 30 * time.Second
	OperationRetryLimit  = 3
	OperationWaitTimeout = 6 * time.Hour
	ledgerFlushSize      = 500
)

// Reserved metadata prefix for system fields.
const ReservedMetadataPrefix = "_ingestion_"

// CredentialFresher ensures OAuth credentials are valid before use.
type CredentialFresher interface {
	EnsureFresh(ctx context.Context, cred connectors.Credential) (connectors.Credential, error)
}

// Destination is the subset of the Hindsight client the engine uses.
type Destination interface {
	RetainBatch(ctx context.Context, bankID string, items []hindsight.MemoryItem, operationID string) ([]string, error)
	RetainFile(ctx context.Context, bankID string, meta hindsight.FileMeta, fileName, mimeType string, body io.Reader) ([]string, error)
	DeleteDocument(ctx context.Context, bankID, documentID string) error
	GetOperation(ctx context.Context, bankID, opID string) (hindsight.OperationStatus, error)
	RetryOperation(ctx context.Context, bankID, opID string) error
}

// Engine executes task runs.
type Engine struct {
	DB       *gorm.DB
	Creds    *credentials.Service
	Settings *settings.Store
	OAuth    CredentialFresher
	Log      *slog.Logger
	// NewDestination builds a destination client (overridable in tests).
	NewDestination func(cred connectors.Credential) (Destination, error)
}

// RunOptions control a single execution.
type RunOptions struct {
	Mode       string // "", full, reingest
	WaitSource func(func() error) error
}

// pendingItem is a ledger update applied once its destination write
// succeeded.
type pendingItem struct {
	row models.TaskItem
	ops []string
}

// runner holds the state of one execution.
type runner struct {
	sharedDocs       *shared.Documents
	releaseShared    func()
	e                *Engine
	ctx              context.Context
	task             models.Task
	state            models.TaskState
	run              *models.TaskRun
	gen              int64
	full             bool
	reing            bool
	dest             Destination
	src              connectors.SourceConnector
	srcCfg           map[string]any
	filter           connectors.Filter
	matchFilter      func(map[string]any) bool
	policy           connectors.FilePolicy
	cred             connectors.Credential
	maxSz            int64
	observationScope observations.Scope
	inlineEnabled    bool

	baseTags []string
	customMD map[string]string

	ledger   map[string]models.TaskItem
	handled  map[string]bool
	lastSnap time.Time

	mu      stdsync.Mutex
	logs    []logLine
	seen    []models.TaskItem // observed items awaiting gen bump (flushed in batches)
	textBuf []retainJob
	textSz  int
	pending []pendingItem
	failed  map[string]string
	deletes []models.TaskItem
}

type retainJob struct {
	item    connectors.SourceItem
	row     models.TaskItem
	content connectors.SourceContent
}

type logLine struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

// Execute runs a task to completion, persisting progress on the run row.
// The run row must already exist with status running.
func (e *Engine) Execute(ctx context.Context, runID string, opts RunOptions) error {
	r := &runner{e: e, ctx: ctx, failed: map[string]string{}, handled: map[string]bool{}}
	var run models.TaskRun
	if err := e.DB.First(&run, "id = ?", runID).Error; err != nil {
		return fmt.Errorf("load run: %w", err)
	}
	r.run = &run
	defer func() {
		if r.releaseShared != nil {
			r.releaseShared()
		}
	}()
	err := r.execute(opts)
	r.finish(err)
	return err
}

func (r *runner) logf(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.mu.Lock()
	r.logs = append(r.logs, logLine{Time: time.Now().UTC(), Level: level, Message: msg})
	if len(r.logs) > 500 {
		r.logs = r.logs[len(r.logs)-500:]
	}
	r.mu.Unlock()
	attrs := []any{"task", r.task.ID, "run", r.run.ID}
	switch level {
	case "error":
		r.e.Log.Error(msg, attrs...)
	case "warn":
		r.e.Log.Warn(msg, attrs...)
	case "debug":
		r.e.Log.Debug(msg, attrs...)
	default:
		r.e.Log.Info(msg, attrs...)
	}
}

func (r *runner) execute(opts RunOptions) error {
	db := r.e.DB.WithContext(r.ctx)
	if err := db.First(&r.task, "id = ?", r.run.TaskID).Error; err != nil {
		return fmt.Errorf("load task: %w", err)
	}
	if err := db.Where(models.TaskState{TaskID: r.task.ID}).FirstOrCreate(&r.state).Error; err != nil {
		return fmt.Errorf("load task state: %w", err)
	}
	cfg, err := r.prepareSource()
	if err != nil {
		return err
	}
	destCred, err := r.e.Creds.Load(r.ctx, r.task.DestinationCredentialID)
	if err != nil {
		return fmt.Errorf("load destination credential: %w", err)
	}
	if r.dest, err = r.e.NewDestination(destCred); err != nil {
		return err
	}
	if r.task.SourceType == "email" || r.task.SourceType == "steam" {
		if err := r.fencePriorRetainOperations(); err != nil {
			return err
		}
	}
	if r.task.SourceType == connectors.SourceCloudflareCrawl {
		key, err := shared.TargetKey(destCred, r.task.DestinationBankID)
		if err != nil {
			return err
		}
		r.sharedDocs = &shared.Documents{DB: r.e.DB, TargetKey: key}
	}
	if err := r.loadLedger(); err != nil {
		return err
	}

	interval := time.Duration(cfg.FullReconcileIntervalHours) * time.Hour
	hasCursor := r.state.CommittedCursorJSON != ""
	r.reing = opts.Mode == models.SyncReingest
	switch {
	case r.reing:
		r.full = true
	case r.task.SourceType == "email" && r.state.PendingScanRunID != "":
		r.full = true
		r.logf("info", "Recovering an uncommitted email scan with full reconciliation")
	case opts.Mode == models.SyncFull:
		r.full = true
		r.logf("info", "Full reconciliation requested")
	case r.task.ReconcileRequired:
		r.full = true
		r.logf("info", "Configuration changed; performing full reconciliation")
	case (r.src.Info().Capabilities.IncrementalMode == connectors.IncrementalConditionalInventory || r.src.Info().Capabilities.IncrementalMode == connectors.IncrementalPollingInventory) && r.hasFailedItems():
		r.full = true
		r.logf("info", "Retrying failed inventory writes with a full scan")
	case !hasCursor:
		r.full = true
		r.logf("info", "No committed cursor; performing baseline full scan")
	case r.src.Info().Capabilities.IncrementalMode == connectors.IncrementalInventory:
		r.full = true
	case !cfg.IncrementalSyncEnabled:
		r.full = true
	case r.state.LastFullReconcileAt == nil || time.Since(*r.state.LastFullReconcileAt) > interval:
		r.full = true
		r.logf("info", "Periodic full reconciliation is due")
	}
	mode := models.SyncIncremental
	if r.full {
		mode = models.SyncFull
	}
	if r.reing {
		mode = models.SyncReingest
	}
	// Generations are allocated at run start so that an interrupted run can
	// never share a generation with a later complete inventory.
	r.gen = r.state.ScanGeneration + 1
	now := time.Now().UTC()
	if err := db.Model(&models.TaskState{}).Where("task_id = ?", r.task.ID).
		Updates(map[string]any{"scan_generation": r.gen, "last_started_at": now}).Error; err != nil {
		return err
	}
	r.run.SyncMode = mode
	r.run.CursorBefore = r.state.CommittedCursorJSON
	if err := db.Model(r.run).Updates(map[string]any{"sync_mode": mode, "cursor_before": r.state.CommittedCursorJSON}).Error; err != nil {
		return err
	}
	r.logf("info", "Starting %s sync (generation %d)", mode, r.gen)
	if committer, ok := r.src.(connectors.ScanCommitter); ok {
		if err := db.Model(&models.TaskState{}).Where("task_id = ?", r.task.ID).Update("pending_scan_run_id", r.run.ID).Error; err != nil {
			return err
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := committer.AbortScan(cleanup, r.run.ID); err != nil {
				r.e.Log.Error("clear connector scan staging", "run", r.run.ID, "error", err)
			}
		}()
	}

	var cursor json.RawMessage
	if hasCursor && (!r.full || r.src.Info().Capabilities.IncrementalMode == connectors.IncrementalConditionalInventory || r.src.Info().Capabilities.IncrementalMode == connectors.IncrementalPollingInventory) {
		cursor = json.RawMessage(r.state.CommittedCursorJSON)
	}

	// 1. Scan source; changes are submitted as they are discovered.
	res, err := r.scanSource(connectors.ScanRequest{
		TaskID: r.task.ID, RunID: r.run.ID, Generation: r.gen,
		Timezone:   cfg.Timezone,
		Credential: r.cred,
		Config:     r.srcCfg,
		Filter:     r.filter,
		Cursor:     cursor,
		Full:       r.full,
		Emit:       r.observe,
		Log:        func(level, msg string) { r.logf(level, "%s", msg) },
	}, opts)
	if err != nil {
		if r.task.SourceType == "webcal" || r.task.SourceType == "email" || r.task.SourceType == "steam" {
			return errors.Join(fmt.Errorf("scan source: %w", err), r.flushSeen(), r.awaitPending())
		}
		return fmt.Errorf("scan source: %w", err)
	}
	flushErr := r.flushText()
	seenErr := r.flushSeen()
	// Settle submitted writes even when the scan or final submission failed.
	pendingErr := r.awaitPending()
	if res.Error != nil {
		res.Error = fmt.Errorf("scan source: %w", res.Error)
	}
	if err := errors.Join(res.Error, flushErr, seenErr, pendingErr); err != nil {
		return err
	}
	if r.task.SourceType == "sql" && !res.Complete {
		return errors.New("SQL inventory was incomplete; deletion and state commit skipped")
	}
	if r.task.SourceType == "email" && r.full && !res.Complete {
		return errors.New("Email full inventory was incomplete; deletion and state commit skipped")
	}
	if r.task.SourceType == "steam" && !res.Complete {
		return errors.New("Steam inventory was incomplete; deletion and state commit skipped")
	}

	if (r.sharedDocs != nil || r.task.SourceType == "sql" || r.task.SourceType == "webcal" || r.task.SourceType == "email" || r.task.SourceType == "steam") && len(r.failed) > 0 {
		return fmt.Errorf("%d item(s) failed; deletion and cursor commit skipped", len(r.failed))
	}

	// 3. Reconcile deletions. Scope exits are always safe; disappearance is
	// only inferred from a complete inventory.
	complete := res.Complete
	if r.task.SourceType == "email" && complete && !r.full {
		r.full = true
		r.run.SyncMode = models.SyncFull
		if err := db.Model(r.run).Update("sync_mode", models.SyncFull).Error; err != nil {
			return err
		}
	}
	if err := r.reconcileDeletions(complete); err != nil {
		return err
	}

	if len(r.failed) > 0 {
		return fmt.Errorf("%d item(s) failed; cursor not advanced", len(r.failed))
	}

	// 4. Commit cursor last.
	finished := time.Now().UTC()
	updates := map[string]any{
		"committed_cursor_json": string(res.Cursor),
		"last_success_at":       finished,
	}
	if _, ok := r.src.(connectors.ScanCommitter); ok {
		updates["pending_scan_run_id"] = ""
	}
	if complete && (r.src.Info().Capabilities.IncrementalMode != connectors.IncrementalPollingInventory || r.full) {
		updates["last_full_reconcile_at"] = finished
	}
	err = r.e.DB.WithContext(r.ctx).Transaction(func(tx *gorm.DB) error {
		if err := r.checkTaskRevision(tx.Clauses(clause.Locking{Strength: "UPDATE"})); err != nil {
			return err
		}
		if committer, ok := r.src.(connectors.ScanCommitter); ok {
			if err := committer.CommitScan(r.ctx, r.run.ID, tx); err != nil {
				return err
			}
		}
		if err := tx.Model(&models.TaskState{}).Where("task_id = ?", r.task.ID).Updates(updates).Error; err != nil {
			return err
		}
		taskUpdates := map[string]any{"destination_locked": true}
		if complete {
			// Only clear when no config change happened during the run.
			if err := tx.Model(&models.Task{}).Where("id = ? AND config_revision = ?", r.task.ID, r.task.ConfigRevision).
				Update("reconcile_required", false).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&models.Task{}).Where("id = ?", r.task.ID).Updates(taskUpdates).Error; err != nil {
			return err
		}
		if r.sharedDocs != nil {
			return tx.Model(&models.SourceJob{}).Where("run_id = ?", r.run.ID).Update("phase", "consumed").Error
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("commit cursor: %w", err)
	}
	r.run.CursorAfter = string(res.Cursor)
	if !complete && r.full {
		r.logf("warn", "Source inventory was not complete; deletion detection skipped")
	}
	return nil
}

// observe processes one emitted source item.
func (r *runner) observe(item connectors.SourceItem) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if item.ID == "" {
		return errors.New("connector emitted an item without ID")
	}
	if r.handled[item.ID] {
		// Connectors should emit each item once (last state wins). As a
		// safety net, a later deletion is never ignored: the item is
		// queued for deletion even if an earlier emission retained it.
		if item.Deleted {
			if err := r.flushText(); err != nil {
				return err
			}
			row := r.ledger[item.ID]
			pending := false
			for _, p := range r.pending {
				if p.row.SourceItemID == item.ID {
					row = p.row
					pending = true
				}
			}
			if !pending && !row.DestinationPresent {
				return nil
			}
			row.TaskID, row.SourceItemID = r.task.ID, item.ID
			row.DestinationPresent, row.DesiredMatch, row.LastSeenGeneration = true, false, r.gen
			r.deletes = append(r.deletes, row)
			return nil
		}
		r.logf("debug", "Ignoring duplicate emission of %s", displayName(item))
		return nil
	}
	r.handled[item.ID] = true
	r.run.DiscoveredCount++

	r.snapshot(false)

	row, ok := r.ledger[item.ID]
	if !ok {
		row = models.TaskItem{TaskID: r.task.ID, SourceItemID: item.ID}
	}
	if r.sharedDocs != nil {
		row.SharedTargetKey = r.sharedDocs.TargetKey
	}
	if item.ContentError != "" {
		row.LastSeenGeneration = r.gen
		return r.fail(row, item, errors.New(item.ContentError))
	}
	row.DestinationDocumentID = DocumentID(r.task.SourceType, r.task.ID, item.ID)

	if item.Deleted {
		if !ok {
			// Deletion of an item this task never tracked (e.g. a delta feed
			// reporting changes outside the scope): nothing to do.
			return nil
		}
		row.DesiredMatch = false
		row.LastSeenGeneration = r.gen
		if row.DestinationPresent {
			r.deletes = append(r.deletes, row)
		} else if err := r.saveRow(row); err != nil {
			return err
		}
		return nil
	}

	row.SourceName = item.Name
	row.SourcePath = item.Path
	row.LastSeenGeneration = r.gen

	desired, reason := r.inScope(item)
	row.DesiredMatch = desired
	if !desired {
		r.logf("debug", "Out of scope: %s (%s)", displayName(item), reason)
		r.run.SkippedCount++
		if row.DestinationPresent {
			r.deletes = append(r.deletes, row)
			return nil
		}
		return r.queueSeen(row)
	}

	tags, md := r.documentTagsAndMetadata(item)
	if _, err := r.resolvedObservationScope(item); err != nil {
		return r.fail(row, item, err)
	}
	target := r.itemFingerprint(item, tags, md)
	row.TargetFingerprint = target
	row.SourceRevision = item.Revision
	if r.sharedDocs != nil {
		actual, err := r.sharedDocs.Fingerprint(r.ctx, row.DestinationDocumentID)
		if err != nil {
			return err
		}
		row.SyncedFingerprint = actual
	}

	if !r.reing && row.DestinationPresent && row.SyncedFingerprint == target && !((r.task.SourceType == "webcal" || r.task.SourceType == "email" || r.task.SourceType == "steam") && row.LastError != "") && !(r.full && r.includesImages() && row.NeedsImageRetry) {
		r.run.UnchangedCount++
		return r.queueSeen(row)
	}

	content, err := r.src.OpenContent(r.ctx, r.contentRequest(item))
	if err != nil {
		if errors.Is(err, connectors.ErrSkip) {
			r.logf("info", "Skipped %s: %v", displayName(item), err)
			r.run.SkippedCount++
			row.DesiredMatch = false
			if row.DestinationPresent {
				r.deletes = append(r.deletes, row)
				return nil
			}
			return r.queueSeen(row)
		}
		if r.ctx.Err() != nil {
			return r.ctx.Err()
		}
		return r.fail(row, item, err)
	}

	for _, warning := range content.Warnings {
		r.logf("warn", "%s: %s", displayName(item), warning)
	}
	row.NeedsImageRetry = r.includesImages() && len(content.Warnings) > 0
	if content.Body != nil {
		return r.retainFile(retainJob{item: item, row: row, content: content}, tags, md)
	}
	if strings.TrimSpace(content.Text) == "" && len(content.Blocks) == 0 {
		r.logf("info", "Skipped %s: empty content", displayName(item))
		r.run.SkippedCount++
		row.DesiredMatch = false
		if row.DestinationPresent {
			r.deletes = append(r.deletes, row)
			return nil
		}
		return r.queueSeen(row)
	}
	if len(content.Blocks) > 0 {
		if err := r.flushText(); err != nil {
			return err
		}
		r.textBuf = append(r.textBuf, retainJob{item: item, row: row, content: content})
		return r.flushText()
	}
	r.textBuf = append(r.textBuf, retainJob{item: item, row: row, content: content})
	r.textSz += len(content.Text)
	if len(r.textBuf) >= RetainBatchSize || r.textSz >= RetainBatchMaxBytes {
		return r.flushText()
	}
	return nil
}

func (r *runner) fail(row models.TaskItem, item connectors.SourceItem, err error) error {
	r.run.FailedCount++
	r.failed[item.ID] = err.Error()
	r.logf("error", "Failed %s: %v", displayName(item), err)
	row.LastError = truncate(err.Error(), 2000)
	return r.saveRow(row)
}

func (r *runner) inScope(item connectors.SourceItem) (bool, string) {
	if !r.matchFilter(attributes(item)) {
		return false, "filtered"
	}
	if item.Kind == connectors.KindFile {
		group := connectors.FileGroup(item.Name, item.MIMEType)
		if group == "" {
			return false, "unsupported file type"
		}
		if !r.policy.Allows(group) {
			return false, "file type " + group + " disabled"
		}
		if r.maxSz > 0 && item.Size > r.maxSz {
			return false, fmt.Sprintf("file larger than %d MB", r.maxSz>>20)
		}
	}
	return true, ""
}

func attributes(item connectors.SourceItem) map[string]any {
	attrs := map[string]any{
		"name":       item.Name,
		"path":       item.Path,
		"modifiedAt": item.ModifiedAt,
		"size":       float64(item.Size),
		"mimeType":   item.MIMEType,
	}
	if item.Kind == connectors.KindFile {
		attrs["extension"] = strings.TrimPrefix(strings.ToLower(extOf(item.Name)), ".")
		attrs["fileType"] = connectors.FileGroup(item.Name, item.MIMEType)
	}
	maps.Copy(attrs, item.Attributes)
	return attrs
}

func (r *runner) documentTagsAndMetadata(item connectors.SourceItem) ([]string, map[string]string) {
	tags := dedupe(append([]string(nil), r.baseTags...))
	md := map[string]string{}
	maps.Copy(md, r.customMD)
	for k, v := range item.Metadata {
		if !strings.HasPrefix(k, ReservedMetadataPrefix) {
			md[k] = v
		}
	}
	md["_ingestion_source_type"] = r.task.SourceType
	md["_ingestion_task_id"] = r.task.ID
	md["_ingestion_source_item_id"] = item.ID
	md["_ingestion_source_revision"] = item.Revision
	return tags, md
}

type limitedReader struct {
	r io.Reader
	n int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if l.n < 0 {
		return 0, errors.New("file exceeds the configured maximum size")
	}
	// Probe one byte beyond the limit to distinguish EOF from oversized content.
	if int64(len(p)) > l.n {
		p = p[:l.n+1]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	if l.n < 0 {
		return n, errors.New("file exceeds the configured maximum size")
	}
	return n, err
}

func displayName(item connectors.SourceItem) string {
	if item.Path != "" {
		return item.Path
	}
	if item.Name != "" {
		return item.Name
	}
	return item.ID
}

func pickTime(ts ...time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

func extOf(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 && i > strings.LastIndexByte(name, '/') {
		return name[i:]
	}
	return ""
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func orEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (r *runner) prepareSource() (settings.Settings, error) {
	cfg, err := r.e.Settings.Get(r.ctx)
	if err != nil {
		return cfg, err
	}

	src, ok := connectors.Source(r.task.SourceType)
	if !ok {
		return cfg, fmt.Errorf("unknown source type %q", r.task.SourceType)
	}
	r.src = src
	if err := json.Unmarshal([]byte(orEmpty(r.task.SourceConfigJSON, "{}")), &r.srcCfg); err != nil {
		return cfg, fmt.Errorf("invalid source config: %w", err)
	}
	if err := json.Unmarshal([]byte(orEmpty(r.task.SourceFilterJSON, "{}")), &r.filter); err != nil {
		return cfg, fmt.Errorf("invalid source filter: %w", err)
	}
	r.matchFilter, err = connectors.CompileFilter(r.filter)
	if err != nil {
		return cfg, err
	}
	r.policy = cfg.FilePolicy
	if r.task.FilePolicyMode == models.FilePolicyOverride {
		_ = json.Unmarshal([]byte(orEmpty(r.task.FilePolicyJSON, "{}")), &r.policy)
	}
	r.observationScope = cfg.ObservationScope
	if r.task.ObservationScopeMode == "override" {
		if err := json.Unmarshal([]byte(r.task.ObservationScopeJSON), &r.observationScope); err != nil {
			return cfg, err
		}
	}
	r.maxSz = int64(cfg.MaxFileSizeMB) << 20
	r.inlineEnabled = cfg.InlineMultimodalEnabled
	if r.task.InlineMultimodalMode == models.FilePolicyOverride {
		r.inlineEnabled = r.task.InlineMultimodalEnabled
	}

	srcCred, err := r.e.Creds.Load(r.ctx, r.task.SourceCredentialID)
	if err != nil {
		return cfg, fmt.Errorf("load source credential: %w", err)
	}
	if srcCred, err = r.e.OAuth.EnsureFresh(r.ctx, srcCred); err != nil {
		return cfg, err
	}
	if srcCred.OAuth != nil {
		srcCred.AccessToken = r.e.tokenSource(srcCred)
	}
	r.cred = srcCred

	r.baseTags = []string{"ingestion", "source:" + r.task.SourceType, "ingestion_task:" + r.task.ID}
	var custom []string
	_ = json.Unmarshal([]byte(orEmpty(r.task.CustomTagsJSON, "[]")), &custom)
	r.baseTags = append(r.baseTags, custom...)
	r.customMD = map[string]string{}
	_ = json.Unmarshal([]byte(orEmpty(r.task.CustomMetadataJSON, "{}")), &r.customMD)
	for k := range r.customMD {
		if strings.HasPrefix(k, ReservedMetadataPrefix) {
			delete(r.customMD, k)
		}
	}

	return cfg, nil
}
