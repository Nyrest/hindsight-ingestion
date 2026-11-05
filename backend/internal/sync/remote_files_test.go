package sync_test

import (
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors/remotefs"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

type inventoryFiles struct {
	files   map[string]string
	listErr error
	readErr map[string]bool
	opened  []string
}

func (f *inventoryFiles) List(_ context.Context, dir string) ([]remotefs.Entry, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var entries []remotefs.Entry
	for name, body := range f.files {
		if path.Dir(name) == dir {
			entries = append(entries, remotefs.Entry{Name: path.Base(name), Regular: true, Size: int64(len(body)), Modified: time.Unix(int64(len(body)), 0)})
		}
	}
	return entries, nil
}
func (f *inventoryFiles) Open(_ context.Context, p string) (io.ReadCloser, error) {
	f.opened = append(f.opened, p)
	if f.readErr[p] {
		return nil, errors.New("remote read failed")
	}
	return io.NopCloser(strings.NewReader(f.files[p])), nil
}
func (*inventoryFiles) Close() error { return nil }

func remoteHarness(t *testing.T) (*harness, *inventoryFiles) {
	t.Helper()
	h := newHarness(t)
	files := &inventoryFiles{files: map[string]string{"/a.txt": "alpha", "/b.txt": "beta"}, readErr: map[string]bool{}}
	source := &remotefs.Source{Type: "remote_inventory_test", Name: "Remote files", Dial: func(context.Context, connectors.Credential) (remotefs.Client, error) { return files, nil }}
	// The harness credential has no protocol fields; the real protocol adapters are
	// exercised separately. This verifies the production inventory against the engine.
	connectors.RegisterSource(source)
	h.task.SourceType = source.Type
	if err := h.db.Model(&models.Task{}).Where("id = ?", h.task.ID).Update("source_type", source.Type).Error; err != nil {
		t.Fatal(err)
	}
	// Keep the connector's actual credential contract intact for the engine.
	connectors.RegisterCredentialType(connectors.CredentialType{Type: source.Type})
	if err := h.db.Model(&models.Credential{}).Where("id = ?", h.task.SourceCredentialID).Update("type", source.Type).Error; err != nil {
		t.Fatal(err)
	}
	return h, files
}

func TestRemoteInventoryImmediateDeletion(t *testing.T) {
	h, files := remoteHarness(t)
	expectStatus(t, h.run(""), models.RunSucceeded)
	files.opened = nil
	unchanged := h.run("")
	expectStatus(t, unchanged, models.RunSucceeded)
	if unchanged.UnchangedCount != 2 || len(files.opened) != 0 {
		t.Fatalf("unchanged=%d opened=%v", unchanged.UnchangedCount, files.opened)
	}
	delete(files.files, "/a.txt")
	files.files["/renamed.txt"] = "alpha"
	renamed := h.run("")
	expectStatus(t, renamed, models.RunSucceeded)
	if renamed.DeletedCount != 1 || renamed.CreatedCount != 1 {
		t.Fatalf("rename deleted=%d created=%d", renamed.DeletedCount, renamed.CreatedCount)
	}
	files.files = map[string]string{}
	empty := h.run("")
	expectStatus(t, empty, models.RunSucceeded)
	if empty.DeletedCount != 2 || len(h.dest.docs) != 0 {
		t.Fatalf("empty inventory deleted=%d remaining=%d", empty.DeletedCount, len(h.dest.docs))
	}
}

func TestRemoteInventoryFailedScanKeepsDocuments(t *testing.T) {
	h, files := remoteHarness(t)
	expectStatus(t, h.run(""), models.RunSucceeded)
	files.files = map[string]string{}
	files.listErr = errors.New("connection lost")
	failed := h.run("")
	expectStatus(t, failed, models.RunFailed)
	if failed.DeletedCount != 0 || len(h.dest.docs) != 2 {
		t.Fatalf("failed inventory deleted=%d remaining=%d", failed.DeletedCount, len(h.dest.docs))
	}
	files.listErr = nil
	empty := h.run("")
	expectStatus(t, empty, models.RunSucceeded)
	if empty.DeletedCount != 2 {
		t.Fatalf("successful empty inventory deleted=%d", empty.DeletedCount)
	}
}

func TestRemoteInventoryReadFailureDoesNotBlockConfirmedDeletion(t *testing.T) {
	h, files := remoteHarness(t)
	expectStatus(t, h.run(""), models.RunSucceeded)
	delete(files.files, "/a.txt")
	files.files["/b.txt"] = "changed"
	files.readErr["/b.txt"] = true
	failed := h.run("")
	expectStatus(t, failed, models.RunFailed)
	if failed.DeletedCount != 1 || failed.FailedCount != 1 || len(h.dest.docs) != 1 {
		t.Fatalf("deleted=%d failed=%d remaining=%d", failed.DeletedCount, failed.FailedCount, len(h.dest.docs))
	}
}

func TestRemoteInventoryFilterChangeDeletesImmediately(t *testing.T) {
	h, _ := remoteHarness(t)
	expectStatus(t, h.run(""), models.RunSucceeded)
	filter := `{"mode":"simple","expression":{"rules":[{"field":"name","operator":"equals","value":"a.txt"}]}}`
	if err := h.db.Model(&models.Task{}).Where("id = ?", h.task.ID).Updates(map[string]any{"source_filter_json": filter, "config_revision": 2, "reconcile_required": true}).Error; err != nil {
		t.Fatal(err)
	}
	filtered := h.run("")
	expectStatus(t, filtered, models.RunSucceeded)
	if filtered.DeletedCount != 1 || len(h.dest.docs) != 1 {
		t.Fatalf("deleted=%d remaining=%d", filtered.DeletedCount, len(h.dest.docs))
	}
}
