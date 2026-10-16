package deepwiki

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

const endpoint = "https://mcp.deepwiki.com/mcp"

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9_.-]+$`)

type Source struct {
	endpoint string
}

func init() { connectors.RegisterSource(&Source{}) }

func (*Source) Info() connectors.SourceInfo {
	return connectors.SourceInfo{
		Type: "deepwiki", Name: "DeepWiki", CredentialTypes: []string{}, BrowseKinds: []string{},
		Capabilities: connectors.Capabilities{IncrementalMode: connectors.IncrementalInventory, DeletionMode: connectors.DeletionScanGeneration},
		Fields: []connectors.FieldSpec{
			{Key: "repoName", Label: "Repository name (case-sensitive)", Type: connectors.FieldString, Required: true,
				Placeholder: "owner/repo", Effect: connectors.EffectReconcile},
		},
		FilterFields: []connectors.FilterFieldSpec{},
	}
}

func (*Source) ValidateCredential(context.Context, connectors.Credential) (string, error) {
	return "", nil
}

func (*Source) ValidateConfig(cfg map[string]any, _ connectors.Filter) error {
	if !repoPattern.MatchString(strings.TrimSpace(connectors.AsString(cfg["repoName"]))) {
		return &connectors.FieldError{Field: "repoName", Message: "Enter a repository as owner/repo"}
	}
	return nil
}

func (*Source) Browse(context.Context, connectors.Credential, connectors.BrowseRequest) (connectors.BrowseResult, error) {
	return connectors.BrowseResult{Items: []connectors.BrowseItem{}}, nil
}

func (s *Source) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	if err := s.ValidateConfig(req.Config, req.Filter); err != nil {
		return connectors.ScanResult{}, err
	}
	repo := strings.TrimSpace(connectors.AsString(req.Config["repoName"]))
	url := s.endpoint
	if url == "" {
		url = endpoint
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "hindsight-ingestion", Version: httpx.Version}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: url, HTTPClient: httpx.New(nil, nil, req.Credential.Proxy).HTTP, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return connectors.ScanResult{}, fmt.Errorf("connect to DeepWiki: %w", err)
	}
	defer session.Close()
	var sections []string
	for _, tool := range []string{"read_wiki_structure", "read_wiki_contents"} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: tool, Arguments: map[string]any{"repoName": repo},
		})
		if err != nil {
			return connectors.ScanResult{}, fmt.Errorf("DeepWiki %s: %w", tool, err)
		}
		var parts []string
		for _, content := range result.Content {
			if text, ok := content.(*mcp.TextContent); ok && strings.TrimSpace(text.Text) != "" {
				parts = append(parts, text.Text)
			}
		}
		text := strings.Join(parts, "\n\n")
		if result.IsError {
			return connectors.ScanResult{}, fmt.Errorf("DeepWiki %s: %s", tool, text)
		}
		if strings.TrimSpace(text) == "" {
			return connectors.ScanResult{}, fmt.Errorf("DeepWiki returned no text from %s for %s", tool, repo)
		}
		sections = append(sections, text)
	}
	text := strings.Join(sections, "\n\n")
	body, err := json.Marshal(connectors.SourceContent{
		Text: text, Context: "DeepWiki documentation for " + repo, TimestampUnset: true,
	})
	if err != nil {
		return connectors.ScanResult{}, err
	}
	sum := sha256.Sum256([]byte(text))
	if err := req.Emit(connectors.SourceItem{
		ID: repo, Name: repo, Path: "https://deepwiki.com/" + repo, Kind: connectors.KindDocument,
		MIMEType: "text/markdown", Size: int64(len(text)), Revision: hex.EncodeToString(sum[:]),
		Metadata: map[string]string{"repo_name": repo, "url": "https://deepwiki.com/" + repo}, Opaque: body,
	}); err != nil {
		return connectors.ScanResult{}, err
	}
	return connectors.ScanResult{Complete: true}, nil
}

func (*Source) OpenContent(_ context.Context, req connectors.ContentRequest) (connectors.SourceContent, error) {
	var content connectors.SourceContent
	err := json.Unmarshal(req.Item.Opaque, &content)
	return content, err
}
