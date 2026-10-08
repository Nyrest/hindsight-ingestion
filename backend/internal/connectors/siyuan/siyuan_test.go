package siyuan

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func TestInlineAssetAuthenticationAndLocation(t *testing.T) {
	var received []string
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Secret") != "" {
			t.Error("source credentials leaked")
		}
		w.Write(append([]byte{137, 80, 78, 71, 13, 10, 26, 10}, bytes.Repeat([]byte{0}, 24)...))
	}))
	defer outside.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token secret" || r.Header.Get("X-Secret") != "custom" {
			t.Error("source auth missing")
		}
		if r.URL.Path == "/prefix/api/export/exportMdContent" {
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"content": "before ![local](assets/a.png) ![redirect](/assets/redirect.png) ![outside](" + outside.URL + "/x.png) after"}})
			return
		}
		received = append(received, r.URL.String())
		if r.URL.Query().Get("box") != "notebook" {
			t.Error("notebook location lost")
		}
		if r.URL.Path == "/prefix/assets/redirect.png" {
			http.Redirect(w, r, outside.URL+"/redirected", http.StatusFound)
			return
		}
		w.Write(append([]byte{137, 80, 78, 71, 13, 10, 26, 10}, bytes.Repeat([]byte{0}, 24)...))
	}))
	defer source.Close()
	req := connectors.ContentRequest{Credential: connectors.Credential{Config: map[string]any{"baseUrl": source.URL + "/prefix"}, Secrets: map[string]string{"token": "secret"}, Headers: map[string]string{"X-Secret": "custom"}}, Item: connectors.SourceItem{ID: "doc", Metadata: map[string]string{"siyuan_notebook_id": "notebook"}}, InlineMultimodalEnabled: true, FilePolicy: connectors.FilePolicy{Images: true}, MaxFileSize: 1000}
	content, err := (&Connector{}).OpenContent(t.Context(), req)
	if err != nil || len(content.Warnings) != 0 || len(received) != 2 {
		t.Fatalf("content=%+v received=%v error=%v", content, received, err)
	}
	images := 0
	for _, block := range content.Blocks {
		if block.Type == "image" {
			images++
		}
	}
	if images != 3 {
		t.Fatalf("images=%d", images)
	}
	req.InlineMultimodalEnabled = false
	if content, err = (&Connector{}).OpenContent(t.Context(), req); err != nil || len(content.Blocks) != 0 || len(received) != 2 {
		t.Fatal("disabled switch downloaded images")
	}
	if _, err = fetchAsset(t.Context(), req, "assets/../private"); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestGlobalAndExplicitAssetLocations(t *testing.T) {
	var locations []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		locations = append(locations, r.URL.RawQuery)
		if r.URL.Query().Get("box") == "notebook" {
			w.WriteHeader(403)
			return
		}
		w.Write([]byte("image"))
	}))
	defer srv.Close()
	req := connectors.ContentRequest{Credential: connectors.Credential{Config: map[string]any{"baseUrl": srv.URL}, Secrets: map[string]string{"token": "secret"}}, Item: connectors.SourceItem{Metadata: map[string]string{"siyuan_notebook_id": "notebook"}}}
	resp, err := fetchAsset(t.Context(), req, "./assets/global.png")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(locations) != 2 || locations[0] != "box=notebook" || locations[1] != "dataPath=assets%2Fglobal.png" {
		t.Fatalf("global fallback: %v", locations)
	}
	locations = nil
	resp, err = fetchAsset(t.Context(), req, "assets/global.png?dataPath=assets%2Fglobal.png")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(locations) != 1 || locations[0] != "dataPath=assets%2Fglobal.png" {
		t.Fatal("explicit global path overwritten")
	}
	locations = nil
	_, err = fetchAsset(t.Context(), req, "assets/local.png?box=notebook")
	if err == nil || len(locations) != 1 {
		t.Fatal("explicit notebook fell back to global")
	}
}

func TestEscapedInlineAssetFilename(t *testing.T) {
	downloads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token secret" {
			t.Error("missing source authentication")
		}
		switch r.URL.Path {
		case "/api/export/exportMdContent":
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"content": `before ![diagram](assets/a\(b\).png) after`}})
		case "/assets/a(b).png":
			downloads++
			if r.URL.Query().Get("box") != "notebook" {
				t.Error("notebook location lost")
			}
			w.Write(append([]byte{137, 80, 78, 71, 13, 10, 26, 10}, bytes.Repeat([]byte{0}, 24)...))
		default:
			t.Errorf("unexpected image path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	content, err := (&Connector{}).OpenContent(t.Context(), connectors.ContentRequest{
		Credential:              connectors.Credential{Config: map[string]any{"baseUrl": srv.URL}, Secrets: map[string]string{"token": "secret"}},
		Item:                    connectors.SourceItem{ID: "doc", Metadata: map[string]string{"siyuan_notebook_id": "notebook"}},
		InlineMultimodalEnabled: true, FilePolicy: connectors.FilePolicy{Images: true}, MaxFileSize: 1000,
	})
	if err != nil || len(content.Warnings) != 0 || downloads != 1 || len(content.Blocks) != 4 || content.Blocks[2].Type != "image" {
		t.Fatalf("escaped asset: downloads=%d content=%+v err=%v", downloads, content, err)
	}
}

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
