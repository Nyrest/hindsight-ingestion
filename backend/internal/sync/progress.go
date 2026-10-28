package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	stdsync "sync"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func (r *runner) setStatus(status string) {
	r.run.Status = status
	r.e.DB.Model(&models.TaskRun{}).Where("id = ?", r.run.ID).Update("status", status)
}

// finish records the terminal state of the run.
func (r *runner) finish(err error) {
	status := models.RunSucceeded
	msg := ""
	switch {
	case err == nil:
		r.logf("info", "Completed: %d discovered, %d created, %d updated, %d deleted, %d unchanged, %d skipped",
			r.run.DiscoveredCount, r.run.CreatedCount, r.run.UpdatedCount, r.run.DeletedCount, r.run.UnchangedCount, r.run.SkippedCount)
	case errors.Is(err, context.Canceled) || r.ctx.Err() != nil:
		status = models.RunCancelled
		msg = "Run cancelled"
		if cause := context.Cause(r.ctx); cause != nil && !errors.Is(cause, context.Canceled) {
			status = models.RunInterrupted
			msg = cause.Error()
		}
		r.logf("warn", "%s", msg)
	default:
		status = models.RunFailed
		msg = err.Error()
		if len(r.failed) > 0 {
			msg += ": " + summarizeFailures(r.failed)
			if r.run.FailedCount > len(r.failed) {
				msg += fmt.Sprintf("; and %d more", r.run.FailedCount-len(r.failed))
			}
		}
		r.logf("error", "Run failed: %s", msg)
	}
	finished := time.Now().UTC()
	logs, _ := json.Marshal(r.logs)
	r.e.writeTerminal(r.run.ID, map[string]any{
		"status":           status,
		"finished_at":      finished,
		"error_message":    truncate(msg, 4000),
		"discovered_count": r.run.DiscoveredCount,
		"created_count":    r.run.CreatedCount,
		"updated_count":    r.run.UpdatedCount,
		"deleted_count":    r.run.DeletedCount,
		"unchanged_count":  r.run.UnchangedCount,
		"skipped_count":    r.run.SkippedCount,
		"failed_count":     r.run.FailedCount,
		"cursor_after":     r.run.CursorAfter,
		"log_json":         string(logs),
	})
}

// tokenSource returns a function yielding a valid access token for cred,
// reloading the credential (which may have been refreshed by the scheduler)
// and refreshing it once the cached token nears expiry.
func (e *Engine) tokenSource(cred connectors.Credential) func(context.Context) (string, error) {
	var mu stdsync.Mutex
	current := *cred.OAuth
	return func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if current.AccessToken != "" && (current.Expiry.IsZero() || time.Until(current.Expiry) > 5*time.Minute) {
			return current.AccessToken, nil
		}
		fresh, err := e.Creds.Load(ctx, cred.ID)
		if err != nil {
			return "", err
		}
		if fresh, err = e.OAuth.EnsureFresh(ctx, fresh); err != nil {
			return "", err
		}
		if fresh.OAuth == nil {
			return "", fmt.Errorf("credential %q lost its OAuth token", cred.Name)
		}
		current = *fresh.OAuth
		return current.AccessToken, nil
	}
}

// writeTerminal persists a run's terminal state, retrying transient
// database errors (e.g. SQLite busy) so a run row never stays active.
func (e *Engine) writeTerminal(runID string, updates map[string]any) {
	var err error
	for attempt := range 6 {
		if err = e.DB.Model(&models.TaskRun{}).Where("id = ?", runID).Updates(updates).Error; err == nil {
			return
		}
		time.Sleep(time.Duration(200*(attempt+1)) * time.Millisecond)
	}
	e.Log.Error("failed to record run result", "run", runID, "error", err)
}

// MarkTerminal records a terminal status for a run that ended outside the
// normal finish path (load errors, panics, queue cancellation).
func (e *Engine) MarkTerminal(runID, status, msg string) {
	e.writeTerminal(runID, map[string]any{"status": status, "finished_at": time.Now().UTC(), "error_message": truncate(msg, 4000)})
}

// snapshot persists live counters (throttled) so the UI can show progress.
func (r *runner) snapshot(force bool) {
	if !force && time.Since(r.lastSnap) < 3*time.Second {
		return
	}
	r.lastSnap = time.Now()
	r.e.DB.Model(&models.TaskRun{}).Where("id = ?", r.run.ID).Updates(map[string]any{
		"discovered_count": r.run.DiscoveredCount,
		"created_count":    r.run.CreatedCount,
		"updated_count":    r.run.UpdatedCount,
		"deleted_count":    r.run.DeletedCount,
		"unchanged_count":  r.run.UnchangedCount,
		"skipped_count":    r.run.SkippedCount,
		"failed_count":     r.run.FailedCount,
	})
}

func summarizeFailures(f map[string]string) string {
	keys := slices.Collect(maps.Keys(f))
	sort.Strings(keys)
	var parts []string
	for i, k := range keys {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("and %d more", len(keys)-3))
			break
		}
		parts = append(parts, f[k])
	}
	return strings.Join(parts, "; ")
}
