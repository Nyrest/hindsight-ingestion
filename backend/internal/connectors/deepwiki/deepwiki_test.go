package deepwiki

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func TestScan(t *testing.T) {
	for _, jsonResponse := range []bool{false, true} {
		t.Run(map[bool]string{false: "SSE", true: "JSON"}[jsonResponse], func(t *testing.T) {
			structure := "# Contents\n- Overview\n- Architecture"
			var toolCalls []string
			result := &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.TextContent{Text: "# Overview\nWiki content"},
				&mcp.TextContent{Text: "# Architecture\nMore content"},
			}}
			source, calls := testSource(t, jsonResponse, func(tool, repo string) *mcp.CallToolResult {
				toolCalls = append(toolCalls, tool)
				if repo != "Owner/Repo" {
					t.Errorf("repository case changed: %q", repo)
				}
				if tool == "read_wiki_structure" {
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: structure}}}
				}
				return result
			})
			scan := func() connectors.SourceItem {
				t.Helper()
				var items []connectors.SourceItem
				res, err := source.Scan(t.Context(), connectors.ScanRequest{
					Config: map[string]any{"repoName": " Owner/Repo "}, Cursor: []byte(`{"ignored":true}`),
					Emit: func(item connectors.SourceItem) error { items = append(items, item); return nil },
				})
				if err != nil || !res.Complete || len(res.Cursor) != 0 || len(items) != 1 {
					t.Fatalf("scan: %+v, items: %d, error: %v", res, len(items), err)
				}
				return items[0]
			}
			item := scan()
			if item.ID != "Owner/Repo" || item.Path != "https://deepwiki.com/Owner/Repo" || item.Kind != connectors.KindDocument {
				t.Fatal(item)
			}
			content, err := source.OpenContent(t.Context(), connectors.ContentRequest{Item: item})
			if err != nil || content.Text != structure+"\n\n# Overview\nWiki content\n\n# Architecture\nMore content" || !content.TimestampUnset {
				t.Fatalf("content: %+v, error: %v", content, err)
			}
			if *calls != 2 || strings.Join(toolCalls, ",") != "read_wiki_structure,read_wiki_contents" {
				t.Fatal("expected structure then contents, without refetching in OpenContent", toolCalls)
			}
			if scan().Revision != item.Revision {
				t.Fatal("unchanged content changed revision")
			}
			result.Content = []mcp.Content{&mcp.TextContent{Text: "Updated wiki"}}
			updated := scan()
			if updated.Revision == item.Revision {
				t.Fatal("changed content retained revision")
			}
			structure = "# Updated contents"
			if scan().Revision == updated.Revision {
				t.Fatal("changed structure retained revision")
			}
		})
	}
}

func TestScanFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result *mcp.CallToolResult
		want   string
	}{
		{"tool error", &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "Repository not found"}}}, "Repository not found"},
		{"empty content", &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "  "}}}, "no text"},
	} {
		for _, failedTool := range []string{"read_wiki_structure", "read_wiki_contents"} {
			t.Run(tc.name+"/"+failedTool, func(t *testing.T) {
				source, _ := testSource(t, false, func(tool, _ string) *mcp.CallToolResult {
					if tool == failedTool {
						return tc.result
					}
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Wiki"}}}
				})
				res, err := source.Scan(t.Context(), connectors.ScanRequest{
					Config: map[string]any{"repoName": "Owner/Repo"},
					Emit:   func(connectors.SourceItem) error { t.Fatal("failed scan emitted a document"); return nil },
				})
				if err == nil || !strings.Contains(err.Error(), tc.want) || res.Complete || len(res.Cursor) != 0 {
					t.Fatalf("result: %+v, error: %v", res, err)
				}
			})
		}
	}
	source, _ := testSource(t, true, func(string, string) *mcp.CallToolResult {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Wiki"}}}
	})
	want := errors.New("emit failed")
	res, err := source.Scan(t.Context(), connectors.ScanRequest{
		Config: map[string]any{"repoName": "Owner/Repo"}, Emit: func(connectors.SourceItem) error { return want },
	})
	if !errors.Is(err, want) || res.Complete {
		t.Fatalf("emit error: %+v, %v", res, err)
	}
}

func TestConfig(t *testing.T) {
	source := &Source{}
	if len(source.Info().CredentialTypes) != 0 {
		t.Fatal("DeepWiki requires a credential")
	}
	if _, ok := connectors.CredentialTypeOf("deepwiki"); ok {
		t.Fatal("DeepWiki registered a credential type")
	}
	for _, repo := range []string{"", "owner", "owner/repo/extra", "https://github.com/owner/repo", "owner/re po"} {
		if err := source.ValidateConfig(map[string]any{"repoName": repo}, connectors.Filter{}); err == nil {
			t.Errorf("accepted invalid repository %q", repo)
		}
	}
}

func testSource(t *testing.T, jsonResponse bool, result func(string, string) *mcp.CallToolResult) (*Source, *int) {
	t.Helper()
	calls := new(int)
	server := mcp.NewServer(&mcp.Implementation{Name: "deepwiki-test", Version: "1"}, nil)
	for _, tool := range []string{"read_wiki_structure", "read_wiki_contents"} {
		mcp.AddTool(server, &mcp.Tool{Name: tool}, func(_ context.Context, _ *mcp.CallToolRequest, args struct {
			RepoName string `json:"repoName"`
		}) (*mcp.CallToolResult, any, error) {
			*calls++
			return result(tool, args.RepoName), nil, nil
		})
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: jsonResponse})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Source{endpoint: srv.URL}, calls
}
