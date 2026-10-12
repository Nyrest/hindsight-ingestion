package email

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/emersion/go-imap/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type folderCursor struct {
	Validity uint32
	Next     imap.UID
}
type mailCursor struct {
	Version int
	Account string
	Folders map[string]folderCursor
}
type scanner struct {
	connector *Connector
	ctx       context.Context
	req       connectors.ScanRequest
	control   *session
	active    *scanSession
	account   string
	full      bool
	emitted   map[string]bool
	fallbacks int
}

func (c *Connector) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	if err := c.ValidateConfig(req.Config, req.Filter); err != nil {
		return connectors.ScanResult{}, err
	}
	if c.DB == nil || req.TaskID == "" {
		return connectors.ScanResult{}, fmt.Errorf("email scan requires task storage")
	}
	if req.RunID == "" {
		req.RunID = uuid.NewString()
	}
	release, err := c.reserve(ctx, req.Credential, 2)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	defer release()
	control, err := openSession(ctx, req.Credential)
	if err != nil {
		return connectors.ScanResult{}, redactError(err, req.Credential)
	}
	defer control.close()
	content, err := openSession(ctx, req.Credential)
	if err != nil {
		return connectors.ScanResult{}, redactError(err, req.Credential)
	}
	defer content.close()
	active := &scanSession{content: content, cache: map[string]connectors.SourceContent{}}
	c.sessions.Store(req.RunID, active)
	defer c.sessions.Delete(req.RunID)
	scan := &scanner{connector: c, ctx: ctx, req: req, control: control, active: active, account: AccountKey(req.Credential), full: req.Full, emitted: map[string]bool{}}
	result, err := scan.run()
	if err != nil {
		return connectors.ScanResult{Error: redactError(err, req.Credential)}, nil
	}
	if scan.fallbacks > 0 && req.Log != nil {
		req.Log("warn", fmt.Sprintf("%d mail occurrence(s) use UID fallback because Message-ID is missing/invalid or the body cannot be fully verified; moves may create a new document", scan.fallbacks))
	}
	return result, nil
}

func (s *scanner) run() (connectors.ScanResult, error) {
	list, err := mailboxes(s.control)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	selected, err := selectedMailboxes(list, s.req.Config)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	previous := mailCursor{}
	if len(s.req.Cursor) > 0 {
		if err := json.Unmarshal(s.req.Cursor, &previous); err != nil {
			return connectors.ScanResult{}, fmt.Errorf("invalid mail cursor: %w", err)
		}
	}
	if previous.Version != 1 || previous.Account != s.account {
		s.full = true
	}
	next := mailCursor{Version: 1, Account: s.account, Folders: map[string]folderCursor{}}
	for _, folder := range selected {
		status, err := examine(s.control, folder.Mailbox)
		if err != nil {
			return connectors.ScanResult{}, err
		}
		if status.UIDValidity == 0 || status.UIDNext == 0 {
			return connectors.ScanResult{}, fmt.Errorf("mailbox did not provide valid UIDVALIDITY and UIDNEXT")
		}
		next.Folders[folder.Mailbox] = folderCursor{Validity: status.UIDValidity, Next: status.UIDNext}
		old, ok := previous.Folders[folder.Mailbox]
		if !ok || old.Validity != status.UIDValidity || old.Next > status.UIDNext {
			s.full = true
		}
	}
	if len(previous.Folders) != len(next.Folders) {
		s.full = true
	}
	if !s.req.Preview {
		db := s.connector.DB.WithContext(s.ctx)
		if err := db.Where("task_id = ?", s.req.TaskID).Delete(&models.EmailScanOccurrence{}).Error; err != nil {
			return connectors.ScanResult{}, err
		}
		if err := db.Where("task_id = ?", s.req.TaskID).Delete(&models.EmailScanIdentity{}).Error; err != nil {
			return connectors.ScanResult{}, err
		}
	}
	for _, folder := range selected {
		upper := next.Folders[folder.Mailbox]
		status, err := examine(s.control, folder.Mailbox)
		if err != nil {
			return connectors.ScanResult{}, err
		}
		if status.UIDValidity != upper.Validity {
			return connectors.ScanResult{}, fmt.Errorf("mailbox UIDVALIDITY changed during scan")
		}
		first := imap.UID(1)
		if !s.full {
			first = previous.Folders[folder.Mailbox].Next
		}
		if first >= upper.Next {
			if s.full && status.NumMessages != 0 {
				return connectors.ScanResult{}, fmt.Errorf("nonempty mailbox has no valid UID inventory")
			}
			continue
		}
		data, err := command(s.control, func() (*imap.SearchData, error) {
			return s.control.client.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{{{Start: first, Stop: upper.Next - 1}}}}, nil).Wait()
		})
		if err != nil {
			return connectors.ScanResult{}, err
		}
		uids := data.AllUIDs()
		if s.full && status.UIDNext == upper.Next && uint32(len(uids)) != status.NumMessages {
			return connectors.ScanResult{}, fmt.Errorf("mail inventory count differs from EXAMINE; full scan must be retried")
		}
		slices.Sort(uids)
		for i, uid := range uids {
			if uid < first || uid >= upper.Next || (i > 0 && uid == uids[i-1]) {
				return connectors.ScanResult{}, fmt.Errorf("mail inventory contains an invalid or duplicate UID")
			}
		}
		for len(uids) > 0 {
			count := min(len(uids), 100)
			batch := uids[:count]
			uids = uids[count:]
			if err := s.batch(folder.Mailbox, upper.Validity, batch); err != nil {
				return connectors.ScanResult{}, err
			}
		}
		status, err = examine(s.control, folder.Mailbox)
		if err != nil {
			return connectors.ScanResult{}, err
		}
		if status.UIDValidity != upper.Validity {
			return connectors.ScanResult{}, fmt.Errorf("mailbox UIDVALIDITY changed during scan")
		}
	}
	// Validate the configured scope again before authorizing disappearance deletes.
	finalList, err := mailboxes(s.control)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	finalScope, err := selectedMailboxes(finalList, s.req.Config)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	if len(finalScope) != len(selected) {
		return connectors.ScanResult{}, fmt.Errorf("mail folder scope changed during scan")
	}
	for i := range selected {
		if selected[i].Mailbox != finalScope[i].Mailbox {
			return connectors.ScanResult{}, fmt.Errorf("mail folder scope changed during scan")
		}
		status, err := examine(s.control, selected[i].Mailbox)
		if err != nil {
			return connectors.ScanResult{}, err
		}
		if status.UIDValidity != next.Folders[selected[i].Mailbox].Validity {
			return connectors.ScanResult{}, fmt.Errorf("mailbox UIDVALIDITY changed before inventory commit")
		}
	}
	cursor, err := json.Marshal(next)
	return connectors.ScanResult{Cursor: cursor, Complete: s.full}, err
}

func (s *scanner) batch(folder string, validity uint32, uids []imap.UID) error {
	db := s.connector.DB.WithContext(s.ctx)
	folderKey := digest(folder)
	var known []models.EmailOccurrence
	if err := db.Where("task_id = ? AND account_key = ? AND folder_key = ? AND uid_validity = ? AND uid IN ?", s.req.TaskID, s.account, folderKey, validity, uids).Find(&known).Error; err != nil {
		return err
	}
	occurrences := map[imap.UID]models.EmailOccurrence{}
	ids := []string{}
	for _, row := range known {
		occurrences[imap.UID(row.UID)] = row
		ids = append(ids, row.ItemID)
	}
	var identities []models.EmailIdentity
	if len(ids) > 0 {
		if err := db.Where("task_id = ? AND item_id IN ?", s.req.TaskID, ids).Find(&identities).Error; err != nil {
			return err
		}
	}
	snapshots := map[string]mailSnapshot{}
	for _, row := range identities {
		var snapshot mailSnapshot
		if err := json.Unmarshal([]byte(row.SnapshotJSON), &snapshot); err != nil {
			return err
		}
		snapshots[row.ItemID] = snapshot
	}
	unknown := []imap.UID{}
	for _, uid := range uids {
		row, ok := occurrences[uid]
		if !ok {
			unknown = append(unknown, uid)
			continue
		}
		snapshot, ok := snapshots[row.ItemID]
		if !ok {
			return fmt.Errorf("mail occurrence has no committed identity")
		}
		if err := json.Unmarshal([]byte(row.PartsJSON), &snapshot.Parts); err != nil {
			return err
		}
		if err := s.publish(folder, validity, uid, row.ItemID, snapshot, nil); err != nil {
			return err
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	headers, err := fetchHeaders(s.control, unknown)
	if err != nil {
		return err
	}
	if s.active.folder != folder {
		status, err := examine(s.active.content, folder)
		if err != nil {
			return err
		}
		if status.UIDValidity != validity {
			return fmt.Errorf("mailbox UIDVALIDITY changed before body fetch")
		}
		s.active.folder = folder
	}
	return readBodies(s.active.content, unknown, headers, s.req.Config, func(uid imap.UID, body, hash string, omitted bool) error {
		snapshot := headers[uid]
		physicalParts := snapshot.Parts
		snapshot.ContentHash, snapshot.Omitted = hash, omitted
		id := digest("uid", s.account, folderKey, fmt.Sprint(validity), fmt.Sprint(uid))
		if snapshot.MessageID != "" && !omitted {
			id = digest("message", s.account, snapshot.MessageID, snapshot.HeaderHash, hash)
		} else {
			s.fallbacks++
		}
		// Keep the first snapshot, including fallback dates and size, when a copy moves.
		var identity models.EmailIdentity
		err = db.Where("task_id = ? AND item_id = ?", s.req.TaskID, id).Take(&identity).Error
		if err == nil {
			if err = json.Unmarshal([]byte(identity.SnapshotJSON), &snapshot); err != nil {
				return err
			}
		} else if err != gorm.ErrRecordNotFound {
			return err
		}
		snapshot.Parts = physicalParts
		content := mailContent(snapshot, body)
		if len(content.Text) > 32<<20 {
			return fmt.Errorf("mail content exceeds the 32 MiB cache limit")
		}
		if err := s.publish(folder, validity, uid, id, snapshot, &content); err != nil {
			return err
		}
		return nil
	})
}

func (s *scanner) publish(folder string, validity uint32, uid imap.UID, id string, snapshot mailSnapshot, content *connectors.SourceContent) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if !s.req.Preview {
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		identity := models.EmailScanIdentity{RunID: s.req.RunID, TaskID: s.req.TaskID, ItemID: id, AccountKey: s.account, MessageIDHash: digest(snapshot.MessageID), ContentFingerprint: snapshot.ContentHash, SnapshotJSON: string(encoded)}
		parts, _ := json.Marshal(snapshot.Parts)
		row := models.EmailScanOccurrence{RunID: s.req.RunID, TaskID: s.req.TaskID, AccountKey: s.account, FolderKey: digest(folder), FolderName: folder, UIDValidity: uint64(validity), UID: uint64(uid), ItemID: id, PartsJSON: string(parts)}
		if err := s.connector.DB.WithContext(s.ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&identity).Error; err != nil {
				return err
			}
			return tx.Create(&row).Error
		}); err != nil {
			return err
		}
	}
	if s.emitted[id] {
		return nil
	}
	s.emitted[id] = true
	item := sourceMail(snapshot, id, s.req.Config)
	item.Opaque, _ = json.Marshal(contentLocation{RunID: s.req.RunID, Folder: folder, Validity: uint64(validity), UID: uid, Snapshot: snapshot})
	if content != nil {
		s.active.cache[id] = *content
	}
	defer delete(s.active.cache, id)
	return s.req.Emit(item)
}
