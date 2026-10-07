package siyuan

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func TestScanAndExport(t *testing.T) {
	var stmts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token tok" {
			io.WriteString(w, `{"code":-1,"msg":"auth failed"}`)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/api/notebook/lsNotebooks":
			io.WriteString(w, `{"code":0,"data":{"notebooks":[{"id":"20240101000000-aaaaaaa","name":"Work"}]}}`)
		case "/api/query/sql":
			stmts = append(stmts, body["stmt"].(string))
			io.WriteString(w, `{"code":0,"data":[{"id":"20240101000000-bbbbbbb","box":"20240101000000-aaaaaaa","hpath":"/Plans","content":"Plans","tag":"#x# #y#","updated":"20240305101010"}]}`)
		case "/api/export/exportMdContent":
			io.WriteString(w, `{"code":0,"data":{"hPath":"/Plans","content":"# Plans\nbody"}}`)
		}
	}))
	defer srv.Close()

	cred := connectors.Credential{Config: map[string]any{"baseUrl": srv.URL}, Secrets: map[string]string{"token": "tok"}}
	c := &Connector{}
	var items []connectors.SourceItem
	res, err := c.Scan(context.Background(), connectors.ScanRequest{
		Credential: cred, Config: map[string]any{"notebookId": "20240101000000-aaaaaaa", "pathPrefix": "/Pl'a"}, Full: true,
		Emit: func(it connectors.SourceItem) error { items = append(items, it); return nil }, Log: func(string, string) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Path != "Work/Plans" || !res.Complete {
		t.Fatalf("items=%+v", items)
	}
	if !strings.Contains(stmts[0], "hpath LIKE '/Pl''a%'") {
		t.Fatalf("prefix not escaped: %s", stmts[0])
	}
	if tags := items[0].Attributes["tags"].([]string); len(tags) != 2 {
		t.Fatalf("tags = %v", tags)
	}

	_, err = c.Scan(context.Background(), connectors.ScanRequest{
		Credential: cred, Config: map[string]any{}, Cursor: res.Cursor,
		Emit: func(connectors.SourceItem) error { return nil }, Log: func(string, string) {},
	})
	if err != nil || !strings.Contains(stmts[len(stmts)-1], "updated >= '20240305101010'") {
		t.Fatalf("incremental stmt: %s (%v)", stmts[len(stmts)-1], err)
	}

	content, err := c.OpenContent(context.Background(), connectors.ContentRequest{Credential: cred, Item: items[0]})
	if err != nil || !strings.HasPrefix(content.Text, "# Plans") {
		t.Fatalf("content = %q %v", content.Text, err)
	}

	if err := c.ValidateConfig(map[string]any{"notebookId": "x' OR 1=1"}, connectors.Filter{}); err == nil {
		t.Fatal("invalid notebook ID accepted")
	}
}
