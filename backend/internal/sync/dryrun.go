package sync

import (
	"context"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

type DryRunItem struct {
	SourceItemID string `json:"sourceItemId"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	Action       string `json:"action"`
	Reason       string `json:"reason"`
}

type DryRunResult struct {
	Complete        bool         `json:"complete"`
	Error           string       `json:"error"`
	DiscoveredCount int          `json:"discoveredCount"`
	CreatedCount    int          `json:"createdCount"`
	UpdatedCount    int          `json:"updatedCount"`
	DeletedCount    int          `json:"deletedCount"`
	UnchangedCount  int          `json:"unchangedCount"`
	SkippedCount    int          `json:"skippedCount"`
	FailedCount     int          `json:"failedCount"`
	Items           []DryRunItem `json:"items"`
	Truncated       bool         `json:"truncated"`
}

// DryRun inventories the saved task and verifies changed content without
// writing destination documents, run history, ledger rows or cursors.
func (e *Engine) DryRun(ctx context.Context, taskID string) (DryRunResult, error) {
	result := DryRunResult{Items: []DryRunItem{}}
	r := &runner{e: e, ctx: ctx}
	if err := e.DB.WithContext(ctx).First(&r.task, "id = ?", taskID).Error; err != nil {
		return result, err
	}
	if _, err := r.prepareSource(); err != nil {
		return result, err
	}
	if err := r.loadLedger(); err != nil {
		return result, err
	}
	observed := map[string]DryRunItem{}
	res, err := r.src.Scan(ctx, connectors.ScanRequest{
		Credential: r.cred, Config: r.srcCfg, Filter: r.filter, Full: true,
		Log: func(string, string) {},
		Emit: func(item connectors.SourceItem) error {
			preview := r.previewItem(item)
			observed[item.ID] = preview
			return ctx.Err()
		},
	})
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	result.Complete = err == nil && res.Complete
	if err != nil {
		result.Error = err.Error()
	}
	result.DiscoveredCount = len(observed)
	if result.Complete {
		for id, row := range r.ledger {
			if _, seen := observed[id]; !seen && row.DestinationPresent {
				observed[id] = DryRunItem{id, row.SourceName, row.SourcePath, "delete", "removed from source"}
			}
		}
	}
	ids := make([]string, 0, len(observed))
	for id := range observed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		preview := observed[id]
		switch preview.Action {
		case "create":
			result.CreatedCount++
		case "update":
			result.UpdatedCount++
		case "delete":
			result.DeletedCount++
		case "unchanged":
			result.UnchangedCount++
		case "skip":
			result.SkippedCount++
		case "fail":
			result.FailedCount++
		}
		if len(result.Items) < 100 {
			result.Items = append(result.Items, preview)
		}
	}
	result.Truncated = len(observed) > len(result.Items)
	return result, nil
}

func (r *runner) previewItem(item connectors.SourceItem) DryRunItem {
	preview := DryRunItem{SourceItemID: item.ID, Name: item.Name, Path: item.Path}
	row := r.ledger[item.ID]
	desired, reason := r.inScope(item)
	if item.Deleted {
		desired, reason = false, "removed from source"
	}
	if !desired {
		return scopePreview(preview, row, reason)
	}
	tags, md := r.documentTagsAndMetadata(item)
	if row.DestinationPresent && row.SyncedFingerprint == Fingerprint(item.Revision, r.task.PolicyRevision, r.task.RetainStrategy, tags, md) {
		preview.Action = "unchanged"
		return preview
	}
	content, err := r.src.OpenContent(r.ctx, connectors.ContentRequest{Credential: r.cred, Config: r.srcCfg, Item: item})
	if errors.Is(err, connectors.ErrSkip) {
		return scopePreview(preview, row, err.Error())
	}
	if err == nil && content.Body != nil {
		// Consume the stream to test readability; never submit it to Hindsight.
		_, err = io.Copy(io.Discard, &limitedReader{r: content.Body, n: r.maxSz})
		_ = content.Body.Close()
	} else if err == nil && strings.TrimSpace(content.Text) == "" {
		return scopePreview(preview, row, "empty content")
	}
	if err != nil {
		preview.Action, preview.Reason = "fail", err.Error()
		return preview
	}
	preview.Action = "create"
	if row.DestinationPresent {
		preview.Action = "update"
	}
	return preview
}

func scopePreview(preview DryRunItem, row models.TaskItem, reason string) DryRunItem {
	preview.Action, preview.Reason = "skip", reason
	if row.DestinationPresent {
		preview.Action = "delete"
	}
	return preview
}
