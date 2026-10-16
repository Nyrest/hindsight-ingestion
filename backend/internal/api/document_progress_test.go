package api

import (
	"testing"
	"time"
)

func TestDocumentCleanupEstimates(t *testing.T) {
	// Boundaries: unknown total, zero documents, first request, partial success,
	// preserved shared references, and final local bookkeeping.
	p := &documentCleanupProgress{state: documentCleanupProgressDTO{Phase: "preparing"}}
	if got := p.snapshot(); got.TotalCount != nil || got.EstimatedRemainingSeconds != nil {
		t.Fatalf("preparation invented a total or estimate: %+v", got)
	}
	p.setTotal(0)
	if got := p.snapshot(); got.Phase != "finalizing" || got.EstimatedRemainingSeconds != nil {
		t.Fatalf("empty cleanup: %+v", got)
	}
	p.setTotal(4)
	if got := p.snapshot(); got.EstimatedRemainingSeconds != nil {
		t.Fatal("estimate before first completed deletion", got)
	}
	p.startedAt = time.Now().Add(-10 * time.Second)
	p.advance(true)
	p.advance(false)
	p.completedAt = p.startedAt.Add(10 * time.Second)
	got := p.snapshot()
	if got.CompletedCount != 2 || got.DeletedCount != 1 || got.EstimatedRemainingSeconds == nil || *got.EstimatedRemainingSeconds != 10 {
		t.Fatalf("shared reference counted as deleted or wrong measured estimate: %+v", got)
	}
	p.advance(true)
	p.advance(true)
	if got := p.snapshot(); got.Phase != "finalizing" || got.EstimatedRemainingSeconds != nil {
		t.Fatalf("completed cleanup still estimating remote deletion: %+v", got)
	}
}
