package api

import (
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

type documentCleanupProgressDTO struct {
	Phase                     string `json:"phase" enum:"idle,preparing,deleting,finalizing"`
	TotalCount                *int   `json:"totalCount"`
	CompletedCount            int    `json:"completedCount"`
	DeletedCount              int    `json:"deletedCount"`
	EstimatedRemainingSeconds *int   `json:"estimatedRemainingSeconds"`
}

type documentCleanupProgress struct {
	mu          sync.Mutex
	state       documentCleanupProgressDTO
	startedAt   time.Time
	completedAt time.Time
}

func (p *documentCleanupProgress) setTotal(total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state.TotalCount = &total
	p.state.Phase = "deleting"
	p.startedAt = time.Now()
}

func (p *documentCleanupProgress) advance(deleted bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state.CompletedCount++
	p.completedAt = time.Now()
	if deleted {
		p.state.DeletedCount++
	}
}

func (p *documentCleanupProgress) snapshot() documentCleanupProgressDTO {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.state
	// Unknown totals and the first deletion have no measured throughput yet.
	if state.TotalCount == nil || state.CompletedCount == 0 && *state.TotalCount > 0 {
		return state
	}
	remaining := *state.TotalCount - state.CompletedCount
	if remaining == 0 {
		state.Phase = "finalizing"
	} else {
		seconds := int(math.Ceil(p.completedAt.Sub(p.startedAt).Seconds() * float64(remaining) / float64(state.CompletedCount)))
		state.EstimatedRemainingSeconds = &seconds
	}
	return state
}

func (s *Server) taskDocumentCleanupProgress(w http.ResponseWriter, r *http.Request) {
	var task models.Task
	if err := s.DB.WithContext(r.Context()).Select("id").First(&task, "id = ?", r.PathValue("id")).Error; err != nil {
		s.fail(w, err)
		return
	}
	if progress, ok := s.documentCleanups.Load(task.ID); ok {
		writeJSON(w, http.StatusOK, progress.(*documentCleanupProgress).snapshot())
		return
	}
	writeJSON(w, http.StatusOK, documentCleanupProgressDTO{Phase: "idle"})
}
