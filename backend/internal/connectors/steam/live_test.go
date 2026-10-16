package steam

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

// Opt in explicitly; normal test runs never make paid or private upstream calls.
func TestLiveSteamDocuments(t *testing.T) {
	apiKey := os.Getenv("STEAM_LIVE_API_KEY")
	if apiKey == "" {
		t.Skip("STEAM_LIVE_API_KEY not set")
	}
	sid := os.Getenv("STEAM_LIVE_ID")
	if sid == "" {
		t.Fatal("STEAM_LIVE_ID is required")
	}
	output := os.Getenv("STEAM_LIVE_OUTPUT")
	if output == "" {
		output = t.TempDir()
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open("sqlite", filepath.Join(output, "steam-validation.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db.DB(); sqlDB.Close() }()
	c := &Connector{DB: db}
	cred := connectors.Credential{ID: "live-test", Type: SourceType, Secrets: map[string]string{"apiKey": apiKey}}
	requests := func() int64 {
		var count int64
		if err := db.Model(&models.SteamAPIUsage{}).Where("key_hash = ?", CredentialKey(cred)).Select("COALESCE(SUM(requests), 0)").Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		return count
	}
	requestsBefore := requests()
	if _, err := c.ValidateCredential(t.Context(), cred); err != nil {
		t.Fatal(err)
	}
	revisions := map[string]string{}
	counts := map[string]int{}
	username := ""
	req := connectors.ScanRequest{TaskID: "live-test", Credential: cred, Config: map[string]any{"steamId": sid, "language": "schinese"}, Full: true,
		Emit: func(item connectors.SourceItem) error {
			if item.ContentError != "" {
				return fmt.Errorf("%s: %s", item.ID, item.ContentError)
			}
			content, err := c.OpenContent(t.Context(), connectors.ContentRequest{Item: item})
			if err != nil {
				return err
			}
			if !strings.Contains(content.Text, sid) {
				t.Error("document lacks player ID")
			}
			if item.Metadata["steam_document_type"] == "profile" {
				username = strings.TrimPrefix(item.Name, "Steam Profile: ")
			}
			if item.Metadata["steam_document_type"] == "game" && (!strings.Contains(content.Text, "## Player") || !strings.Contains(content.Text, "- Username: "+md(username))) {
				t.Error("game lacks player ownership")
			}
			if item.Metadata["steam_document_type"] == "game" {
				for _, status := range []string{"Available", "Partial", "Unavailable", "No Achievements"} {
					if strings.Contains(content.Text, "- Status: "+status+"\n") {
						counts[status]++
						break
					}
				}
			}
			revisions[item.ID] = item.Revision
			if err := os.WriteFile(filepath.Join(output, strings.ReplaceAll(item.ID, ":", "_")+".md"), []byte(content.Text), 0600); err != nil {
				return err
			}
			if len(revisions)%25 == 0 {
				t.Logf("generated %d documents", len(revisions))
			}
			return nil
		}}
	first, err := c.Scan(t.Context(), req)
	if err != nil || first.Error != nil || !first.Complete {
		t.Fatalf("full scan failed: %v %v", err, first.Error)
	}
	afterFull := requests()
	fullRequests := afterFull - requestsBefore
	t.Logf("full scan: documents=%d requests=%d states=%v", len(revisions), fullRequests, counts)
	if err := os.WriteFile(filepath.Join(output, "cursor.json"), first.Cursor, 0600); err != nil {
		t.Fatal(err)
	}
	secondCount, changed := 0, 0
	req.Full = false
	req.Cursor = first.Cursor
	req.Emit = func(item connectors.SourceItem) error {
		secondCount++
		if item.Revision != revisions[item.ID] {
			changed++
		}
		return nil
	}
	second, err := c.Scan(t.Context(), req)
	if err != nil || second.Error != nil || !second.Complete || secondCount != len(revisions) {
		t.Fatalf("incremental scan failed: %v %v", err, second.Error)
	}
	incrementalRequests := requests() - afterFull
	report := map[string]any{"steamId": sid, "documents": len(revisions), "achievementStates": counts, "fullRequests": fullRequests, "incrementalRequests": incrementalRequests, "changedDocuments": changed}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(filepath.Join(output, "report.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("incremental scan: requests=%d changed=%d", incrementalRequests, changed)
}
