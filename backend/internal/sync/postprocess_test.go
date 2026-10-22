package sync_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/postprocess"
	"github.com/Nyrest/hindsight-ingestion/internal/sync"
	"github.com/google/uuid"
)

func setPostProcessing(t *testing.T, h *harness, cfg postprocess.Pipeline) {
	t.Helper()
	raw, _ := json.Marshal(cfg)
	if err := h.db.Model(&models.Task{}).Where("id = ?", h.task.ID).Update("post_process_json", string(raw)).Error; err != nil {
		t.Fatal(err)
	}
}

func TestPostProcessingBinaryAttachments(t *testing.T) {
	for _, mimeType := range []string{"application/pdf", "image/png"} {
		t.Run(mimeType, func(t *testing.T) {
			h := newHarness(t)
			setPostProcessing(t, h, postprocess.Pipeline{Enabled: true, Mode: postprocess.ModeCode,
				Code: `function process(c, input) { return {content: new Uint8Array([0, 255, 1]), fileName: "converted.pdf", mimeType: "` + mimeType + `"}; }`})
			h.setDocs(map[string]fakeDoc{"a": {rev: "1", content: "source"}})
			preview, err := h.engine.DryRun(t.Context(), h.task.ID)
			if err != nil || preview.CreatedCount != 1 || len(h.dest.docs) != 0 {
				t.Fatalf("binary dry run: %+v %v", preview, err)
			}
			expectStatus(t, h.run(""), models.RunSucceeded)
			blocks := h.dest.docs[h.documentID("a")].Blocks
			if len(blocks) != 1 || blocks[0].Source == nil {
				t.Fatalf("missing attachment: %+v", blocks)
			}
			block := blocks[0]
			if block.Source.Type != "base64" || block.Source.MediaType != mimeType || block.Source.Data != "AP8B" {
				t.Fatalf("attachment bytes or media type changed: %+v", block)
			}
			if mimeType == "application/pdf" && (block.Type != "file" || block.FileName != "converted.pdf") {
				t.Fatalf("file metadata missing: %+v", block)
			}
			if mimeType == "image/png" && (block.Type != "image" || block.FileName != "") {
				t.Fatalf("invalid image block: %+v", block)
			}
			if len(h.dest.files) != 0 {
				t.Fatal("binary output bypassed the idempotent retain path")
			}
		})
	}
}

func TestPostProcessingBinarySizeLimit(t *testing.T) {
	h := newHarness(t)
	cfg, err := h.engine.Settings.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxFileSizeMB = 1
	if err := h.engine.Settings.Put(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	setPostProcessing(t, h, postprocess.Pipeline{Enabled: true, Mode: postprocess.ModeCode,
		Code: `function process() { return new Uint8Array(1024 * 1024 + 1); }`})
	h.setDocs(map[string]fakeDoc{"a": {rev: "1", content: "source"}})
	preview, err := h.engine.DryRun(t.Context(), h.task.ID)
	if err != nil || preview.FailedCount != 1 {
		t.Fatalf("oversized binary preview: %+v %v", preview, err)
	}
	run := h.run("")
	if run.FailedCount != 1 || len(h.dest.docs) != 0 {
		t.Fatalf("oversized binary was submitted: %+v", run)
	}
}

func TestPostProcessingBinaryLostResponseRecovery(t *testing.T) {
	h, feed := caldavHarness(t)
	setPostProcessing(t, h, postprocess.Pipeline{Enabled: true, Mode: postprocess.ModeCode,
		Code: `function process(c, input) { return {content: new TextEncoder().encode(input), fileName: "event.txt", mimeType: "text/plain"}; }`})
	h.dest.loseResponse = true
	failed := h.run("")
	expectStatus(t, failed, models.RunFailed)
	var intent models.HindsightOperation
	if err := h.db.First(&intent, "type = ? AND run_id = ?", "retain_intent", failed.ID).Error; err != nil || intent.RemoteOperationID == "" {
		t.Fatal("lost binary response left no durable operation", err)
	}
	var row models.TaskItem
	if err := h.db.First(&row, "task_id = ?", h.task.ID).Error; err != nil || !row.DestinationPresent || row.LastError == "" {
		t.Fatal("unconfirmed binary document was not recorded", row, err)
	}
	for _, doc := range h.dest.docs {
		if len(doc.Blocks) != 1 || doc.Blocks[0].FileName != "event.txt" {
			t.Fatal("file attachment was not submitted", doc)
		}
		data, err := base64.StdEncoding.DecodeString(doc.Blocks[0].Source.Data)
		if err != nil || len(data) == 0 {
			t.Fatal("invalid attachment bytes", err)
		}
	}
	h.dest.loseResponse = false
	feed.replace(caldavReport(), "")
	var scans atomic.Int32
	feed.hook = func() { scans.Add(1) }
	run := models.TaskRun{ID: uuid.NewString(), TaskID: h.task.ID, Status: models.RunRunning}
	if err := h.db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := h.engine.Execute(ctx, run.ID, sync.RunOptions{}); err == nil || scans.Load() != 0 || len(h.dest.deleted) != 0 {
		t.Fatal("unsettled binary retain did not block scanning and deletion", err)
	}
	h.dest.mu.Lock()
	h.dest.opStatus[intent.RemoteOperationID] = "completed"
	h.dest.mu.Unlock()
	recovered := h.run("")
	expectStatus(t, recovered, models.RunSucceeded)
	if recovered.DeletedCount != 1 || len(h.dest.docs) != 0 {
		t.Fatal("empty calendar left an orphan binary document", recovered, h.dest.docs)
	}
}

func TestPostProcessingTransformsSkipsAndFails(t *testing.T) {
	h := newHarness(t)
	h.db.Model(&models.Task{}).Where("id = ?", h.task.ID).Updates(map[string]any{
		"file_policy_mode": "override", "file_policy_json": `{"plainText":true,"documents":true,"images":true,"audios":false}`,
		"inline_multimodal_mode": "override", "inline_multimodal_enabled": true,
	})
	setPostProcessing(t, h, postprocess.Pipeline{Enabled: true, Mode: postprocess.ModeCode, Code: `
async function process(context, input) {
  if (context.item.name === "bad") throw new Error("rejected " + context.task.sourceType);
  if (typeof input !== "string") return "file:" + new TextDecoder().decode(input) + ":" + context.document.fileName;
  console.log("processing", context.item.id);
  return input.includes("draft") ? null : input.toUpperCase();
}`})
	h.setDocs(map[string]fakeDoc{
		"a":   {rev: "1", content: "hello"},
		"d":   {rev: "1", content: "a draft"},
		"bad": {rev: "1", content: "x"},
		"f1":  {rev: "1", content: "pdf text", file: true, name: "report.pdf"},
		"img": {rev: "1", content: "unused", image: true},
	})
	r := h.run("")
	if r.FailedCount != 1 || r.SkippedCount != 1 || r.CreatedCount != 3 {
		t.Fatalf("created=%d skipped=%d failed=%d (%s)", r.CreatedCount, r.SkippedCount, r.FailedCount, r.ErrorMessage)
	}
	if got := h.dest.docs[h.documentID("a")].Content; got != "HELLO" {
		t.Fatalf("text content %q", got)
	}
	// Binary converted to text is retained as text, not uploaded as a file.
	if got := h.dest.docs[h.documentID("f1")].Content; got != "file:pdf text:report.pdf" || h.dest.files[h.documentID("f1")] != "" {
		t.Fatalf("file content %q", got)
	}
	blocks := h.dest.docs[h.documentID("img")].Blocks
	if len(blocks) != 3 || blocks[0].Text != "BEFORE" || blocks[1].Type != "image" || blocks[2].Text != "AFTER" {
		t.Fatalf("inline blocks %+v", blocks)
	}
	if _, ok := h.dest.docs[h.documentID("d")]; ok {
		t.Fatal("skipped document was retained")
	}
	var item models.TaskItem
	h.db.First(&item, "task_id = ? AND source_item_id = ?", h.task.ID, "bad")
	if !strings.Contains(item.LastError, "Error: rejected fake") {
		t.Fatalf("last error %q", item.LastError)
	}
	if !strings.Contains(r.LogJSON, "processing a") {
		t.Fatal("console output missing from the run log")
	}
	if postprocess.Shared.Idle() == 0 {
		t.Fatal("the run's VM was not returned to the pool")
	}

	h.setDocs(map[string]fakeDoc{"a": {rev: "1", content: "hello"}, "d": {rev: "1", content: "a draft"}, "n": {rev: "1", content: "new"}})
	preview, err := h.engine.DryRun(t.Context(), h.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]string{}
	for _, item := range preview.Items {
		actions[item.SourceItemID] = item.Action + ":" + item.Reason
	}
	if actions["n"] != "create:" || !strings.Contains(actions["d"], "post-processing returned no content") {
		t.Fatalf("dry run actions %v", actions)
	}
}
