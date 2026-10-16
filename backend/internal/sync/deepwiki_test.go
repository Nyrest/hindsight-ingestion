package sync_test

import (
	"context"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors/deepwiki"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/proxy"
)

type wikiSource struct {
	*fakeSource
	proxy proxy.Config
}

func (*wikiSource) Info() connectors.SourceInfo { return (&deepwiki.Source{}).Info() }

func (s *wikiSource) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	s.proxy = req.Credential.Proxy
	res, err := s.fakeSource.Scan(ctx, req)
	res.Cursor = nil
	return res, err
}

func TestDeepWikiSyncWithoutCredentialOrCursor(t *testing.T) {
	h := newHarness(t)
	old, _ := connectors.Source("deepwiki")
	source := &wikiSource{fakeSource: h.src}
	connectors.RegisterSource(source)
	t.Cleanup(func() { connectors.RegisterSource(old) })
	h.task.SourceType, h.task.SourceCredentialID = "deepwiki", ""
	h.task.SourceConfigJSON = `{"repoName":"Owner/Repo"}`
	if err := h.db.Save(&h.task).Error; err != nil {
		t.Fatal(err)
	}
	cfg, err := h.engine.Settings.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Proxy = proxy.Config{Type: "none"}
	if err := h.engine.Settings.Put(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	h.setDocs(map[string]fakeDoc{"Owner/Repo": {rev: "1", content: "Wiki"}})
	preview, err := h.engine.DryRun(t.Context(), h.task.ID)
	if err != nil || preview.CreatedCount != 1 || len(h.dest.docs) != 0 {
		t.Fatalf("preview: %+v, error: %v", preview, err)
	}
	first := h.run("")
	expectStatus(t, first, models.RunSucceeded)
	if first.CreatedCount != 1 || source.proxy.Type != "none" {
		t.Fatalf("first run: %+v, proxy: %+v", first, source.proxy)
	}
	unchanged := h.run("")
	expectStatus(t, unchanged, models.RunSucceeded)
	if unchanged.UnchangedCount != 1 {
		t.Fatal(unchanged)
	}
	h.setDocs(map[string]fakeDoc{"Owner/Repo": {rev: "2", content: "Updated wiki"}})
	updated := h.run("")
	expectStatus(t, updated, models.RunSucceeded)
	if updated.UpdatedCount != 1 || len(h.dest.docs) != 1 || h.state().CommittedCursorJSON != "" {
		t.Fatal(updated, h.state())
	}
}
