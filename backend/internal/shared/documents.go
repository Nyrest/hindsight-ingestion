package shared

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors/cloudflare"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

type bankLock struct {
	token chan struct{}
	users int
}

var bankLocks = struct {
	sync.Mutex
	entries map[string]*bankLock
}{entries: map[string]*bankLock{}}

func TargetKey(cred connectors.Credential, bankID string) (string, error) {
	endpoint, err := cloudflare.NormalizeURL(cred.String("baseUrl"))
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(strings.TrimRight(endpoint, "/") + "\x00" + bankID))
	return hex.EncodeToString(digest[:]), nil
}

func Lock(ctx context.Context, key string) (func(), error) {
	bankLocks.Lock()
	entry := bankLocks.entries[key]
	if entry == nil {
		entry = &bankLock{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		bankLocks.entries[key] = entry
	}
	entry.users++
	bankLocks.Unlock()
	select {
	case <-ctx.Done():
		releaseUser(key, entry)
		return nil, ctx.Err()
	case <-entry.token:
		return func() { entry.token <- struct{}{}; releaseUser(key, entry) }, nil
	}
}

func releaseUser(key string, entry *bankLock) {
	bankLocks.Lock()
	defer bankLocks.Unlock()
	entry.users--
	if entry.users == 0 {
		delete(bankLocks.entries, key)
	}
}

type Documents struct {
	DB        *gorm.DB
	TargetKey string
}

func (d Documents) Fingerprint(ctx context.Context, documentID string) (string, error) {
	var document models.SharedDocument
	err := d.DB.WithContext(ctx).First(&document, "target_key = ? AND document_id = ?", d.TargetKey, documentID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	return document.SyncedFingerprint, err
}

func (d Documents) Confirm(ctx context.Context, row models.TaskItem) error {
	return d.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "target_key"}, {Name: "document_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"synced_fingerprint", "updated_at"}),
	}).Create(&models.SharedDocument{TargetKey: d.TargetKey, DocumentID: row.DestinationDocumentID, SyncedFingerprint: row.SyncedFingerprint}).Error
}

func (d Documents) OtherReferences(ctx context.Context, row models.TaskItem) (bool, error) {
	var count int64
	err := d.DB.WithContext(ctx).Model(&models.TaskItem{}).
		Where("shared_target_key = ? AND source_item_id = ? AND destination_document_id = ? AND task_id <> ? AND destination_present = ?", d.TargetKey, row.SourceItemID, row.DestinationDocumentID, row.TaskID, true).Count(&count).Error
	return count > 0, err
}

// Forget is only called after the last reference and the remote document are removed.
func (d Documents) Forget(ctx context.Context, documentID string) error {
	return d.DB.WithContext(ctx).Where("target_key = ? AND document_id = ?", d.TargetKey, documentID).Delete(&models.SharedDocument{}).Error
}
