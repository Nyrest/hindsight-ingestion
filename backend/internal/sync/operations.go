package sync

import (
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/Nyrest/hindsight-ingestion/internal/hindsight"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func (r *runner) flushText() error {
	if len(r.textBuf) == 0 {
		return nil
	}
	batch := r.textBuf
	r.textBuf, r.textSz = nil, 0
	items := make([]hindsight.MemoryItem, 0, len(batch))
	keys := make([]string, 0, len(batch))
	for _, j := range batch {
		tags, md := r.documentTagsAndMetadata(j.item)
		scopes, err := r.resolvedObservationScope(j.item)
		if err != nil {
			return err
		}
		mi := hindsight.MemoryItem{
			ObservationScopes: scopes,
			Blocks:            j.content.Blocks,
			Content:           j.content.Text,
			Context:           j.content.Context,
			Metadata:          md,
			DocumentID:        j.row.DestinationDocumentID,
			Tags:              tags,
			Strategy:          r.task.RetainStrategy,
			UpdateMode:        "replace",
		}
		if j.content.TimestampUnset {
			mi.Timestamp = "unset"
		} else if ts := pickTime(j.content.Timestamp, j.item.ModifiedAt); !ts.IsZero() {
			mi.Timestamp = ts.UTC().Format(time.RFC3339Nano)
		}
		items = append(items, mi)
		keys = append(keys, j.row.DestinationDocumentID+"\x00"+j.row.TargetFingerprint)
	}
	operationID := OperationID(r.run.ID, keys)
	if err := r.recordRetainIntent(batch); err != nil {
		return err
	}
	if r.sharedDocs != nil {
		if err := r.reserveShared(batch, operationID); err != nil {
			return err
		}
	}
	var retainIntent string
	if r.task.SourceType == "caldav" || r.task.SourceType == "email" || r.task.SourceType == "steam" || r.task.SourceType == "google_people" {
		retainIntent = uuid.NewString()
		if err := r.e.DB.WithContext(r.ctx).Create(&models.HindsightOperation{ID: retainIntent, RunID: r.run.ID, RemoteOperationID: operationID, Type: "retain_intent", Status: "submitted"}).Error; err != nil {
			return err
		}
	}
	ops, err := r.dest.RetainBatch(r.ctx, r.task.DestinationBankID, items, operationID)
	if err != nil {
		if r.ctx.Err() != nil {
			return r.ctx.Err()
		}
		// A destination failure is run-fatal: no point continuing.
		return fmt.Errorf("hindsight retain: %w", err)
	}
	if r.sharedDocs != nil {
		err = r.recordSharedOperations(ops, operationID)
	} else if retainIntent != "" {
		err = r.e.DB.WithContext(r.ctx).Transaction(func(tx *gorm.DB) error {
			rows := r.operationRows(ops, "retain")
			if len(rows) > 0 {
				if err := tx.Create(&rows).Error; err != nil {
					return err
				}
			}
			return tx.Model(&models.HindsightOperation{}).Where("id = ?", retainIntent).Update("status", "completed").Error
		})
	} else {
		err = r.recordOps(ops, "retain")
	}
	if err != nil {
		return err
	}
	for _, j := range batch {
		r.pending = append(r.pending, pendingItem{row: j.row, ops: ops})
	}
	r.logf("debug", "Submitted %d document(s) to Hindsight", len(batch))
	return nil
}

func (r *runner) retainFile(job retainJob, tags []string, md map[string]string) error {
	item, row, content := job.item, job.row, job.content
	defer content.Body.Close()
	if err := r.flushText(); err != nil {
		return err
	}
	name := content.FileName
	if name == "" {
		name = item.Name
	}
	mimeType := content.MIMEType
	if mimeType == "" {
		mimeType = item.MIMEType
	}
	meta := hindsight.FileMeta{
		DocumentID: row.DestinationDocumentID,
		Context:    content.Context,
		Metadata:   md,
		Tags:       tags,
		Strategy:   r.task.RetainStrategy,
	}
	if content.TimestampUnset {
		meta.Timestamp = "unset"
	} else if ts := pickTime(content.Timestamp, item.ModifiedAt); !ts.IsZero() {
		meta.Timestamp = ts.UTC().Format(time.RFC3339Nano)
	}
	body := io.Reader(content.Body)
	if r.maxSz > 0 {
		body = &limitedReader{r: content.Body, n: r.maxSz}
	}
	ops, err := r.dest.RetainFile(r.ctx, r.task.DestinationBankID, meta, name, mimeType, body)
	if err != nil {
		if r.ctx.Err() != nil {
			return r.ctx.Err()
		}
		return r.fail(row, item, fmt.Errorf("upload file: %w", err))
	}
	if err := r.recordOps(ops, "file_retain"); err != nil {
		return err
	}
	r.pending = append(r.pending, pendingItem{row: row, ops: ops})
	r.logf("debug", "Uploaded file %s", displayName(item))
	return nil
}

func (r *runner) recordOps(ops []string, typ string) error {
	if len(ops) == 0 {
		return nil
	}
	return r.e.DB.WithContext(r.ctx).Create(r.operationRows(ops, typ)).Error
}

func (r *runner) operationRows(ops []string, typ string) []models.HindsightOperation {
	rows := make([]models.HindsightOperation, 0, len(ops))
	for _, op := range ops {
		rows = append(rows, models.HindsightOperation{
			ID: uuid.NewString(), RunID: r.run.ID, RemoteOperationID: op, Type: typ, Status: "pending",
		})
	}
	return rows
}

// awaitPending polls Hindsight until every submitted operation completed,
// then marks the corresponding items as synced.
func (r *runner) awaitPending() error {
	if len(r.pending) == 0 {
		return nil
	}
	var opRows []models.HindsightOperation
	if err := r.e.DB.WithContext(r.ctx).Where("run_id = ? AND status <> ?", r.run.ID, "completed").Find(&opRows).Error; err != nil {
		return err
	}
	if len(opRows) > 0 {
		r.setStatus(models.RunWaitingOperations)
		r.logf("info", "Waiting for %d Hindsight operation(s)", len(opRows))
	}
	failedOps := map[string]string{}
	deadline := time.Now().Add(OperationWaitTimeout)
	delay := OperationPollInitial
	for len(opRows) > 0 {
		var still []models.HindsightOperation
		for _, op := range opRows {
			st, err := r.dest.GetOperation(r.ctx, r.task.DestinationBankID, op.RemoteOperationID)
			if err != nil {
				if r.ctx.Err() != nil {
					return r.ctx.Err()
				}
				r.logf("warn", "Operation status check failed: %v", err)
				still = append(still, op)
				continue
			}
			op.RetryCount = st.RetryCount
			switch st.Status {
			case "completed":
				op.Status = "completed"
			case "pending", "processing", "retrying":
				op.Status = st.Status
				still = append(still, op)
			case "failed":
				if op.RetryCount < OperationRetryLimit && st.RetryCount < OperationRetryLimit {
					if err := r.dest.RetryOperation(r.ctx, r.task.DestinationBankID, op.RemoteOperationID); err == nil {
						op.RetryCount++
						op.Status = "retrying"
						still = append(still, op)
						r.logf("warn", "Hindsight operation %s failed (%s); retrying", op.RemoteOperationID, st.ErrorMessage)
						break
					}
				}
				op.Status = "failed"
				failedOps[op.RemoteOperationID] = orEmpty(st.ErrorMessage, "operation failed")
			default: // cancelled, not_found
				op.Status = st.Status
				failedOps[op.RemoteOperationID] = "operation " + st.Status
			}
			if err := r.e.DB.WithContext(r.ctx).Model(&models.HindsightOperation{}).Where("id = ?", op.ID).
				Updates(map[string]any{"status": op.Status, "retry_count": op.RetryCount, "updated_at": time.Now().UTC()}).Error; err != nil {
				return err
			}
		}
		opRows = still
		if len(opRows) == 0 {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %d Hindsight operation(s)", len(opRows))
		}
		if err := sleepCtx(r.ctx, delay); err != nil {
			return err
		}
		delay = min(delay*2, OperationPollMax)
	}
	r.setStatus(models.RunRunning)

	now := time.Now().UTC()
	var rows []models.TaskItem
	for _, p := range r.pending {
		var failure string
		for _, op := range p.ops {
			if msg, bad := failedOps[op]; bad {
				failure = msg
			}
		}
		row := p.row
		if failure != "" {
			r.run.FailedCount++
			r.failed[row.SourceItemID] = failure
			row.LastError = truncate("Hindsight: "+failure, 2000)
			// The write may have partially landed; treat the document as
			// present so a later scope exit or disappearance deletes it
			// (deleting a missing document is a no-op). SyncedFingerprint
			// stays stale, so the item is retried.
			row.DestinationPresent = true
			r.logf("error", "Hindsight failed to ingest %s: %s", orEmpty(row.SourcePath, row.SourceName), failure)
		} else {
			if row.DestinationPresent {
				r.run.UpdatedCount++
			} else {
				r.run.CreatedCount++
			}
			row.DestinationPresent = true
			row.SyncedFingerprint = row.TargetFingerprint
			row.LastSyncedAt = &now
			row.LastError = ""
			if r.sharedDocs != nil {
				if err := r.sharedDocs.Confirm(r.ctx, row); err != nil {
					return err
				}
			}
		}
		rows = append(rows, row)
	}
	r.pending = nil
	return r.upsertRows(rows)
}
