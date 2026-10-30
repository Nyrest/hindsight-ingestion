package postprocess

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExtractionMultipart(t *testing.T) {
	for _, tc := range []struct{ provider, keyHeader, response, expected string }{
		{StepUnstructured, "unstructured-api-key", `[{"text":"Title"},{"text":"Body"}]`, "Title\n\nBody"},
		{StepKreuzberg, "Authorization", `[{"content":"# Content"}]`, "# Content"},
		{StepDocling, "X-Api-Key", `{"status":"success","document":{"md_content":"# Content"}}`, "# Content"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			data := []byte{0, 255, 1, 13, 10}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Query().Get("mode") != "test" {
					t.Error("unexpected method or query")
				}
				key := "secret"
				if tc.keyHeader == "Authorization" {
					key = "Bearer " + key
				}
				if r.Header.Get(tc.keyHeader) != key || r.Header.Get("X-Custom") != "Note" {
					t.Error("missing headers")
				}
				if err := r.ParseMultipartForm(1024); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				defer r.MultipartForm.RemoveAll()
				file, header, err := r.FormFile("files")
				if err != nil {
					t.Error(err)
					return
				}
				defer file.Close()
				got, _ := io.ReadAll(file)
				if !bytes.Equal(got, data) || header.Filename != "report.pdf" || header.Header.Get("Content-Type") != "application/pdf" {
					t.Error("upload changed file bytes or metadata")
				}
				if r.FormValue("language") != "eng" {
					t.Error("missing custom form field")
				}
				if tc.provider == StepDocling && (r.FormValue("to_formats") != "md" || r.FormValue("target_type") != "inbody") {
					t.Error("missing output options")
				}
				fmt.Fprint(w, tc.response)
			}))
			defer server.Close()
			cfg := Pipeline{Steps: []PipelineStep{{Type: tc.provider, Enabled: true, URL: server.URL, APIKey: "secret",
				Headers: []KeyValue{{"X-Custom", "{{item.name}}"}}, Form: []KeyValue{{"language", "eng"}}, Query: []KeyValue{{"mode", "test"}}}, {Type: StepAppend, Enabled: true, Text: "!"}}}
			res := run(t, cfg, Document{Binary: true, Data: data, FileName: "report.pdf", MIMEType: "application/pdf"})
			if res.Binary || res.Text != tc.expected+"!" {
				t.Fatalf("unexpected result %+v", res)
			}
		})
	}
}

func resultZIP(t *testing.T, names ...string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	for _, name := range names {
		file, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(file, "# Parsed"); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestMinerUUploadPollDownload(t *testing.T) {
	for _, mode := range []string{"cloud", "self_hosted"} {
		t.Run(mode, func(t *testing.T) {
			zipData := resultZIP(t, "output/full.md", "output/data.json")
			uploads, downloads, polls := 0, 0, 0
			storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || r.Header.Get("X-Custom") != "" {
					t.Error("API credentials leaked to storage")
				}
				if r.Method == "PUT" {
					uploads++
					data, _ := io.ReadAll(r.Body)
					if string(data) != "PDF bytes" {
						t.Error("upload content changed")
					}
					if mode == "self_hosted" && r.Header.Get("X-Upload") != "signed" {
						t.Error("missing upload headers")
					}
				} else {
					downloads++
					w.Write(zipData)
				}
			}))
			defer storage.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer secret" {
					t.Error("missing API credentials")
				}
				if r.Method == "POST" {
					var payload map[string]any
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
					}
					files, ok := payload["files"].([]any)
					if !ok || len(files) != 1 {
						t.Error("missing file manifest")
					}
					if mode == "cloud" {
						if r.URL.Path != "/api/v4/file-urls/batch" {
							t.Error(r.URL.Path)
						}
						fmt.Fprintf(w, `{"code":0,"data":{"batch_id":"batch","file_urls":[%q]}}`, storage.URL)
					} else {
						if r.URL.Path != "/v1/tasks" {
							t.Error(r.URL.Path)
						}
						manifest := files[0].(map[string]any)
						if manifest["file_name"] != "report.pdf" || manifest["page_range"] != "" || manifest["bytes"] != float64(9) {
							t.Error("incorrect V1 manifest")
						}
						w.WriteHeader(http.StatusAccepted)
						fmt.Fprintf(w, `{"task_id":"task","status":"waiting-file","files":[{"file_id":"f","file_name":"report.pdf","status":"waiting-file","upload":{"url":%q,"headers":{"X-Upload":"signed"}}}]}`, storage.URL)
					}
					return
				}
				polls++
				if mode == "cloud" {
					state := "running"
					if polls > 1 {
						state = "done"
					}
					fmt.Fprintf(w, `{"code":0,"data":{"batch_id":"batch","extract_result":[{"file_name":"report.pdf","state":%q,"full_zip_url":%q}]}}`, state, storage.URL)
				} else {
					fmt.Fprintf(w, `{"task_id":"task","status":"completed","files":[{"file_id":"f","file_name":"report.pdf","status":"completed","result_url":%q}]}`, storage.URL)
				}
			}))
			defer server.Close()
			res := run(t, Pipeline{Steps: []PipelineStep{{Type: StepMinerU, Enabled: true, APIMode: mode, URL: server.URL, APIKey: "secret", Headers: []KeyValue{{"X-Custom", "private"}}}}}, Document{Binary: true, Data: []byte("PDF bytes"), FileName: "report.pdf"})
			if res.Text != "# Parsed" || uploads != 1 || downloads != 1 {
				t.Fatalf("result=%+v uploads=%d downloads=%d", res, uploads, downloads)
			}
		})
	}
}

func TestExtractionRejectsInvalidResults(t *testing.T) {
	for _, tc := range []struct{ provider, response string }{
		{StepUnstructured, `null`}, {StepUnstructured, `[{"text":null}]`}, {StepKreuzberg, `{"error":"bad"}`},
		{StepDocling, `{"status":"partial_success","document":{"md_content":"partial"}}`},
		{StepDocling, `{"status":"success","document":{}}`},
	} {
		if _, err := extractionText(tc.provider, []byte(tc.response)); err == nil {
			t.Errorf("accepted %s", tc.response)
		}
	}
	for _, data := range [][]byte{[]byte("not a ZIP"), resultZIP(t, "x.json"), resultZIP(t, "a.md", "b.md")} {
		if _, err := markdownFromZIP(data); err == nil {
			t.Error("accepted invalid ZIP result")
		}
	}
}

func TestExtractionSkipsWithoutRequest(t *testing.T) {
	for _, provider := range []string{StepUnstructured, StepKreuzberg, StepDocling, StepMinerU} {
		res := run(t, Pipeline{Steps: []PipelineStep{{Type: StepSkip, Enabled: true, Pattern: "skip"}, {Type: provider, Enabled: true, URL: "http://invalid.invalid"}}}, Document{Text: "skip"})
		if !res.Skip {
			t.Error("skipped document was extracted")
		}
	}
}

func TestMinerUCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"task_id":"task","status":"pending","files":[{"file_id":"f","file_name":"document.txt","status":"pending"}]}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	script := Generate([]PipelineStep{{Type: StepMinerU, Enabled: true, URL: server.URL}})
	_, err := Run(ctx, script, Env{}, Document{Text: "text"})
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestExtractionHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "private upstream details", 401) }))
	defer server.Close()
	_, err := Run(context.Background(), Generate([]PipelineStep{{Type: StepUnstructured, Enabled: true, URL: server.URL}}), Env{}, Document{Text: "text"})
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "private") {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestExtractionConfigurationAndReservedFields(t *testing.T) {
	for _, step := range []PipelineStep{
		{Type: StepDocling},
		{Type: StepMinerU, URL: "https://example.com", APIMode: "invalid"},
		{Type: StepMinerU, URL: "https://example.com", APIMode: "cloud"},
	} {
		if _, err := step.normalize(); err == nil {
			t.Errorf("accepted invalid step %+v", step)
		}
	}
	for _, key := range []string{"files", "to_formats", "target_type", "image_export_mode"} {
		_, _, err := extractionMultipart(PipelineStep{Type: StepDocling, Form: []KeyValue{{key, "override"}}}, []byte("file"), "file.txt", "text/plain")
		if err == nil {
			t.Errorf("accepted reserved field %s", key)
		}
	}
	if _, err := extractionText(StepKreuzberg, []byte(`[]`)); err == nil {
		t.Error("accepted missing file result")
	}
	if text, err := extractionText(StepUnstructured, []byte(`[]`)); err != nil || text != "" {
		t.Error("empty element array is a valid Unstructured result")
	}
}

func TestMinerUTerminalAndMalformedResponses(t *testing.T) {
	for _, response := range []string{
		`{"task_id":"t","status":"failed","files":[{"file_id":"f","file_name":"document.txt","status":"failed"}]}`,
		`{"task_id":"t","status":"partial","files":[{"file_id":"f","file_name":"document.txt","status":"completed"}]}`,
		`{"task_id":"t","status":"unknown","files":[{"file_id":"f","file_name":"document.txt","status":"pending"}]}`,
		`{"task_id":"t","status":"completed","files":[]}`,
		`{"task_id":"t","status":"completed","files":[{"file_id":"f","file_name":"document.txt","status":"completed"}]}`,
		`{"status":"pending"}`, `null`,
	} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, response) }))
			defer server.Close()
			_, err := Run(context.Background(), Generate([]PipelineStep{{Type: StepMinerU, Enabled: true, URL: server.URL}}), Env{}, Document{Text: "text"})
			if err == nil {
				t.Error("accepted invalid result")
			}
		})
	}
	for _, response := range []string{`{"code":401}`, `{"data":{}}`, `{"code":0,"data":{"batch_id":"b","file_urls":[]}}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, response) }))
		_, err := Run(context.Background(), Generate([]PipelineStep{{Type: StepMinerU, Enabled: true, APIMode: "cloud", APIKey: "secret", URL: server.URL}}), Env{}, Document{Text: "text"})
		server.Close()
		if err == nil {
			t.Errorf("accepted invalid cloud result %s", response)
		}
	}
}

func TestExtractionStorageSameOriginAndRedirect(t *testing.T) {
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Custom") != "" {
			t.Error("redirect leaked credentials")
		}
		fmt.Fprint(w, "download")
	}))
	defer storage.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("same-origin authentication missing")
		}
		http.Redirect(w, r, storage.URL, http.StatusFound)
	}))
	defer api.Close()
	client := extractionClient{vm: &VM{ctx: context.Background()}, step: PipelineStep{URL: api.URL}, headers: http.Header{"Authorization": {"Bearer secret"}, "X-Custom": {"private"}}}
	content, err := client.storageRequest("GET", "/download", nil, nil)
	if err != nil || string(content) != "download" {
		t.Fatalf("download: %s, %v", content, err)
	}
	client.step.URL = "https://api.example.com"
	if _, err := client.storageRequest("GET", "http://storage.example.com", nil, nil); err == nil {
		t.Error("allowed HTTPS downgrade")
	}
}
