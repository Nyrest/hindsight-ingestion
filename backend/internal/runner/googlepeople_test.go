package runner

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func TestRecoverInterruptedClearsGooglePeopleStaging(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open("sqlite", filepath.Join(t.TempDir(), "recovery.db"), log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sql, _ := db.DB(); sql.Close() }()
	for _, row := range []any{
		&models.TaskRun{ID: "interrupted", TaskID: "contacts", Status: models.RunRunning},
		&models.TaskState{TaskID: "contacts", PendingScanRunID: "interrupted", CommittedCursorJSON: "baseline"},
		&models.GooglePeopleIdentity{TaskID: "contacts", ResourceName: "people/1", ItemID: "contact:1", Subject: "account"},
		&models.GooglePeopleScanAlias{RunID: "interrupted", TaskID: "contacts", ResourceName: "people/2", ItemID: "contact:2", Subject: "account"},
		&models.GooglePeopleScanContact{RunID: "preview-without-history", TaskID: "contacts", ItemID: "contact:2", SnapshotJSON: "{}"},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	manager := NewManager(db, nil, 1, log)
	count, err := manager.RecoverInterrupted(t.Context())
	if err != nil || count != 1 {
		t.Fatal(count, err)
	}
	for _, model := range []any{&models.GooglePeopleScanAlias{}, &models.GooglePeopleScanContact{}} {
		var remaining int64
		if err := db.Model(model).Count(&remaining).Error; err != nil || remaining != 0 {
			t.Fatal("recovery retained staging", remaining, err)
		}
	}
	var committed int64
	db.Model(&models.GooglePeopleIdentity{}).Count(&committed)
	var state models.TaskState
	if err := db.First(&state, "task_id = ?", "contacts").Error; err != nil || state.CommittedCursorJSON != "baseline" || state.PendingScanRunID != "interrupted" || committed != 1 {
		t.Fatal("recovery changed committed state", state, err)
	}
}
