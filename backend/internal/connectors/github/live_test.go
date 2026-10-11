package github

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

// Opt in so the normal suite remains deterministic and needs no network or PAT.
func TestPublicRepositoryReadOnly(t *testing.T) {
	repo := os.Getenv("GITHUB_LIVE_TEST_REPOSITORY")
	if repo == "" {
		t.Skip("set GITHUB_LIVE_TEST_REPOSITORY for read-only upstream validation")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cred := connectors.Credential{}
	cfg := map[string]any{"repository": repo}
	var items []connectors.SourceItem
	s := &Source{}
	result, err := s.Scan(ctx, connectors.ScanRequest{Credential: cred, Config: cfg, Full: true, Emit: func(i connectors.SourceItem) error { items = append(items, i); return nil }})
	if err != nil || !result.Complete || len(items) == 0 {
		t.Fatalf("public scan: %d items, complete=%t, err=%v", len(items), result.Complete, err)
	}
	counts := map[string]int{}
	opened := false
	for _, item := range items {
		counts[item.Metadata["github_content_type"]]++
		if !opened && item.Kind == connectors.KindFile && item.Name == "README.md" {
			content, err := s.OpenContent(ctx, connectors.ContentRequest{Credential: cred, Config: cfg, Item: item})
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(content.Body)
			content.Body.Close()
			if err != nil || len(b) == 0 {
				t.Fatalf("readme %d bytes: %v", len(b), err)
			}
			opened = true
		}
	}
	if !opened {
		t.Fatal("README.md was not readable")
	}
	t.Logf("Read-only GitHub validation: %v; README read by fixed blob SHA", counts)
}
