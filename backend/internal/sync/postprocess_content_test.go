package sync

import (
	"io"
	"strings"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/proxy"
)

func TestPostProcessingBinaryPreservesSourceMetadata(t *testing.T) {
	r := &runner{ctx: t.Context(), task: models.Task{ID: t.Name()},
		post: newPostProcessor(t.Name(), `{"enabled":true,"mode":"code","code":"function process(c, input) { return input; }"}`, proxy.Default)}
	defer r.post.release()
	item := connectors.SourceItem{ID: "a", Name: "original.pdf", MIMEType: "application/pdf"}
	out, err := r.postProcess(item, connectors.SourceContent{Body: io.NopCloser(strings.NewReader("PDF"))})
	if err != nil {
		t.Fatal(err)
	}
	if out.Body != nil || len(out.Blocks) != 1 {
		t.Fatalf("binary output is not a recoverable attachment: %+v", out)
	}
	block := out.Blocks[0]
	if block.FileName != "original.pdf" || block.Source == nil || block.Source.MediaType != "application/pdf" || block.Source.Data != "UERG" {
		t.Fatalf("source metadata or binary content changed: %+v", block)
	}
}
