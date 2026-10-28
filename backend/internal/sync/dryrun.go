package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"io"
	"strings"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/shared"
)

type DryRunItem struct {
	Warnings     []string `json:"warnings,omitempty"`
	SourceItemID string   `json:"sourceItemId"`
	Name         string   `json:"name"`
	Path         string   `json:"path"`
	Action       string   `json:"action"`
	Reason       string   `json:"reason"`
}

type DryRunResult struct {
	ReleasedCount   int          `json:"releasedCount"`
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
	return e.DryRunWithOptions(ctx, taskID, RunOptions{})
}

func (e *Engine) DryRunWithOptions(ctx context.Context, taskID string, opts RunOptions) (DryRunResult, error) {
	result := DryRunResult{Items: []DryRunItem{}}
	r := &runner{e: e, ctx: ctx}
	if err := e.DB.WithContext(ctx).First(&r.task, "id = ?", taskID).Error; err != nil {
		return result, err
	}
	cfg, err := r.prepareSource()
	if err != nil {
		return result, err
	}
	if r.task.SourceType == connectors.SourceCloudflareCrawl {
		cred, err := e.Creds.Load(ctx, r.task.DestinationCredentialID)
		if err != nil {
			return result, err
		}
		key, err := shared.TargetKey(cred, r.task.DestinationBankID)
		if err != nil {
			return result, err
		}
		r.sharedDocs = &shared.Documents{DB: e.DB, TargetKey: key}
	}
	defer func() {
		if r.releaseShared != nil {
			r.releaseShared()
		}
		r.post.release()
		r.work.close()
	}()
	if err := r.loadLedger(); err != nil {
		return result, err
	}
	res, err := r.scanPreview(connectors.ScanRequest{
		TaskID: taskID, RunID: uuid.NewString(), Preview: true,
		Timezone:   cfg.Timezone,
		Credential: r.cred, Config: r.srcCfg, Filter: r.filter, Full: true,
		Log: func(string, string) {},
		Emit: func(item connectors.SourceItem) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return r.work.put("preview", item.ID, r.previewItem(item))
		},
	}, opts)
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	err = errors.Join(err, res.Error)
	if r.task.SourceType == "steam" && !res.Complete && err == nil {
		err = errors.New("Steam inventory was incomplete; deletion preview skipped")
	}
	special := r.task.SourceType == "webcal" || r.task.SourceType == "caldav" || r.task.SourceType == "email" || r.task.SourceType == "steam"
	if special {
		err = errors.Join(err, r.checkTaskRevision(e.DB.WithContext(ctx)))
		failures := 0
		if visitErr := r.work.each("preview", func(_ string, data []byte) error {
			var preview DryRunItem
			if e := json.Unmarshal(data, &preview); e != nil {
				return e
			}
			if preview.Action == "fail" {
				failures++
				if failures <= 3 {
					err = errors.Join(err, errors.New(truncate(preview.Reason, 2000)))
				}
			}
			return nil
		}); visitErr != nil {
			return result, visitErr
		}
		if failures > 3 {
			err = errors.Join(err, fmt.Errorf("and %d more failed items", failures-3))
		}
	}
	result.Complete = err == nil && res.Complete
	if err != nil {
		result.Error = err.Error()
	}
	result.DiscoveredCount, err = r.work.count("preview")
	if err != nil {
		return result, err
	}
	if result.Complete {
		if err := r.work.each("ledger", func(id string, data []byte) error {
			var row models.TaskItem
			if err := json.Unmarshal(data, &row); err != nil {
				return err
			}
			if !row.DestinationPresent {
				return nil
			}
			seen, err := r.work.get("preview", id, nil)
			if err != nil || seen {
				return err
			}
			return r.work.put("preview", id, DryRunItem{SourceItemID: id, Name: row.SourceName, Path: row.SourcePath, Action: "delete", Reason: "removed from source"})
		}); err != nil {
			return result, err
		}
	}
	total := 0
	err = r.work.each("preview", func(id string, data []byte) error {
		var preview DryRunItem
		if err := json.Unmarshal(data, &preview); err != nil {
			return err
		}
		if !result.Complete && (special || r.sharedDocs != nil) && preview.Action == "delete" {
			preview.Action, preview.Reason = "skip", "incomplete source inventory; document unchanged"
		}
		if preview.Action == "delete" && r.sharedDocs != nil {
			row, _, err := r.ledgerItem(id)
			if err != nil {
				return err
			}
			other, err := r.sharedDocs.OtherReferences(ctx, row)
			if err != nil {
				return err
			}
			if other {
				preview.Action, preview.Reason = "release", "another task still references this document"
			}
		}
		switch preview.Action {
		case "create":
			result.CreatedCount++
		case "update":
			result.UpdatedCount++
		case "delete":
			result.DeletedCount++
		case "release":
			result.ReleasedCount++
		case "unchanged":
			result.UnchangedCount++
		case "skip":
			result.SkippedCount++
		case "fail":
			result.FailedCount++
		}
		total++
		if len(result.Items) < 100 {
			result.Items = append(result.Items, preview)
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	result.Truncated = total > len(result.Items)

	return result, nil
}

func (r *runner) previewItem(item connectors.SourceItem) DryRunItem {
	preview := DryRunItem{SourceItemID: item.ID, Name: item.Name, Path: item.Path}
	row, _, err := r.ledgerItem(item.ID)
	if err != nil {
		preview.Action, preview.Reason = "fail", err.Error()
		return preview
	}
	if item.ContentError != "" {
		preview.Action, preview.Reason = "fail", item.ContentError
		return preview
	}
	desired, reason := r.inScope(item)
	if item.Deleted {
		desired, reason = false, "removed from source"
	}
	if !desired {
		return scopePreview(preview, row, reason)
	}
	if _, err := r.resolvedObservationScope(item); err != nil {
		preview.Action, preview.Reason = "fail", err.Error()
		return preview
	}
	tags, md := r.documentTagsAndMetadata(item)
	documentID := DocumentID(r.task.SourceType, r.task.ID, item.ID)
	if r.sharedDocs != nil {
		fingerprint, err := r.sharedDocs.Fingerprint(r.ctx, documentID)
		if err != nil {
			preview.Action, preview.Reason = "fail", err.Error()
			return preview
		}
		row.SyncedFingerprint = fingerprint
	}
	if row.DestinationPresent &&
		row.DestinationDocumentID == documentID &&
		!((r.task.SourceType == "webcal" || r.task.SourceType == "caldav" || r.task.SourceType == "email" || r.task.SourceType == "steam") && row.LastError != "") &&
		row.SyncedFingerprint == r.itemFingerprint(item, tags, md) && !(r.includesImages() && row.NeedsImageRetry) {
		preview.Action = "unchanged"
		return preview
	}
	content, err := r.src.OpenContent(r.ctx, r.contentRequest(item))
	if err == nil {
		content, err = r.postProcess(item, content)
	}
	preview.Warnings = content.Warnings
	if errors.Is(err, connectors.ErrSkip) {
		return scopePreview(preview, row, err.Error())
	}
	if err == nil && content.Body != nil {
		// Consume the stream to test readability; never submit it to Hindsight.
		_, err = io.Copy(io.Discard, &limitedReader{r: content.Body, n: r.maxSz})
		_ = content.Body.Close()
	} else if err == nil && strings.TrimSpace(content.Text) == "" && len(content.Blocks) == 0 {
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

func (r *runner) scanPreview(req connectors.ScanRequest, opts RunOptions) (connectors.ScanResult, error) {
	source, async := r.src.(connectors.AsyncSourceConnector)
	if !async {
		return r.src.Scan(r.ctx, req)
	}
	job, err := source.StartJob(r.ctx, req)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := source.CancelJob(cleanup, req.Credential, job); err != nil {
			r.e.Log.Warn("preview source cleanup failed", "task", r.task.ID, "error", err)
		}
	}()
	wait := func() error {
		for {
			var err error
			job, err = source.PollJob(r.ctx, req.Credential, job)
			if err != nil {
				return err
			}
			if job.Status == "completed" {
				return nil
			}
			if err := sleepCtx(r.ctx, SourcePollInterval); err != nil {
				return err
			}
		}
	}
	err = opts.waitSource(wait)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	if r.sharedDocs != nil {
		r.releaseShared, err = shared.Lock(r.ctx, r.sharedDocs.TargetKey)
		if err != nil {
			return connectors.ScanResult{}, err
		}
		if err := r.previewSharedOperations(); err != nil {
			return connectors.ScanResult{}, err
		}
	}
	return source.ReadJob(r.ctx, req, job)
}

func (r *runner) previewSharedOperations() error {
	operations, err := r.sharedOperations()
	if err != nil || len(operations) == 0 {
		return err
	}
	cred, err := r.e.Creds.Load(r.ctx, r.task.DestinationCredentialID)
	if err != nil {
		return err
	}
	destination, err := r.e.NewDestination(cred)
	if err != nil {
		return err
	}
	for _, operation := range operations {
		status, err := destination.GetOperation(r.ctx, r.task.DestinationBankID, operation.RemoteOperationID)
		if err != nil {
			return err
		}
		switch status.Status {
		case "completed", "failed", "not_found", "cancelled":
		default:
			return errors.New("shared bank has unconfirmed Hindsight writes; deletion preview is unavailable")
		}
	}
	return nil
}

func scopePreview(preview DryRunItem, row models.TaskItem, reason string) DryRunItem {
	preview.Action, preview.Reason = "skip", reason
	if row.DestinationPresent {
		preview.Action = "delete"
	}
	return preview
}
