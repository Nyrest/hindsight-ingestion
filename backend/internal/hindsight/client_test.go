package hindsight

import (
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func TestClient(t *testing.T) {
	var gotFile, gotMeta, gotAuth, gotCustom string
	var retainBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("X-Custom")
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/default/banks/b1/memories":
			json.NewDecoder(r.Body).Decode(&retainBody)
			io.WriteString(w, `{"success":true,"bank_id":"b1","items_count":1,"async":true,"operation_id":"op1"}`)
		case r.Method == "POST" && r.URL.Path == "/v1/default/banks/b1/files/retain":
			mr, err := r.MultipartReader()
			if err != nil {
				t.Error(err)
				return
			}
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				b, _ := io.ReadAll(p)
				if p.FormName() == "files" {
					gotFile = p.FileName() + ":" + string(b)
				} else {
					gotMeta = string(b)
				}
			}
			io.WriteString(w, `{"operation_ids":["op2"]}`)
		case r.Method == "DELETE":
			w.WriteHeader(404)
		case r.URL.Path == "/v1/default/banks/b1/operations/op1":
			io.WriteString(w, `{"operation_id":"op1","status":"completed"}`)
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()

	c, err := NewClient(connectors.Credential{
		Config:  map[string]any{"baseUrl": srv.URL + "/"},
		Secrets: map[string]string{"apiKey": "k"},
		Headers: map[string]string{"X-Custom": "1", "Authorization": "should-be-overridden"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ops, err := c.RetainBatch(ctx, "b1", []MemoryItem{{Content: "x", DocumentID: "d1", Strategy: "s"}}, "opid")
	if err != nil || len(ops) != 1 || ops[0] != "op1" {
		t.Fatalf("retain: %v %v", ops, err)
	}
	if gotAuth != "Bearer k" || gotCustom != "1" {
		t.Fatalf("headers: auth=%q custom=%q (auth must win over custom)", gotAuth, gotCustom)
	}
	if retainBody["operation_id"] != "opid" || retainBody["async"] != true {
		t.Fatalf("retain body: %v", retainBody)
	}

	ops, err = c.RetainFile(ctx, "b1", FileMeta{DocumentID: "d2", Tags: []string{"t"}}, `we"ird.pdf`, "application/pdf", strings.NewReader("PDFDATA"))
	if err != nil || len(ops) != 1 {
		t.Fatalf("file retain: %v %v", ops, err)
	}
	if gotFile != `we"ird.pdf:PDFDATA` || !strings.Contains(gotMeta, `"document_id":"d2"`) {
		t.Fatalf("multipart: file=%q meta=%q", gotFile, gotMeta)
	}
	if err := c.DeleteDocument(ctx, "b1", "missing"); err != nil {
		t.Fatalf("delete 404 should be nil: %v", err)
	}
	st, err := c.GetOperation(ctx, "b1", "op1")
	if err != nil || st.Status != "completed" {
		t.Fatalf("op: %v %v", st, err)
	}
	_ = multipart.ErrMessageTooLarge
}
