package sync_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	stdsync "sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors/cloudflare"
	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/runner"
	ingestion "github.com/Nyrest/hindsight-ingestion/internal/sync"
	"github.com/google/uuid"
)

type crawlFixture struct {
	mu         stdsync.Mutex
	cancelCode atomic.Int32
	expired    atomic.Bool
	ready      atomic.Bool
	posts      atomic.Int32
	deletes    atomic.Int32
	records    []map[string]any
	total      int
}

func cloudHarness(t *testing.T) (*harness, *crawlFixture) {
	t.Helper()
	h := newHarness(t)
	fixture := &crawlFixture{}
	fixture.ready.Store(true)
	fixture.pages("Original")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			fixture.posts.Add(1)
			fmt.Fprint(w, `{"success":true,"result":"job"}`)
		case http.MethodDelete:
			fixture.deletes.Add(1)
			if code := fixture.cancelCode.Load(); code != 0 {
				w.WriteHeader(int(code))
				return
			}
			fmt.Fprint(w, `{"success":true,"result":{}}`)
		default:
			if fixture.expired.Load() {
				w.WriteHeader(404)
				return
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			status := "running"
			if fixture.ready.Load() {
				status = "completed"
			}
			records := fixture.records
			if r.URL.Query().Get("limit") == "1" {
				records = nil
			}
			json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "job", "status": status, "total": fixture.total, "finished": fixture.total, "records": records, "browserSecondsUsed": 1.25}})
		}
	}))
	t.Cleanup(server.Close)
	original, _ := connectors.Source(connectors.SourceCloudflareCrawl)
	connectors.RegisterSource(&cloudflare.Connector{APIBase: server.URL})
	t.Cleanup(func() { connectors.RegisterSource(original) })
	oldInterval := ingestion.SourcePollInterval
	ingestion.SourcePollInterval = time.Millisecond
	t.Cleanup(func() { ingestion.SourcePollInterval = oldInterval })
	cred := models.Credential{ID: uuid.NewString()}
	name := "Cloudflare"
	err := h.engine.Creds.Apply(&cred, credentials.Input{Name: &name, Type: connectors.SourceCloudflareCrawl, Config: map[string]any{"accountId": strings.Repeat("a", 32), "apiToken": "secret"}, HeadersSet: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&cred).Error; err != nil {
		t.Fatal(err)
	}
	h.task.SourceType, h.task.SourceCredentialID = connectors.SourceCloudflareCrawl, cred.ID
	h.task.SourceConfigJSON = `{"url":"https://example.com/docs/"}`
	if err := h.db.Save(&h.task).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.runs.Shutdown(ctx)
	})
	return h, fixture
}

func (f *crawlFixture) pages(title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.total = 1
	f.records = []map[string]any{{"url": "https://example.com/docs/page", "status": "completed", "markdown": "# Body", "metadata": map[string]any{"url": "https://EXAMPLE.com:443/docs/" + strings.Repeat("long", 150) + "?q=1#anchor", "title": title, "status": 200}}}
}

func duplicateCloudTask(t *testing.T, h *harness) models.Task {
	t.Helper()
	task := h.task
	task.ID = uuid.NewString()
	task.CustomTagsJSON = `["team:b"]`
	if err := h.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&models.TaskState{TaskID: task.ID}).Error; err != nil {
		t.Fatal(err)
	}
	return task
}

func TestCloudflareTaskIsolatedLifecycle(t *testing.T) {
	h, f := cloudHarness(t)
	a := h.task
	first := h.run(models.SyncFull)
	expectStatus(t, first, models.RunSucceeded)
	if first.CreatedCount != 1 || h.dest.retains != 1 {
		t.Fatalf("initial writes: %#v", first)
	}
	var job models.SourceJob
	if err := h.db.First(&job, "run_id = ?", first.ID).Error; err != nil || strings.Contains(job.RequestJSON+job.JobJSON, "secret") {
		t.Fatal("job snapshot leaked credentials", err)
	}
	repeat := h.run(models.SyncFull)
	expectStatus(t, repeat, models.RunSucceeded)
	if h.dest.retains != 1 {
		t.Fatal("unchanged content was re-written")
	}
	f.pages("Changed title")
	expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
	if h.dest.retains != 2 {
		t.Fatal("title change did not write")
	}
	b := duplicateCloudTask(t, h)
	h.task = b
	expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
	if len(h.dest.docs) != 2 || h.dest.retains != 3 {
		t.Fatal("tasks sharing a URL did not retain separate documents")
	}
	var rowA, rowB models.TaskItem
	if err := h.db.Where("task_id = ?", a.ID).First(&rowA).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Where("task_id = ?", b.ID).First(&rowB).Error; err != nil {
		t.Fatal(err)
	}
	if rowA.SourceItemID != rowB.SourceItemID || rowA.DestinationDocumentID == rowB.DestinationDocumentID {
		t.Fatal("same source item was not isolated by task")
	}
	for _, row := range []models.TaskItem{rowA, rowB} {
		id := row.DestinationDocumentID
		doc := h.dest.docs[id]
		tag := "team:a"
		if row.TaskID == b.ID {
			tag = "team:b"
		}
		if id != ingestion.DocumentID(connectors.SourceCloudflareCrawl, row.TaskID, row.SourceItemID) || !contains(doc.Tags, tag) {
			t.Fatalf("task identity/policy: %#v", doc)
		}
	}
	h.task = a
	expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
	if h.dest.retains != 3 {
		t.Fatal("another task's write invalidated this task's fingerprint")
	}
	f.mu.Lock()
	f.records, f.total = nil, 0
	f.mu.Unlock()
	preview, err := h.engine.DryRun(t.Context(), a.ID)
	if err != nil || !preview.Complete || preview.ReleasedCount != 0 || preview.DeletedCount != 1 {
		t.Fatalf("deletion preview: %#v %v", preview, err)
	}
	expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
	if len(h.dest.docs) != 1 || len(h.dest.deleted) != 1 || h.dest.deleted[0] != rowA.DestinationDocumentID {
		t.Fatal("removing one task affected another task document")
	}
	h.task = b
	expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
	if len(h.dest.docs) != 0 || len(h.dest.deleted) != 2 {
		t.Fatal("second task did not delete its own document")
	}
	var sharedCount int64
	h.db.Model(&models.SharedDocument{}).Count(&sharedCount)
	if sharedCount != 0 {
		t.Fatal("shared state remained after last deletion")
	}
}

func TestCloudflareFilterExitDeletesOnlyCurrentTaskDocument(t *testing.T) {
	h, _ := cloudHarness(t)
	a := h.task
	expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
	b := duplicateCloudTask(t, h)
	h.task = b
	expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
	a.SourceFilterJSON = `{"mode":"simple","expression":{"rules":[{"field":"name","operator":"equals","value":"Excluded"}]}}`
	a.ConfigRevision++
	if err := h.db.Save(&a).Error; err != nil {
		t.Fatal(err)
	}
	h.task = a
	preview, err := h.engine.DryRun(t.Context(), a.ID)
	if err != nil || preview.DeletedCount != 1 || preview.ReleasedCount != 0 {
		t.Fatal(preview, err)
	}
	expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
	if len(h.dest.docs) != 1 || len(h.dest.deleted) != 1 {
		t.Fatal("filter exit did not isolate task documents")
	}
}

func TestCloudflareSuccessfulPagesSettleBeforeReportingFailedInventory(t *testing.T) {
	h, f := cloudHarness(t)
	expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
	before := h.state().CommittedCursorJSON
	f.pages("New title")
	f.mu.Lock()
	f.records = append(f.records, map[string]any{"url": "https://example.com/docs/broken", "status": "errored", "markdown": "", "metadata": map[string]any{"status": 503}})
	f.total = 2
	f.mu.Unlock()
	run := h.run(models.SyncFull)
	expectStatus(t, run, models.RunFailed)
	if run.UpdatedCount != 1 || run.FailedCount != 1 || len(h.dest.deleted) != 0 || h.state().CommittedCursorJSON != before {
		t.Fatal("good pages were lost or failure advanced state", run)
	}
	for _, doc := range h.dest.docs {
		if doc.Metadata["title"] != "New title" {
			t.Fatal("successful page was not confirmed")
		}
	}
}

func TestCloudflareIncompleteCrawlsKeepReferencesAndWatermark(t *testing.T) {
	cases := []string{"limit", "errored", "disallowed", "empty", "oversize", "unknown"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			h, f := cloudHarness(t)
			expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
			before := h.state().CommittedCursorJSON
			f.mu.Lock()
			switch name {
			case "limit":
				h.task.SourceConfigJSON = `{"url":"https://example.com/docs/","limit":1}`
				h.db.Save(&h.task)
			case "empty":
				f.records[0]["markdown"] = ""
			case "oversize":
				f.records[0]["markdown"] = strings.Repeat("x", (5<<20)+1)
			default:
				f.records[0]["status"] = name
			}
			f.mu.Unlock()
			run := h.run(models.SyncFull)
			expectStatus(t, run, models.RunFailed)
			if h.state().CommittedCursorJSON != before || len(h.dest.deleted) != 0 || len(h.dest.docs) != 1 {
				t.Fatal("incomplete crawl advanced watermark or deleted documents")
			}
			var item models.TaskItem
			h.db.Where("task_id = ?", h.task.ID).First(&item)
			if !item.DestinationPresent {
				t.Fatal("incomplete crawl released existing reference")
			}
		})
	}
}

func cloudSnapshot(t *testing.T, h *harness) string {
	t.Helper()
	snapshot := []any{}
	for _, table := range []string{"source_jobs", "shared_documents", "task_items", "task_states", "task_runs", "hindsight_operations"} {
		var rows []map[string]any
		if err := h.db.Table(table).Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		snapshot = append(snapshot, rows)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCloudflareDryRunDoesNotPersistAndCleansTemporaryJob(t *testing.T) {
	h, f := cloudHarness(t)
	expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
	before := cloudSnapshot(t, h)
	preview, err := h.runs.DryRun(t.Context(), h.task.ID)
	if err != nil || !preview.Complete || preview.UnchangedCount != 1 {
		t.Fatalf("preview: %#v %v", preview, err)
	}
	if cloudSnapshot(t, h) != before || f.deletes.Load() != 1 {
		t.Fatal("preview persisted state or failed to clean job")
	}
	f.ready.Store(false)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := h.runs.DryRun(ctx, h.task.ID); err == nil {
		t.Fatal("cancelled preview succeeded")
	}
	if cloudSnapshot(t, h) != before || f.deletes.Load() != 2 {
		t.Fatal("cancelled preview persisted state or failed to clean job")
	}
}

func waitCloudRun(t *testing.T, h *harness, id, status string) models.TaskRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var run models.TaskRun
		if err := h.db.First(&run, "id = ?", id).Error; err != nil {
			t.Fatal(err)
		}
		if run.Status == status {
			return run
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run %s never reached %s", id, status)
	return models.TaskRun{}
}

func TestCloudflareWaitReleasesSlotButProtectsTaskAndCancel(t *testing.T) {
	h, f := cloudHarness(t)
	h.runs = runner.NewManager(h.db, h.engine, 1, h.engine.Log)
	f.ready.Store(false)
	id, err := h.runs.Start(h.task.ID, models.TriggerManual, models.SyncFull, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	waitCloudRun(t, h, id, models.RunWaitingSource)
	if _, err := h.runs.Start(h.task.ID, models.TriggerManual, models.SyncFull, nil, false); !errors.Is(err, runner.ErrAlreadyRunning) {
		t.Fatalf("task re-entry: %v", err)
	}
	if _, err := h.runs.BeginEdit(h.task.ID); !errors.Is(err, runner.ErrAlreadyRunning) {
		t.Fatalf("active task edit: %v", err)
	}
	other := duplicateCloudTask(t, h)
	other.SourceType = "fake"
	other.SourceConfigJSON = "{}"
	if err := h.db.Save(&other).Error; err != nil {
		t.Fatal(err)
	}
	otherID, err := h.runs.Start(other.ID, models.TriggerManual, models.SyncFull, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	waitCloudRun(t, h, otherID, models.RunSucceeded)
	if !h.runs.Cancel(h.task.ID) {
		t.Fatal("cancel did not find task")
	}
	waitCloudRun(t, h, id, models.RunCancelled)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, active := h.runs.Active(h.task.ID); !active {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if f.deletes.Load() != 1 {
		t.Fatal("user cancellation did not call DELETE")
	}
	release, err := h.runs.BeginEdit(h.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestCloudflareRestartReusesJobAndConfirmsOldWrites(t *testing.T) {
	for _, stage := range []string{"waiting", "waiting_crash", "writing"} {
		t.Run(stage, func(t *testing.T) {
			h, f := cloudHarness(t)
			if stage != "writing" {
				f.ready.Store(false)
			}
			id, err := h.runs.Start(h.task.ID, models.TriggerManual, models.SyncFull, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			if stage != "writing" {
				waitCloudRun(t, h, id, models.RunWaitingSource)
			} else {
				waitCloudRun(t, h, id, models.RunSucceeded)
				// Simulate a crash after confirmed writes but before the final cursor commit.
				h.db.Model(&models.TaskRun{}).Where("id = ?", id).Updates(map[string]any{"status": models.RunRunning, "finished_at": nil})
				h.db.Model(&models.SourceJob{}).Where("run_id = ?", id).Update("phase", "completed")
				h.db.Model(&models.TaskState{}).Where("task_id = ?", h.task.ID).Updates(map[string]any{"committed_cursor_json": "", "last_full_reconcile_at": nil})
				h.db.Model(&models.HindsightOperation{}).Where("run_id = ?", id).Update("status", "pending")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			h.runs.Shutdown(ctx)
			if stage == "waiting_crash" {
				h.db.Model(&models.TaskRun{}).Where("id = ?", id).Updates(map[string]any{"status": models.RunWaitingSource, "finished_at": nil})
			}
			if f.deletes.Load() != 0 {
				t.Fatal("shutdown cancelled remote job")
			}
			h.runs = runner.NewManager(h.db, h.engine, 1, h.engine.Log)
			f.ready.Store(true)
			if _, err := h.runs.RecoverInterrupted(t.Context()); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			var recovered models.TaskRun
			for time.Now().Before(deadline) {
				h.db.Where("task_id = ? AND status = ?", h.task.ID, models.RunSucceeded).Order("started_at DESC").Take(&recovered)
				if recovered.ID != "" {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if recovered.ID == "" || f.posts.Load() != 1 {
				t.Fatalf("recovery failed/recreated job: %#v posts=%d", recovered, f.posts.Load())
			}
			if stage == "waiting_crash" && recovered.ID != id {
				t.Fatal("waiting job did not resume its existing run")
			}
			if stage == "writing" {
				var old models.TaskRun
				h.db.First(&old, "id = ?", id)
				expectStatus(t, old, models.RunInterrupted)
				if h.dest.retains != 1 {
					t.Fatal("recovery repeated a confirmed retain")
				}
				var pending int64
				h.db.Model(&models.HindsightOperation{}).Where("run_id = ? AND status <> ?", id, "completed").Count(&pending)
				if pending != 0 {
					t.Fatal("recovery did not query old operations")
				}
			}
		})
	}
}

func TestCloudflareNewTablesHaveStableUniqueKeys(t *testing.T) {
	h, _ := cloudHarness(t)
	row := models.SharedDocument{TargetKey: strings.Repeat("a", 64), DocumentID: "cloudflare_crawl:" + strings.Repeat("b", 64)}
	if err := h.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&row).Error; err == nil {
		t.Fatal("shared unique key missing")
	}
	row.TargetKey = strings.Repeat("c", 64)
	if err := h.db.Create(&row).Error; err != nil {
		t.Fatal("different bank cannot share ID", err)
	}
	job := models.SourceJob{RunID: uuid.NewString(), TaskID: h.task.ID, Phase: "running", RequestJSON: "{}", JobJSON: "{}"}
	if err := h.db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&job).Error; err == nil {
		t.Fatal("source job run unique key missing")
	}
}

func TestCloudflareCancellationFailureKeepsTaskProtectedUntilRetry(t *testing.T) {
	h, f := cloudHarness(t)
	f.ready.Store(false)
	f.cancelCode.Store(401)
	id, err := h.runs.Start(h.task.ID, models.TriggerManual, models.SyncFull, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	waitCloudRun(t, h, id, models.RunWaitingSource)
	h.runs.Cancel(h.task.ID)
	waitCloudRun(t, h, id, models.RunFailed)
	var job models.SourceJob
	h.db.First(&job, "run_id = ?", id)
	if job.Phase != "cancel_failed" || job.ErrorMessage == "" {
		t.Fatal("unconfirmed cancellation not recorded", job)
	}
	if _, err := h.runs.BeginEdit(h.task.ID); !errors.Is(err, runner.ErrAlreadyRunning) {
		t.Fatal("edit allowed after failed cancellation", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, active := h.runs.Active(h.task.ID); !active {
			break
		}
		time.Sleep(time.Millisecond)
	}
	f.cancelCode.Store(0)
	if cancelled, err := h.runs.RetrySourceCancellation(t.Context(), h.task.ID); err != nil || !cancelled {
		t.Fatal("cancel retry failed", cancelled, err)
	}
	release, err := h.runs.BeginEdit(h.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if f.deletes.Load() != 2 {
		t.Fatal("missing DELETE retry")
	}
}

func TestCloudflareExpiredJobEndsRunAndNextRunCreatesNewJob(t *testing.T) {
	h, f := cloudHarness(t)
	f.expired.Store(true)
	expectStatus(t, h.run(models.SyncFull), models.RunFailed)
	f.expired.Store(false)
	expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
	if f.posts.Load() != 2 {
		t.Fatal("expired job was reused")
	}
}

func TestCloudflareDryRunProtectsUnconfirmedSharedWrites(t *testing.T) {
	h, f := cloudHarness(t)
	expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
	h.dest.mu.Lock()
	for operation := range h.dest.opStatus {
		h.dest.opStatus[operation] = "processing"
	}
	h.dest.mu.Unlock()
	if err := h.db.Model(&models.HindsightOperation{}).Where("run_id IN (?)", h.db.Model(&models.TaskRun{}).Select("id").Where("task_id = ?", h.task.ID)).Update("status", "pending").Error; err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.records, f.total = nil, 0
	f.mu.Unlock()
	before := cloudSnapshot(t, h)
	preview, err := h.engine.DryRun(t.Context(), h.task.ID)
	if err != nil || preview.Complete || preview.DeletedCount != 0 || preview.Error == "" {
		t.Fatal("pending operation preview did not protect deletion", preview, err)
	}
	if cloudSnapshot(t, h) != before {
		t.Fatal("pending operation preview changed persistent state")
	}
}

func TestCloudflareLostRetainResponsePersistsKnownOperationAndProtectsCleanup(t *testing.T) {
	h, f := cloudHarness(t)
	h.dest.loseResponse = true
	failed := h.run(models.SyncFull)
	expectStatus(t, failed, models.RunFailed)
	var operation models.HindsightOperation
	if err := h.db.Where("run_id = ?", failed.ID).Take(&operation).Error; err != nil {
		t.Fatal("lost response has no durable operation ID", err)
	}
	if operation.Status != "submitting" || !h.dest.opIDsSeen[operation.RemoteOperationID] {
		t.Fatal("wrong submission operation persisted", operation)
	}
	before := cloudSnapshot(t, h)
	preview, err := h.engine.DryRun(t.Context(), h.task.ID)
	if err != nil || preview.Complete || preview.Error == "" || preview.DeletedCount != 0 || cloudSnapshot(t, h) != before {
		t.Fatal("lost response was not protected", preview, err)
	}
	h.dest.mu.Lock()
	h.dest.loseResponse = false
	h.dest.opStatus[operation.RemoteOperationID] = "completed"
	h.dest.mu.Unlock()
	expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
	if f.posts.Load() != 2 {
		t.Fatal("sync recovery recreated its valid crawl job")
	}
	if err := h.db.First(&operation, "id = ?", operation.ID).Error; err != nil || operation.Status != "completed" {
		t.Fatal("recovery did not query the unconfirmed operation", operation, err)
	}
}
