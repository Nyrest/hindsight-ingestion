package sync_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/crypto"
	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/hindsight"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/runner"
	"github.com/Nyrest/hindsight-ingestion/internal/settings"
	"github.com/Nyrest/hindsight-ingestion/internal/sync"
)

// --- fake source ----------------------------------------------------------

type fakeDoc struct {
	rev     string
	content string
	file    bool
	name    string
}

type fakeSource struct {
	mu      stdsync.Mutex
	docs    map[string]fakeDoc
	opened  []string
	failIDs map[string]bool
	// partial makes the next scan report an incomplete inventory.
	partial         bool
	scanErr         error
	scanHook        func()
	deleteAfterScan string
}

func (f *fakeSource) Info() connectors.SourceInfo {
	return connectors.SourceInfo{
		Type: "fake", Name: "Fake", CredentialType: "fakecred",
		Capabilities: connectors.Capabilities{IncrementalMode: connectors.IncrementalHighWater, SupportsFiles: true},
		FilterFields: []connectors.FilterFieldSpec{{Key: "name", Type: connectors.FilterString, Operators: connectors.StringOps}},
	}
}
func (f *fakeSource) ValidateCredential(context.Context, connectors.Credential) (string, error) {
	return "ok", nil
}
func (f *fakeSource) ValidateConfig(map[string]any, connectors.Filter) error { return nil }
func (f *fakeSource) Browse(context.Context, connectors.Credential, connectors.BrowseRequest) (connectors.BrowseResult, error) {
	return connectors.BrowseResult{}, nil
}

// Scan emits every doc on a full scan, and only docs whose revision is
// greater than the cursor on incremental scans.
func (f *fakeSource) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	f.mu.Lock()
	docs := map[string]fakeDoc{}
	for k, v := range f.docs {
		docs[k] = v
	}
	partial, scanErr, hook := f.partial, f.scanErr, f.scanHook
	deleteAfterScan := f.deleteAfterScan
	f.partial = false
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if scanErr != nil {
		return connectors.ScanResult{}, scanErr
	}
	var cur struct{ High string }
	if !req.Full && len(req.Cursor) > 0 {
		_ = json.Unmarshal(req.Cursor, &cur)
	}
	ids := make([]string, 0, len(docs))
	for id := range docs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	high := cur.High
	for i, id := range ids {
		d := docs[id]
		if d.rev > high {
			high = d.rev
		}
		if cur.High != "" && d.rev <= cur.High {
			continue
		}
		if partial && i == len(ids)-1 {
			break
		}
		kind := connectors.KindPage
		if d.file {
			kind = connectors.KindFile
		}
		name := d.name
		if name == "" {
			name = id
		}
		if err := req.Emit(connectors.SourceItem{ID: id, Name: name, Path: "/" + name, Kind: kind, Revision: d.rev, Size: int64(len(d.content))}); err != nil {
			return connectors.ScanResult{}, err
		}
	}
	if deleteAfterScan != "" {
		if err := req.Emit(connectors.SourceItem{ID: deleteAfterScan, Deleted: true}); err != nil {
			return connectors.ScanResult{}, err
		}
	}
	b, _ := json.Marshal(struct{ High string }{high})
	return connectors.ScanResult{Cursor: b, Complete: req.Full && !partial}, nil
}

func (f *fakeSource) OpenContent(ctx context.Context, req connectors.ContentRequest) (connectors.SourceContent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, req.Item.ID)
	if f.failIDs[req.Item.ID] {
		return connectors.SourceContent{}, errors.New("boom")
	}
	d := f.docs[req.Item.ID]
	if d.file {
		return connectors.SourceContent{Body: io.NopCloser(strings.NewReader(d.content)), FileName: req.Item.Name}, nil
	}
	return connectors.SourceContent{Text: d.content}, nil
}

// --- fake hindsight -------------------------------------------------------

type fakeDest struct {
	mu        stdsync.Mutex
	docs      map[string]hindsight.MemoryItem
	files     map[string]string
	deleted   []string
	opStatus  map[string]string
	opSeq     int
	retains   int
	failNext  bool
	failOps   bool
	opIDsSeen map[string]bool
}

func newFakeDest() *fakeDest {
	return &fakeDest{docs: map[string]hindsight.MemoryItem{}, files: map[string]string{}, opStatus: map[string]string{}, opIDsSeen: map[string]bool{}}
}

func (d *fakeDest) RetainBatch(ctx context.Context, bank string, items []hindsight.MemoryItem, opID string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failNext {
		d.failNext = false
		return nil, errors.New("hindsight down")
	}
	d.retains++
	d.opIDsSeen[opID] = true
	for _, it := range items {
		d.docs[it.DocumentID] = it
	}
	d.opSeq++
	id := fmt.Sprintf("op-%d", d.opSeq)
	d.opStatus[id] = "completed"
	if d.failOps {
		d.opStatus[id] = "failed"
	}
	return []string{id}, nil
}

func (d *fakeDest) RetainFile(ctx context.Context, bank string, meta hindsight.FileMeta, name, mime string, body io.Reader) ([]string, error) {
	b, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.files[meta.DocumentID] = string(b)
	d.docs[meta.DocumentID] = hindsight.MemoryItem{DocumentID: meta.DocumentID, Content: string(b), Tags: meta.Tags, Metadata: meta.Metadata}
	d.opSeq++
	id := fmt.Sprintf("op-%d", d.opSeq)
	d.opStatus[id] = "completed"
	return []string{id}, nil
}

func (d *fakeDest) DeleteDocument(ctx context.Context, bank, docID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.docs, docID)
	d.deleted = append(d.deleted, docID)
	return nil
}

func (d *fakeDest) GetOperation(ctx context.Context, bank, op string) (hindsight.OperationStatus, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return hindsight.OperationStatus{Status: d.opStatus[op], RetryCount: 99}, nil
}

func (d *fakeDest) RetryOperation(ctx context.Context, bank, op string) error { return nil }

// --- harness --------------------------------------------------------------

type noOAuth struct{}

func (noOAuth) EnsureFresh(_ context.Context, c connectors.Credential) (connectors.Credential, error) {
	return c, nil
}

type harness struct {
	t      *testing.T
	db     *gorm.DB
	src    *fakeSource
	dest   *fakeDest
	engine *sync.Engine
	runs   *runner.Manager
	task   models.Task
}

var registerOnce stdsync.Once
var sharedSource = &fakeSource{}

func newHarness(t *testing.T) *harness {
	t.Helper()
	registerOnce.Do(func() {
		connectors.RegisterSource(sharedSource)
		connectors.RegisterCredentialType(connectors.CredentialType{Type: "fakecred"})
		connectors.RegisterCredentialType(hindsight.CredentialSpec)
	})
	sharedSource.mu.Lock()
	sharedSource.docs = map[string]fakeDoc{}
	sharedSource.opened = nil
	sharedSource.failIDs = map[string]bool{}
	sharedSource.partial = false
	sharedSource.scanErr = nil
	sharedSource.scanHook = nil
	sharedSource.deleteAfterScan = ""
	sharedSource.mu.Unlock()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// SYNC_TEST_DB_TYPE/SYNC_TEST_DB_DSN run the engine suite on PostgreSQL
	// or MySQL; the default is a fresh SQLite file.
	dbType, dsn := os.Getenv("SYNC_TEST_DB_TYPE"), os.Getenv("SYNC_TEST_DB_DSN")
	if dbType == "" {
		dbType, dsn = "sqlite", filepath.Join(t.TempDir(), "test.db")
	}
	db, err := database.Open(dbType, dsn, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	})
	cipher, _ := crypto.New(bytes.Repeat([]byte{9}, 32))
	creds := credentials.NewService(db, cipher)
	mk := func(typ string, cfg map[string]any) string {
		m := models.Credential{ID: uuid.NewString()}
		name := typ
		if err := creds.Apply(&m, credentials.Input{Name: &name, Type: typ, Config: cfg, HeadersSet: true}, true); err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
		return m.ID
	}
	srcCred := mk("fakecred", map[string]any{})
	dstCred := mk("hindsight", map[string]any{"baseUrl": "http://hindsight.invalid"})

	dest := newFakeDest()
	engine := &sync.Engine{
		DB: db, Creds: creds, Settings: settings.NewStore(db), OAuth: noOAuth{}, Log: log,
		NewDestination: func(connectors.Credential) (sync.Destination, error) { return dest, nil },
	}
	sync.OperationPollInitial = time.Millisecond
	task := models.Task{
		ID: uuid.NewString(), Name: "t", Enabled: true, SourceType: "fake", SourceCredentialID: srcCred,
		SourceConfigJSON: "{}", SourceFilterJSON: `{"mode":"simple","rules":[]}`,
		DestinationCredentialID: dstCred, DestinationBankID: "bank", CustomTagsJSON: `["team:a"]`,
		CustomMetadataJSON: `{"project":"x","_ingestion_task_id":"spoof"}`, FilePolicyMode: "global",
		FilePolicyJSON: "{}", CronExpression: "*/15 * * * *", CronTimezone: "UTC", ConfigRevision: 1, PolicyRevision: 1,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	db.Create(&models.TaskState{TaskID: task.ID})
	return &harness{t: t, db: db, src: sharedSource, dest: dest, engine: engine,
		runs: runner.NewManager(db, engine, 2, log), task: task}
}

func (h *harness) setDocs(docs map[string]fakeDoc) {
	h.src.mu.Lock()
	h.src.docs = docs
	h.src.opened = nil
	h.src.mu.Unlock()
}

func (h *harness) run(mode string) models.TaskRun {
	h.t.Helper()
	id, err := h.runs.Start(h.task.ID, models.TriggerManual, mode, nil, true)
	if err != nil {
		h.t.Fatal(err)
	}
	var r models.TaskRun
	h.db.First(&r, "id = ?", id)
	return r
}

func (h *harness) state() models.TaskState {
	var s models.TaskState
	h.db.First(&s, "task_id = ?", h.task.ID)
	return s
}

func (h *harness) opened() []string {
	h.src.mu.Lock()
	defer h.src.mu.Unlock()
	return append([]string(nil), h.src.opened...)
}

func expectStatus(t *testing.T, r models.TaskRun, status string) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("run status = %s (%s), want %s", r.Status, r.ErrorMessage, status)
	}
}

// --- tests ----------------------------------------------------------------

func TestIncrementalLifecycle(t *testing.T) {
	h := newHarness(t)
	h.setDocs(map[string]fakeDoc{"a": {rev: "1", content: "alpha"}, "b": {rev: "1", content: "beta"}})

	r := h.run("")
	expectStatus(t, r, models.RunSucceeded)
	if r.SyncMode != models.SyncFull || r.CreatedCount != 2 {
		t.Fatalf("baseline: mode=%s created=%d", r.SyncMode, r.CreatedCount)
	}
	docA := sync.DocumentID(h.task.ID, "a")
	got := h.dest.docs[docA]
	if got.Content != "alpha" {
		t.Fatalf("doc a = %+v", got)
	}
	// Tags and metadata.
	for _, tag := range []string{"source:fake", "ingestion_task:" + h.task.ID, "team:a"} {
		if !contains(got.Tags, tag) {
			t.Errorf("missing tag %s in %v", tag, got.Tags)
		}
	}
	if got.Metadata["_ingestion_task_id"] != h.task.ID || got.Metadata["_ingestion_source_item_id"] != "a" ||
		got.Metadata["project"] != "x" || got.Metadata["_ingestion_source_revision"] != "1" {
		t.Errorf("metadata = %v", got.Metadata)
	}
	if h.state().CommittedCursorJSON == "" || h.state().LastFullReconcileAt == nil {
		t.Fatal("cursor not committed after baseline")
	}

	// Incremental: only b changed; a must not be fetched.
	h.setDocs(map[string]fakeDoc{"a": {rev: "1", content: "alpha"}, "b": {rev: "2", content: "beta2"}})
	r = h.run("")
	expectStatus(t, r, models.RunSucceeded)
	if r.SyncMode != models.SyncIncremental || r.UpdatedCount != 1 || r.DiscoveredCount != 1 {
		t.Fatalf("incremental: mode=%s updated=%d discovered=%d", r.SyncMode, r.UpdatedCount, r.DiscoveredCount)
	}
	if o := h.opened(); len(o) != 1 || o[0] != "b" {
		t.Fatalf("opened %v, want [b]", o)
	}

	// Full scan with no changes re-uploads nothing.
	h.setDocs(map[string]fakeDoc{"a": {rev: "1", content: "alpha"}, "b": {rev: "2", content: "beta2"}})
	retains := h.dest.retains
	r = h.run(models.SyncFull)
	expectStatus(t, r, models.RunSucceeded)
	if r.UnchangedCount != 2 || h.dest.retains != retains || len(h.opened()) != 0 {
		t.Fatalf("full no-op: unchanged=%d retains %d->%d opened=%v", r.UnchangedCount, retains, h.dest.retains, h.opened())
	}

	// Deletion detected on full scan.
	h.setDocs(map[string]fakeDoc{"b": {rev: "2", content: "beta2"}})
	r = h.run(models.SyncFull)
	expectStatus(t, r, models.RunSucceeded)
	if r.DeletedCount != 1 || !contains(h.dest.deleted, docA) {
		t.Fatalf("deletion: deleted=%d %v", r.DeletedCount, h.dest.deleted)
	}
	var n int64
	h.db.Model(&models.TaskItem{}).Where("task_id = ?", h.task.ID).Count(&n)
	if n != 1 {
		t.Fatalf("ledger rows = %d, want 1", n)
	}
}

func TestPartialInventoryNeverDeletes(t *testing.T) {
	h := newHarness(t)
	h.setDocs(map[string]fakeDoc{"a": {rev: "1", content: "a"}, "b": {rev: "1", content: "b"}})
	expectStatus(t, h.run(""), models.RunSucceeded)

	h.src.mu.Lock()
	h.src.partial = true
	h.src.mu.Unlock()
	r := h.run(models.SyncFull)
	expectStatus(t, r, models.RunSucceeded)
	if r.DeletedCount != 0 || len(h.dest.deleted) != 0 {
		t.Fatalf("partial scan deleted %v", h.dest.deleted)
	}
}

func TestCursorNotCommittedOnFailure(t *testing.T) {
	h := newHarness(t)
	h.setDocs(map[string]fakeDoc{"a": {rev: "1", content: "a"}})
	expectStatus(t, h.run(""), models.RunSucceeded)
	before := h.state().CommittedCursorJSON

	// Destination failure.
	h.setDocs(map[string]fakeDoc{"a": {rev: "2", content: "a2"}})
	h.dest.failNext = true
	expectStatus(t, h.run(""), models.RunFailed)
	if h.state().CommittedCursorJSON != before {
		t.Fatal("cursor advanced despite destination failure")
	}

	// Source content failure.
	h.src.mu.Lock()
	h.src.failIDs["a"] = true
	h.src.mu.Unlock()
	expectStatus(t, h.run(""), models.RunFailed)
	if h.state().CommittedCursorJSON != before {
		t.Fatal("cursor advanced despite item failure")
	}

	// Hindsight async operation failure.
	h.src.mu.Lock()
	h.src.failIDs = map[string]bool{}
	h.src.mu.Unlock()
	h.dest.failOps = true
	expectStatus(t, h.run(""), models.RunFailed)
	if h.state().CommittedCursorJSON != before {
		t.Fatal("cursor advanced despite failed Hindsight operation")
	}
	var item models.TaskItem
	h.db.First(&item, "task_id = ? AND source_item_id = ?", h.task.ID, "a")
	if item.SyncedFingerprint == item.TargetFingerprint {
		t.Fatal("item marked synced although operation failed")
	}

	// Recovery: retry from the old cursor succeeds.
	h.dest.failOps = false
	r := h.run("")
	expectStatus(t, r, models.RunSucceeded)
	if h.dest.docs[sync.DocumentID(h.task.ID, "a")].Content != "a2" {
		t.Fatal("retry did not ingest the change")
	}
}

func TestPolicyChangeReRetains(t *testing.T) {
	h := newHarness(t)
	h.setDocs(map[string]fakeDoc{"a": {rev: "1", content: "a"}, "b": {rev: "1", content: "b"}})
	expectStatus(t, h.run(""), models.RunSucceeded)

	h.db.Model(&models.Task{}).Where("id = ?", h.task.ID).Updates(map[string]any{
		"custom_tags_json": `["team:b"]`, "policy_revision": 2, "reconcile_required": true,
	})
	r := h.run("")
	expectStatus(t, r, models.RunSucceeded)
	if r.UpdatedCount != 2 {
		t.Fatalf("policy change updated %d, want 2", r.UpdatedCount)
	}
	if !contains(h.dest.docs[sync.DocumentID(h.task.ID, "a")].Tags, "team:b") {
		t.Fatal("new tag not applied")
	}
	var task models.Task
	h.db.First(&task, "id = ?", h.task.ID)
	if task.ReconcileRequired {
		t.Fatal("reconcile flag not cleared")
	}
}

func TestFilterExitDeletes(t *testing.T) {
	h := newHarness(t)
	h.setDocs(map[string]fakeDoc{"keep-1": {rev: "1", content: "k"}, "drop-1": {rev: "1", content: "d"}})
	expectStatus(t, h.run(""), models.RunSucceeded)

	h.db.Model(&models.Task{}).Where("id = ?", h.task.ID).Updates(map[string]any{
		"source_filter_json": `{"mode":"simple","rules":[{"field":"name","operator":"contains","value":"keep"}]}`,
		"reconcile_required": true,
	})
	r := h.run("")
	expectStatus(t, r, models.RunSucceeded)
	if r.DeletedCount != 1 || !contains(h.dest.deleted, sync.DocumentID(h.task.ID, "drop-1")) {
		t.Fatalf("filter exit: deleted=%d %v", r.DeletedCount, h.dest.deleted)
	}
}

func TestFilePolicyAndStreaming(t *testing.T) {
	h := newHarness(t)
	h.setDocs(map[string]fakeDoc{
		"f1": {rev: "1", content: "hello pdf", file: true, name: "report.pdf"},
		"f2": {rev: "1", content: "png bytes", file: true, name: "image.png"},
		"f3": {rev: "1", content: "bin", file: true, name: "blob.xyz"},
	})
	r := h.run("")
	expectStatus(t, r, models.RunSucceeded)
	if r.CreatedCount != 1 || r.SkippedCount != 2 {
		t.Fatalf("created=%d skipped=%d", r.CreatedCount, r.SkippedCount)
	}
	if h.dest.files[sync.DocumentID(h.task.ID, "f1")] != "hello pdf" {
		t.Fatal("file content not streamed")
	}

	// Enable images via override → image ingested.
	h.db.Model(&models.Task{}).Where("id = ?", h.task.ID).Updates(map[string]any{
		"file_policy_mode": "override", "file_policy_json": `{"plainText":true,"documents":false,"images":true,"audios":false}`,
		"reconcile_required": true,
	})
	r = h.run("")
	expectStatus(t, r, models.RunSucceeded)
	// Documents disabled → previously synced PDF removed; PNG added.
	if r.CreatedCount != 1 || r.DeletedCount != 1 {
		t.Fatalf("override: created=%d deleted=%d", r.CreatedCount, r.DeletedCount)
	}
}

func TestDuplicateRunRejected(t *testing.T) {
	h := newHarness(t)
	h.setDocs(map[string]fakeDoc{"a": {rev: "1", content: "a"}})
	release := make(chan struct{})
	started := make(chan struct{})
	h.src.mu.Lock()
	h.src.scanHook = func() { close(started); <-release }
	h.src.mu.Unlock()

	if _, err := h.runs.Start(h.task.ID, models.TriggerManual, "", nil, false); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := h.runs.Start(h.task.ID, models.TriggerScheduled, "", nil, false); !errors.Is(err, runner.ErrAlreadyRunning) {
		t.Fatalf("second start err = %v, want ErrAlreadyRunning", err)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, busy := h.runs.Active(h.task.ID); !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCrashRecoveryKeepsCursor(t *testing.T) {
	h := newHarness(t)
	h.setDocs(map[string]fakeDoc{"a": {rev: "1", content: "a"}})
	expectStatus(t, h.run(""), models.RunSucceeded)
	before := h.state().CommittedCursorJSON

	// Simulate a run left "running" by a crashed process.
	now := time.Now()
	stale := models.TaskRun{ID: uuid.NewString(), TaskID: h.task.ID, TriggerType: "scheduled", Status: models.RunRunning, SyncMode: "incremental", StartedAt: &now}
	h.db.Create(&stale)
	n, err := h.runs.RecoverInterrupted(context.Background())
	// Other tests may share the database, so require at least this run.
	if err != nil || n < 1 {
		t.Fatalf("recover = %d, %v", n, err)
	}
	h.db.First(&stale, "id = ?", stale.ID)
	if stale.Status != models.RunInterrupted {
		t.Fatalf("stale status = %s", stale.Status)
	}
	if h.state().CommittedCursorJSON != before {
		t.Fatal("recovery changed the cursor")
	}
	expectStatus(t, h.run(""), models.RunSucceeded)
}

func TestStaleActiveRunDoesNotBlockStart(t *testing.T) {
	h := newHarness(t)
	h.setDocs(map[string]fakeDoc{"a": {rev: "1", content: "a"}})
	expectStatus(t, h.run(""), models.RunSucceeded)
	before := h.state().CommittedCursorJSON

	stale := models.TaskRun{ID: uuid.NewString(), TaskID: h.task.ID, Status: models.RunRunning}
	if err := h.db.Create(&stale).Error; err != nil {
		t.Fatal(err)
	}
	expectStatus(t, h.run(""), models.RunSucceeded)
	if err := h.db.First(&stale, "id = ?", stale.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stale.Status != models.RunInterrupted || stale.FinishedAt == nil {
		t.Fatalf("stale run not closed: %+v", stale)
	}
	if h.state().CommittedCursorJSON != before {
		t.Fatal("stale run recovery changed the cursor")
	}
}

func TestDuplicateDeletionAfterUpsert(t *testing.T) {
	for _, count := range []int{1, sync.RetainBatchSize} {
		t.Run(fmt.Sprintf("batch-%d", count), func(t *testing.T) {
			h := newHarness(t)
			docs := map[string]fakeDoc{}
			for i := 0; i < count; i++ {
				docs[fmt.Sprintf("doc-%02d", i)] = fakeDoc{rev: "1", content: "text"}
			}
			h.setDocs(docs)
			h.src.deleteAfterScan = "doc-00"
			r := h.run("")
			expectStatus(t, r, models.RunSucceeded)
			id := sync.DocumentID(h.task.ID, "doc-00")
			if _, present := h.dest.docs[id]; present || r.DeletedCount != 1 {
				t.Fatalf("duplicate deletion left document present=%v, deleted=%d", present, r.DeletedCount)
			}
			var item models.TaskItem
			if err := h.db.First(&item, "task_id = ? AND source_item_id = ?", h.task.ID, "doc-00").Error; err != nil {
				t.Fatal(err)
			}
			if item.DestinationPresent || item.DesiredMatch || item.SyncedFingerprint != "" {
				t.Fatalf("deleted item still marked synced: %+v", item)
			}
		})
	}
}

func TestShutdownInterruptsRun(t *testing.T) {
	h := newHarness(t)
	h.setDocs(map[string]fakeDoc{"a": {rev: "1", content: "a"}})
	started := make(chan struct{})
	h.src.mu.Lock()
	h.src.scanHook = func() { close(started); time.Sleep(200 * time.Millisecond) }
	h.src.mu.Unlock()
	id, err := h.runs.Start(h.task.ID, models.TriggerManual, "", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h.runs.Shutdown(ctx)
	var r models.TaskRun
	h.db.First(&r, "id = ?", id)
	if r.Status != models.RunInterrupted {
		t.Fatalf("status = %s (%s), want interrupted", r.Status, r.ErrorMessage)
	}
	if h.state().CommittedCursorJSON != "" {
		t.Fatal("cursor committed by interrupted run")
	}
}

func TestDeterministicIDs(t *testing.T) {
	a := sync.DocumentID("task", "item")
	if a != sync.DocumentID("task", "item") || a == sync.DocumentID("task", "item2") || a == sync.DocumentID("task2", "item") {
		t.Fatal("document IDs not deterministic/unique")
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
