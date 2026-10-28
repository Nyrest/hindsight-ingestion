package sync

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func TestWorkingSetPagesAndCleanup(t *testing.T) {
	w, err := newWorkingSet(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	dir := w.dir
	t.Cleanup(w.close)
	rows := make([]models.TaskItem, 1005)
	for i := range rows {
		id := fmt.Sprintf("%04d", i)
		rows[i] = models.TaskItem{TaskID: "task", SourceItemID: id, SourceName: "title", DestinationPresent: true}
	}
	if err := w.ledgerRows(rows); err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := w.each("ledger", func(id string, data []byte) error {
		var row models.TaskItem
		if err := json.Unmarshal(data, &row); err != nil {
			return err
		}
		if row.SourceItemID != id {
			return fmt.Errorf("incorrect page row")
		}
		first, err := w.mark(id)
		if err != nil {
			return err
		}
		if !first {
			return fmt.Errorf("first observation lost")
		}
		again, err := w.mark(id)
		if err != nil {
			return err
		}
		if again {
			return fmt.Errorf("duplicate observation accepted")
		}
		count++
		return w.put("preview", id, DryRunItem{SourceItemID: id, Action: "create"})
	}); err != nil {
		t.Fatal(err)
	}
	n, err := w.count("preview")
	if err != nil || n != 1005 || count != 1005 {
		t.Fatal(n, count, err)
	}
	w.close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("working files retained", err)
	}
}

func BenchmarkLedgerWorkingSet(b *testing.B) {
	db, err := database.Open("sqlite", filepath.Join(b.TempDir(), "ledger.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		b.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	for start := 0; start < 100000; start += 100 {
		rows := make([]models.TaskItem, 100)
		for i := range rows {
			id := fmt.Sprintf("%064d", start+i)
			rows[i] = models.TaskItem{TaskID: "review", SourceItemID: id, SourceRevision: strings.Repeat("a", 64), SourceName: "A representative document title.md", SourcePath: "/work/documents/representative/document/" + id + ".md", DestinationDocumentID: "doc-" + id, DestinationPresent: true, TargetFingerprint: strings.Repeat("b", 64), SyncedFingerprint: strings.Repeat("b", 64)}
		}
		if err := db.Create(&rows).Error; err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		r := &runner{e: &Engine{DB: db}, ctx: b.Context(), task: models.Task{ID: "review"}}
		if err := r.loadLedger(); err != nil {
			b.Fatal(err)
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		delta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
		b.ReportMetric(float64(delta), "live-heap-bytes")
		runtime.KeepAlive(r)
		r.work.close()
	}
}

func TestEmailPendingMetadataSpillsWithoutWaitingForDestination(t *testing.T) {
	w, err := newWorkingSet(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	r := &runner{ctx: t.Context(), work: w, task: models.Task{SourceType: "email"}}
	for window := 0; window < 2; window++ {
		for i := 0; i < pendingWindowSize; i++ {
			r.pending = append(r.pending, pendingItem{row: models.TaskItem{SourceItemID: fmt.Sprintf("%d-%d", window, i)}, ops: []string{"operation"}})
		}
		// No destination is installed: spilling must not poll or block an IMAP FETCH.
		if err := r.maybeAwaitPending(); err != nil {
			t.Fatal(err)
		}
		if len(r.pending) != 0 {
			t.Fatal("pending metadata retained in memory")
		}
	}
	count, err := w.count("pending")
	if err != nil || count != 2*pendingWindowSize {
		t.Fatal(count, err)
	}
}
