package sync_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	stdsync "sync"
	"testing"

	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/notion"
	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/sync"
	"github.com/google/uuid"
)

type notionTree struct {
	mu            stdsync.Mutex
	children      []string
	revisions     map[string]string
	failPath      string
	markdownReads int
}

func (tree *notionTree) serve(w http.ResponseWriter, r *http.Request) {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	if r.URL.Path == tree.failPath {
		w.WriteHeader(403)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 2 && len(parts) != 3 {
		w.WriteHeader(400)
		return
	}
	id := parts[1]
	if parts[0] == "pages" && len(parts) == 3 && parts[2] == "markdown" {
		tree.markdownReads++
		json.NewEncoder(w).Encode(map[string]any{"markdown": "# " + id + "\n" + tree.revisions[id]})
		return
	}
	if parts[0] == "pages" && len(parts) == 2 {
		revision := tree.revisions[id]
		if revision == "" {
			revision = "2026-10-01T00:00:00Z"
		}
		json.NewEncoder(w).Encode(map[string]any{"object": "page", "id": id, "last_edited_time": revision, "url": "https://notion.so/" + id,
			"properties": map[string]any{"title": map[string]any{"type": "title", "title": []map[string]string{{"plain_text": id}}}}})
		return
	}
	if parts[0] == "blocks" && len(parts) == 3 && parts[2] == "children" {
		var blocks []map[string]any
		if id == "root" {
			for _, child := range tree.children {
				blocks = append(blocks, map[string]any{"type": "child_page", "id": child})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"results": blocks})
		return
	}
	w.WriteHeader(400)
}

func notionHarness(t *testing.T) (*harness, *notionTree) {
	t.Helper()
	h := newHarness(t)
	tree := &notionTree{children: []string{"a", "b"}, revisions: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(tree.serve))
	t.Cleanup(server.Close)
	cred := models.Credential{ID: uuid.NewString()}
	name := "Notion"
	if err := h.engine.Creds.Apply(&cred, credentials.Input{Name: &name, Type: "notion", Config: map[string]any{"baseUrl": server.URL, "token": "test-token"}}, true); err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&cred).Error; err != nil {
		t.Fatal(err)
	}
	h.task.SourceType, h.task.SourceCredentialID, h.task.SourceConfigJSON = "notion", cred.ID, `{"mode":"pages","pageIds":["root"],"includeSubpages":true}`
	if err := h.db.Save(&h.task).Error; err != nil {
		t.Fatal(err)
	}
	return h, tree
}

func TestNotionHierarchyLifecycle(t *testing.T) {
	h, tree := notionHarness(t)
	first := h.run("")
	expectStatus(t, first, models.RunSucceeded)
	if first.CreatedCount != 3 {
		t.Fatalf("initial: %+v", first)
	}
	for _, id := range []string{"root", "a", "b"} {
		if _, ok := h.dest.docs[sync.DocumentID("notion", h.task.ID, id)]; !ok {
			t.Fatal("page identity changed", id)
		}
	}
	reads := tree.markdownReads
	unchanged := h.run("")
	expectStatus(t, unchanged, models.RunSucceeded)
	if unchanged.UnchangedCount != 3 || tree.markdownReads != reads {
		t.Fatal("unchanged pages fetched content", unchanged, tree.markdownReads)
	}
	tree.mu.Lock()
	tree.children = []string{"a", "c"}
	tree.revisions["a"] = "2026-10-02T00:00:00Z"
	tree.mu.Unlock()
	preview, err := h.engine.DryRun(t.Context(), h.task.ID)
	if err != nil || !preview.Complete || preview.UpdatedCount != 1 || preview.CreatedCount != 1 || preview.DeletedCount != 1 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	if len(h.dest.deleted) != 0 {
		t.Fatal("dry run deleted documents")
	}
	changed := h.run("")
	expectStatus(t, changed, models.RunSucceeded)
	if changed.UpdatedCount != 1 || changed.CreatedCount != 1 || changed.DeletedCount != 1 {
		t.Fatalf("descendant-only change: %+v", changed)
	}
	tree.mu.Lock()
	tree.children = nil
	tree.mu.Unlock()
	empty := h.run("")
	expectStatus(t, empty, models.RunSucceeded)
	if empty.DeletedCount != 2 || len(h.dest.docs) != 1 {
		t.Fatalf("scope exits: %+v", empty)
	}
}

func TestNotionHierarchyAccessFailureDoesNotDelete(t *testing.T) {
	h, tree := notionHarness(t)
	expectStatus(t, h.run(""), models.RunSucceeded)
	before := h.state()
	for _, path := range []string{"/pages/root", "/pages/a", "/blocks/a/children", "/blocks/root/children"} {
		tree.mu.Lock()
		tree.children = []string{"a"}
		tree.failPath = path
		tree.mu.Unlock()
		failed := h.run("")
		expectStatus(t, failed, models.RunFailed)
		if len(h.dest.deleted) != 0 || len(h.dest.docs) != 3 || h.state().CommittedCursorJSON != before.CommittedCursorJSON || !h.state().LastSuccessAt.Equal(*before.LastSuccessAt) {
			t.Fatal(fmt.Sprintf("failure at %s changed deletion/state: %+v", path, failed))
		}
	}
}
