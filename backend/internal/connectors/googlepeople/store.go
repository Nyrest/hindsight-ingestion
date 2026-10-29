package googlepeople

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (c *Connector) AbortScan(ctx context.Context, runID string) error {
	return c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return clearStage(tx, runID) })
}

func clearStage(tx *gorm.DB, runID string) error {
	if err := tx.Where("run_id = ?", runID).Delete(&models.GooglePeopleScanAlias{}).Error; err != nil {
		return err
	}
	return tx.Where("run_id = ?", runID).Delete(&models.GooglePeopleScanContact{}).Error
}

func (c *Connector) clearAbandoned(ctx context.Context, req connectors.ScanRequest) error {
	return c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, model := range []any{&models.GooglePeopleScanAlias{}, &models.GooglePeopleScanContact{}} {
			if err := tx.Where("task_id = ? AND run_id <> ?", req.TaskID, req.RunID).Delete(model).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *scanner) aliases(ctx context.Context, people []person) (map[string]string, error) {
	names := []string{}
	for _, contact := range people {
		names = append(names, contact.ResourceName)
		names = append(names, contact.Metadata.PreviousResourceNames...)
	}
	slices.Sort(names)
	names = slices.Compact(names)
	known := map[string]string{}
	db := s.connector.DB.WithContext(ctx)
	for offset := 0; offset < len(names); offset += 300 {
		batch := names[offset:min(offset+300, len(names))]
		var committed []models.GooglePeopleIdentity
		if err := db.Where("task_id = ? AND resource_name IN ?", s.request.TaskID, batch).Find(&committed).Error; err != nil {
			return nil, err
		}
		for _, alias := range committed {
			known[alias.ResourceName] = alias.ItemID
		}
		var staged []models.GooglePeopleScanAlias
		if err := db.Where("run_id = ? AND resource_name IN ?", s.request.RunID, batch).Find(&staged).Error; err != nil {
			return nil, err
		}
		for _, alias := range staged {
			known[alias.ResourceName] = alias.ItemID
		}
	}
	return known, nil
}

func contactID(p person, known map[string]string) (string, error) {
	id, err := sourceContactID(p)
	if err != nil {
		return "", err
	}
	if id == "" && !p.Metadata.Deleted {
		return "", errors.New("Google contact has no CONTACT source ID")
	}
	for _, resource := range append([]string{p.ResourceName}, p.Metadata.PreviousResourceNames...) {
		if existing := known[resource]; existing != "" {
			if id != "" && id != existing {
				return "", errors.New("Google contact resource name has conflicting identities")
			}
			id = existing
		}
	}
	if id == "" {
		return "", errRebaseline
	}
	return id, nil
}

func sourceContactID(p person) (string, error) {
	id := ""
	for _, source := range p.Metadata.Sources {
		if source.Type != "CONTACT" || source.ID == "" {
			continue
		}
		candidate := "contact:" + source.ID
		if len(candidate) > 128 {
			return "", errors.New("Google CONTACT source ID exceeds supported length")
		}
		if id != "" && candidate != id {
			return "", errors.New("Google person has conflicting CONTACT source IDs")
		}
		id = candidate
	}
	return id, nil
}

func (s *scanner) stagePage(ctx context.Context, people []person) error {
	known, err := s.aliases(ctx, people)
	if err != nil {
		return err
	}
	aliases := map[string]models.GooglePeopleScanAlias{}
	contacts := map[string]models.GooglePeopleScanContact{}
	for _, contact := range people {
		id, err := contactID(contact, known)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(contact.ResourceName, "people/") || len(contact.ResourceName) > 512 {
			return errors.New("Google contact has an invalid resource name")
		}
		for _, resource := range append([]string{contact.ResourceName}, contact.Metadata.PreviousResourceNames...) {
			if !strings.HasPrefix(resource, "people/") || len(resource) > 512 {
				return errors.New("Google contact has an invalid former resource name")
			}
			known[resource] = id
			aliases[resource] = models.GooglePeopleScanAlias{RunID: s.request.RunID, TaskID: s.request.TaskID, ResourceName: resource, ItemID: id, Subject: s.request.Credential.OAuthSubject}
		}
		snapshot, err := normalize(contact, id)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		contacts[id] = models.GooglePeopleScanContact{RunID: s.request.RunID, TaskID: s.request.TaskID, ItemID: id, SnapshotJSON: string(encoded)}
	}
	return s.connector.savePage(ctx, aliases, contacts)
}

func (c *Connector) savePage(ctx context.Context, aliases map[string]models.GooglePeopleScanAlias, contacts map[string]models.GooglePeopleScanContact) error {
	return c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		aliasRows := make([]models.GooglePeopleScanAlias, 0, len(aliases))
		for _, alias := range aliases {
			aliasRows = append(aliasRows, alias)
		}
		if len(aliasRows) > 0 {
			if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).CreateInBatches(aliasRows, 100).Error; err != nil {
				return err
			}
		}
		contactRows := make([]models.GooglePeopleScanContact, 0, len(contacts))
		for _, contact := range contacts {
			contactRows = append(contactRows, contact)
		}
		if len(contactRows) == 0 {
			return nil
		}
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).CreateInBatches(contactRows, 100).Error
	})
}

func (s *scanner) emit(ctx context.Context) error {
	lastID := ""
	for {
		var snapshots []models.GooglePeopleScanContact
		if err := s.connector.DB.WithContext(ctx).Where("run_id = ? AND item_id > ?", s.request.RunID, lastID).
			Order("item_id").Limit(100).Find(&snapshots).Error; err != nil {
			return err
		}
		if len(snapshots) == 0 {
			return nil
		}
		for _, row := range snapshots {
			if err := ctx.Err(); err != nil {
				return err
			}
			var contact contactSnapshot
			if err := json.Unmarshal([]byte(row.SnapshotJSON), &contact); err != nil {
				return fmt.Errorf("read staged Google contact: %w", err)
			}
			if err := s.request.Emit(contact.sourceItem()); err != nil {
				return err
			}
		}
		lastID = snapshots[len(snapshots)-1].ItemID
	}
}

func (*Connector) CommitScan(ctx context.Context, runID string, tx *gorm.DB) error {
	lastResource := ""
	for {
		var staged []models.GooglePeopleScanAlias
		if err := tx.WithContext(ctx).Where("run_id = ? AND resource_name > ?", runID, lastResource).
			Order("resource_name").Limit(100).Find(&staged).Error; err != nil {
			return err
		}
		if len(staged) == 0 {
			break
		}
		identities := make([]models.GooglePeopleIdentity, 0, len(staged))
		for _, alias := range staged {
			identities = append(identities, models.GooglePeopleIdentity{TaskID: alias.TaskID, ResourceName: alias.ResourceName, ItemID: alias.ItemID, Subject: alias.Subject})
		}
		if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(identities).Error; err != nil {
			return err
		}
		lastResource = staged[len(staged)-1].ResourceName
	}
	var run models.TaskRun
	if err := tx.First(&run, "id = ?", runID).Error; err != nil {
		return err
	}
	// Keep historical aliases while their logical contact remains in the ledger.
	tracked := tx.Model(&models.TaskItem{}).Select("source_item_id").Where("task_id = ?", run.TaskID)
	if err := tx.Where("task_id = ? AND item_id NOT IN (?)", run.TaskID, tracked).Delete(&models.GooglePeopleIdentity{}).Error; err != nil {
		return err
	}
	return clearStage(tx.WithContext(ctx), runID)
}
