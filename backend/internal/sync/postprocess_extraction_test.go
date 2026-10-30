package sync

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/postprocess"
	"github.com/Nyrest/hindsight-ingestion/internal/proxy"
)

func TestBinaryExtractionDoesNotBypassPostProcessing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "extraction unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	for _, provider := range []string{postprocess.StepUnstructured, postprocess.StepKreuzberg, postprocess.StepDocling, postprocess.StepMinerU} {
		t.Run(provider, func(t *testing.T) {
			cfg, _ := json.Marshal(postprocess.Pipeline{Enabled: true, Mode: "ui", Steps: []postprocess.PipelineStep{{Type: provider, Enabled: true, URL: server.URL}}})
			r := &runner{ctx: t.Context(), post: newPostProcessor(t.Name(), string(cfg), proxy.Default)}
			defer r.post.release()
			body := &trackingBody{Reader: strings.NewReader("PDF bytes")}
			_, err := r.postProcess(connectors.SourceItem{ID: "file", Name: "report.pdf", MIMEType: "application/pdf"}, connectors.SourceContent{Body: body})
			if err == nil || !strings.Contains(err.Error(), "503") || !body.closed {
				t.Fatalf("extraction bypassed or body leaked: %v", err)
			}
		})
	}
}

func TestBinaryExtractionReturnsTextForRetention(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"text":"Parsed document"}]`)
	}))
	defer server.Close()
	cfg, _ := json.Marshal(postprocess.Pipeline{Enabled: true, Mode: "ui", Steps: []postprocess.PipelineStep{{Type: postprocess.StepUnstructured, Enabled: true, URL: server.URL}}})
	r := &runner{ctx: t.Context(), post: newPostProcessor(t.Name(), string(cfg), proxy.Default)}
	defer r.post.release()
	body := &trackingBody{Reader: strings.NewReader("PDF bytes")}
	out, err := r.postProcess(connectors.SourceItem{ID: "file", Name: "report.pdf"}, connectors.SourceContent{Body: body})
	if err != nil || !body.closed || out.Body != nil || len(out.Blocks) != 0 || out.Text != "Parsed document" {
		t.Fatalf("expected extracted text, got %+v, %v", out, err)
	}
}
