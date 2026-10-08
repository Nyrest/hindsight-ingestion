// Package hindsight is the Hindsight API client used as the sync destination.
package hindsight

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

// CredentialType is the credential type key for Hindsight.
const CredentialType = "hindsight"

// CredentialSpec is the Hindsight credential form.
var CredentialSpec = connectors.CredentialType{
	Type:        CredentialType,
	Name:        "Hindsight",
	Description: "Hindsight API endpoint used as a destination",
	Fields: []connectors.FieldSpec{
		{Key: "baseUrl", Label: "Base URL", Type: connectors.FieldURL, Required: true, Placeholder: "https://hindsight.example.com"},
		{Key: "apiKey", Label: "API key", Type: connectors.FieldPassword, Secret: true, Help: "Sent as a Bearer token. Leave empty if the instance has no authentication."},
	},
}

func init() {
	connectors.RegisterCredentialType(CredentialSpec)
}

// Client talks to one Hindsight instance.
type Client struct {
	baseURL string
	http    *httpx.Client
}

// NewClient builds a client from a decrypted credential.
func NewClient(cred connectors.Credential) (*Client, error) {
	base := strings.TrimRight(cred.String("baseUrl"), "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("hindsight base URL must be an http(s) URL")
	}
	auth := map[string]string{}
	if key := cred.String("apiKey"); key != "" {
		auth["Authorization"] = "Bearer " + key
	}
	c := httpx.New(cred.Headers, auth, cred.Proxy)
	return &Client{baseURL: base, http: c}, nil
}

func (c *Client) bankURL(bankID string, parts ...string) string {
	u := c.baseURL + "/v1/default/banks/" + url.PathEscape(bankID)
	for _, p := range parts {
		u += "/" + url.PathEscape(p)
	}
	return u
}

// Version returns the server version string.
func (c *Client) Version(ctx context.Context) (string, error) {
	var out struct {
		APIVersion string `json:"api_version"`
	}
	if err := c.http.JSON(ctx, http.MethodGet, c.baseURL+"/version", nil, &out); err != nil {
		return "", err
	}
	return out.APIVersion, nil
}

// Bank is a memory bank summary.
type Bank struct {
	BankID       string `json:"bank_id"`
	Name         string `json:"name"`
	DisplayAlias string `json:"display_alias"`
	FactCount    int    `json:"fact_count"`
}

// ListBanks returns every visible bank.
func (c *Client) ListBanks(ctx context.Context) ([]Bank, error) {
	var all []Bank
	for offset := 0; ; {
		var out struct {
			Banks []Bank `json:"banks"`
			Total int    `json:"total"`
		}
		q := url.Values{"limit": {"100"}, "offset": {fmt.Sprint(offset)}}
		if err := c.http.JSON(ctx, http.MethodGet, c.baseURL+"/v1/default/banks?"+q.Encode(), nil, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Banks...)
		offset += len(out.Banks)
		if len(out.Banks) == 0 || offset >= out.Total {
			return all, nil
		}
	}
}

// Strategies returns the bank's retain strategies and default strategy.
func (c *Client) Strategies(ctx context.Context, bankID string) (def string, names []string, err error) {
	var out struct {
		Config map[string]any `json:"config"`
	}
	if err := c.http.JSON(ctx, http.MethodGet, c.bankURL(bankID, "config"), nil, &out); err != nil {
		return "", nil, err
	}
	def = connectors.AsString(out.Config["retain_default_strategy"])
	if m, ok := out.Config["retain_strategies"].(map[string]any); ok {
		for k := range m {
			names = append(names, k)
		}
	}
	return def, names, nil
}

// MemoryItem is one retain item.
type MemoryItem struct {
	ObservationScopes json.RawMessage           `json:"observation_scopes,omitempty"`
	Blocks            []connectors.ContentBlock `json:"-"`
	Content           string                    `json:"content"`
	Timestamp         string                    `json:"timestamp,omitempty"`
	Context           string                    `json:"context,omitempty"`
	Metadata          map[string]string         `json:"metadata,omitempty"`
	DocumentID        string                    `json:"document_id"`
	Tags              []string                  `json:"tags,omitempty"`
	Strategy          string                    `json:"strategy,omitempty"`
	UpdateMode        string                    `json:"update_mode,omitempty"`
}

func (item MemoryItem) MarshalJSON() ([]byte, error) {
	type plain MemoryItem
	var content any = item.Content
	if len(item.Blocks) > 0 {
		content = item.Blocks
	}
	return json.Marshal(struct {
		plain
		Content any `json:"content"`
	}{plain(item), content})
}

// RetainBatch submits items asynchronously and returns operation IDs.
// operationID makes resubmission idempotent.
func (c *Client) RetainBatch(ctx context.Context, bankID string, items []MemoryItem, operationID string) ([]string, error) {
	body := map[string]any{"items": items, "async": true}
	if operationID != "" {
		body["operation_id"] = operationID
	}
	var out struct {
		Success      bool     `json:"success"`
		OperationID  string   `json:"operation_id"`
		OperationIDs []string `json:"operation_ids"`
	}
	if err := c.http.JSON(ctx, http.MethodPost, c.bankURL(bankID, "memories"), body, &out); err != nil {
		return nil, err
	}
	if !out.Success {
		return nil, fmt.Errorf("hindsight retain was not accepted")
	}
	return uniq(append([]string{out.OperationID}, out.OperationIDs...)), nil
}

// FileMeta is per-file retain metadata.
type FileMeta struct {
	DocumentID string            `json:"document_id"`
	Context    string            `json:"context,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Tags       []string          `json:"tags,omitempty"`
	Timestamp  string            `json:"timestamp,omitempty"`
	Strategy   string            `json:"strategy,omitempty"`
}

// RetainFile streams a single file to /files/retain using an io.Pipe so the
// content is never fully buffered in memory.
func (c *Client) RetainFile(ctx context.Context, bankID string, meta FileMeta, fileName, mimeType string, body io.Reader) ([]string, error) {
	reqJSON, err := json.Marshal(map[string]any{"files_metadata": []FileMeta{meta}})
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := func() error {
			if err := mw.WriteField("request", string(reqJSON)); err != nil {
				return err
			}
			h := make(textproto.MIMEHeader)
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files"; filename="%s"`, escapeQuotes(fileName)))
			if mimeType == "" {
				mimeType = "application/octet-stream"
			}
			h.Set("Content-Type", mimeType)
			part, err := mw.CreatePart(h)
			if err != nil {
				return err
			}
			if _, err := io.Copy(part, body); err != nil {
				return err
			}
			return mw.Close()
		}()
		pw.CloseWithError(err)
	}()

	header := http.Header{"Content-Type": {mw.FormDataContentType()}, "Accept": {"application/json"}}
	resp, err := c.http.DoStream(ctx, http.MethodPost, c.bankURL(bankID, "files", "retain"), header, pr)
	pr.CloseWithError(io.ErrClosedPipe) // unblock writer if the request failed early
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		OperationIDs []string `json:"operation_ids"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode file retain response: %w", err)
	}
	return uniq(out.OperationIDs), nil
}

// DeleteDocument deletes a document; a missing document is not an error.
func (c *Client) DeleteDocument(ctx context.Context, bankID, documentID string) error {
	err := c.http.JSON(ctx, http.MethodDelete, c.bankURL(bankID, "documents", documentID), nil, nil)
	if httpx.IsStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}

// OperationStatus is an async operation's state.
type OperationStatus struct {
	Status       string `json:"status"`
	RetryCount   int    `json:"retry_count"`
	ErrorMessage string `json:"error_message"`
}

// GetOperation returns an operation's status.
func (c *Client) GetOperation(ctx context.Context, bankID, opID string) (OperationStatus, error) {
	var out OperationStatus
	err := c.http.JSON(ctx, http.MethodGet, c.bankURL(bankID, "operations", opID), nil, &out)
	if httpx.IsStatus(err, http.StatusNotFound) {
		return OperationStatus{Status: "not_found"}, nil
	}
	return out, err
}

// RetryOperation retries a failed operation.
func (c *Client) RetryOperation(ctx context.Context, bankID, opID string) error {
	return c.http.JSON(ctx, http.MethodPost, c.bankURL(bankID, "operations", opID, "retry"), nil, nil)
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func escapeQuotes(s string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, "\\\"", "\r", "", "\n", "").Replace(s)
}
