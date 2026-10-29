package sync

import (
	"fmt"
	"github.com/Nyrest/hindsight-ingestion/internal/postprocess"
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

type unreadBody struct {
	testing.TB
	closed bool
}

func (b *unreadBody) Read([]byte) (int, error) {
	b.Fatal("pass-through file was buffered")
	return 0, io.EOF
}
func (b *unreadBody) Close() error { b.closed = true; return nil }

func TestBinaryTextStepsKeepStreaming(t *testing.T) {
	for _, raw := range []string{
		`{"enabled":true,"mode":"ui","steps":[]}`,
		`{"enabled":true,"mode":"ui","steps":[{"type":"trim","enabled":true,"side":"both"}]}`,
	} {
		r := &runner{ctx: t.Context(), post: newPostProcessor(t.Name(), raw, proxy.Default)}
		body := &unreadBody{TB: t}
		out, err := r.postProcess(connectors.SourceItem{ID: "a"}, connectors.SourceContent{Body: body})
		if err != nil || out.Body != body || body.closed {
			t.Fatal("stream ownership changed", err)
		}
		_ = out.Body.Close()
	}
}

func TestPostProcessorRotatesStatefulVM(t *testing.T) {
	r := &runner{ctx: t.Context(), post: newPostProcessor(t.Name(), `{"enabled":true,"mode":"code","code":"let n=0;function process(){return String(++n);}"}`, proxy.Default)}
	defer r.post.release()
	for i := 0; i < 130; i++ {
		out, err := r.postProcess(connectors.SourceItem{ID: "a"}, connectors.SourceContent{Text: "x"})
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprint(i%postprocess.MaxVMDocuments + 1)
		if out.Text != want {
			t.Fatalf("document %d state=%s want=%s", i, out.Text, want)
		}
	}
}

func TestBinaryTransformationInputBound(t *testing.T) {
	r := &runner{ctx: t.Context(), maxSz: 1 << 30, post: newPostProcessor(t.Name(), `{"enabled":true,"mode":"code","code":"function process(c,i){return i;}"}`, proxy.Default)}
	defer r.post.release()
	body := &trackingBody{Reader: strings.NewReader(strings.Repeat("x", postprocess.MaxDocumentBytes+1))}
	_, err := r.postProcess(connectors.SourceItem{ID: "a"}, connectors.SourceContent{Body: body})
	if err == nil || !body.closed {
		t.Fatal("oversized transformation accepted or body leaked", err)
	}
}

type trackingBody struct {
	io.Reader
	closed bool
}

func (b *trackingBody) Close() error { b.closed = true; return nil }

func BenchmarkUIBinaryPassThrough(b *testing.B) {
	data := strings.Repeat("x", 4<<20)
	r := &runner{ctx: b.Context(), post: newPostProcessor("benchmark", `{"enabled":true,"mode":"ui","steps":[]}`, proxy.Default)}
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := r.postProcess(connectors.SourceItem{ID: "a"}, connectors.SourceContent{Body: io.NopCloser(strings.NewReader(data))})
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, out.Body); err != nil {
			b.Fatal(err)
		}
		_ = out.Body.Close()
	}
}
