package postprocess

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
	"github.com/dop251/goja"
)

type extractionClient struct {
	vm      *VM
	step    PipelineStep
	headers http.Header
}

func (v *VM) extract(call goja.FunctionCall) goja.Value {
	var step PipelineStep
	var doc struct {
		FileName string `json:"fileName"`
		MIMEType string `json:"mimeType"`
	}
	// Export via JSON so the same public field names apply in UI and code mode.
	decode := func(value goja.Value, target any) error {
		raw, err := json.Marshal(value.Export())
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, target)
	}
	if err := decode(call.Argument(0), &step); err != nil {
		v.throw("extraction options: %v", err)
	}
	if err := decode(call.Argument(2), &doc); err != nil {
		v.throw("extraction document: %v", err)
	}
	step, validationErr := step.normalize()
	if validationErr != nil {
		v.throw("extraction: %v", validationErr)
	}
	client := extractionClient{vm: v, step: step, headers: extractionHeaders(step)}
	name := path.Base(strings.ReplaceAll(doc.FileName, "\\", "/"))
	if name == "" || name == "." || name == "/" {
		name = "document.bin"
	}
	data := v.bytesArg(call.Argument(1))
	var text string
	var err error
	if step.Type == StepMinerU {
		text, err = client.minerU(data, name)
	} else {
		text, err = client.multipartExtract(data, name, doc.MIMEType)
	}
	if err != nil {
		v.throw("%s: %v", step.Type, err)
	}
	return v.rt.ToValue(text)
}

func extractionHeaders(step PipelineStep) http.Header {
	h := make(http.Header)
	h.Set("Accept", "application/json")
	if step.APIKey != "" {
		switch step.Type {
		case StepUnstructured:
			h.Set("unstructured-api-key", step.APIKey)
		case StepDocling:
			h.Set("X-Api-Key", step.APIKey)
		default:
			h.Set("Authorization", "Bearer "+step.APIKey)
		}
	}
	for _, pair := range step.Headers {
		if pair.Key != "" {
			h.Set(pair.Key, pair.Value)
		}
	}
	return h
}

func (c extractionClient) request(method, endpoint string, body []byte, headers http.Header) ([]byte, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, fmt.Errorf("a valid HTTP(S) URL without user information is required")
	}
	ctx, cancel := context.WithTimeout(c.vm.ctx, FetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set("User-Agent", httpx.UserAgent)
	resp, data, err := c.vm.request(req)
	if err != nil {
		var requestErr *url.Error
		if errors.As(err, &requestErr) {
			err = requestErr.Err // Signed storage URLs must not appear in run logs.
		}
		return nil, fmt.Errorf("%s request failed: %w", method, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned HTTP %d", method, resp.StatusCode)
	}
	return data, nil
}

func (c extractionClient) jsonRequest(method, endpoint string, payload any, target any) error {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	headers := c.headers.Clone()
	headers.Set("Content-Type", "application/json")
	data, err := c.request(method, endpoint, body, headers)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("invalid JSON response: %w", err)
	}
	return nil
}

func (c extractionClient) multipartExtract(data []byte, name, contentType string) (string, error) {
	body, boundary, err := extractionMultipart(c.step, data, name, contentType)
	if err != nil {
		return "", err
	}
	headers := c.headers.Clone()
	headers.Set("Content-Type", boundary)
	endpoint, err := url.Parse(c.step.URL)
	if err != nil {
		return "", err
	}
	query := endpoint.Query()
	for _, pair := range c.step.Query {
		if pair.Key != "" {
			query.Add(pair.Key, pair.Value)
		}
	}
	endpoint.RawQuery = query.Encode()
	response, err := c.request("POST", endpoint.String(), body, headers)
	if err != nil {
		return "", err
	}
	return extractionText(c.step.Type, response)
}

func extractionMultipart(step PipelineStep, data []byte, name, contentType string) ([]byte, string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fields := append([]KeyValue{}, step.Form...)
	for _, field := range fields {
		if field.Key == "files" || (step.Type == StepDocling && (field.Key == "to_formats" || field.Key == "target_type" || field.Key == "image_export_mode")) {
			return nil, "", fmt.Errorf("form field %q is managed by the extraction step", field.Key)
		}
	}
	// Docling must return JSON with Markdown, not a ZIP or image attachments.
	if step.Type == StepDocling {
		fields = append(fields, KeyValue{"to_formats", "md"}, KeyValue{"target_type", "inbody"}, KeyValue{"image_export_mode", "placeholder"})
	}
	for _, field := range fields {
		if field.Key == "" {
			continue
		}
		if err := w.WriteField(field.Key, field.Value); err != nil {
			return nil, "", err
		}
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "files", "filename": name}))
	h.Set("Content-Type", strings.NewReplacer("\r", "", "\n", "").Replace(contentType))
	part, err := w.CreatePart(h)
	if err != nil {
		return nil, "", err
	}
	if _, err = part.Write(data); err != nil {
		return nil, "", err
	}
	if err = w.Close(); err != nil {
		return nil, "", err
	}
	return body.Bytes(), w.FormDataContentType(), nil
}

func extractionText(provider string, data []byte) (string, error) {
	if provider == StepDocling {
		var result struct {
			Status   string `json:"status"`
			Document struct {
				Markdown *string `json:"md_content"`
			} `json:"document"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return "", err
		}
		if result.Status != "success" || result.Document.Markdown == nil {
			return "", fmt.Errorf("conversion did not return a successful Markdown document (status %q)", result.Status)
		}
		return *result.Document.Markdown, nil
	}
	var elements []map[string]json.RawMessage
	if err := json.Unmarshal(data, &elements); err != nil {
		return "", fmt.Errorf("invalid extraction response: %w", err)
	}
	if elements == nil {
		return "", fmt.Errorf("expected an array of extraction results")
	}
	if provider == StepKreuzberg && len(elements) != 1 {
		return "", fmt.Errorf("expected one extraction result for the uploaded file")
	}
	texts := make([]string, 0, len(elements))
	for _, element := range elements {
		field := "text"
		if provider == StepKreuzberg {
			field = "content"
		}
		var text *string
		if err := json.Unmarshal(element[field], &text); err != nil || text == nil {
			return "", fmt.Errorf("extraction result is missing %s", field)
		}
		texts = append(texts, *text)
	}
	return strings.Join(texts, "\n\n"), nil
}

func markdownFromZIP(data []byte) (string, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("invalid result ZIP: %w", err)
	}
	// A single input must have one Markdown artifact; never silently choose among documents.
	var markdown *zip.File
	for _, file := range archive.File {
		if !file.FileInfo().IsDir() && strings.EqualFold(path.Ext(file.Name), ".md") {
			if markdown != nil {
				return "", fmt.Errorf("result ZIP contains multiple Markdown documents")
			}
			markdown = file
		}
	}
	if markdown == nil {
		return "", fmt.Errorf("result ZIP has no Markdown document")
	}
	if markdown.UncompressedSize64 > MaxDocumentBytes {
		return "", fmt.Errorf("Markdown exceeds %d MiB", MaxDocumentBytes>>20)
	}
	reader, err := markdown.Open()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	text, err := io.ReadAll(io.LimitReader(reader, MaxDocumentBytes+1))
	if err != nil {
		return "", err
	}
	if len(text) > MaxDocumentBytes {
		return "", fmt.Errorf("Markdown exceeds %d MiB", MaxDocumentBytes>>20)
	}
	return string(text), nil
}

func waitForExtraction(ctx context.Context) error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

// Pre-signed storage URLs must not receive API keys or custom API headers.
func (c extractionClient) storageRequest(method, reference string, body []byte, supplied http.Header) ([]byte, error) {
	if reference == "" {
		return nil, fmt.Errorf("missing upload or result URL")
	}
	base, err := url.Parse(strings.TrimRight(c.step.URL, "/") + "/")
	if err != nil {
		return nil, err
	}
	ref, err := url.Parse(reference)
	if err != nil {
		return nil, err
	}
	u := base.ResolveReference(ref)
	if base.Scheme == "https" && u.Scheme != "https" {
		return nil, fmt.Errorf("refusing HTTPS downgrade")
	}
	headers := make(http.Header)
	if strings.EqualFold(base.Scheme, u.Scheme) && strings.EqualFold(base.Host, u.Host) {
		headers = c.headers.Clone()
	}
	for k, values := range supplied {
		headers[k] = values
	}
	return c.request(method, u.String(), body, headers)
}
