package remotefs

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

type testClient struct {
	directories map[string][]Entry
	failures    map[string]error
	opened      []string
	closed      bool
}

func (c *testClient) List(_ context.Context, p string) ([]Entry, error) {
	return c.directories[p], c.failures[p]
}
func (c *testClient) Open(_ context.Context, p string) (io.ReadCloser, error) {
	c.opened = append(c.opened, p)
	return io.NopCloser(strings.NewReader("content")), nil
}
func (c *testClient) Close() error { c.closed = true; return nil }

func testSource(c *testClient) *Source {
	return &Source{Type: "sftp", Name: "SFTP", Dial: func(context.Context, connectors.Credential) (Client, error) { return c, nil }}
}

func TestInventoryAndConnectionReuse(t *testing.T) {
	c := &testClient{directories: map[string][]Entry{
		"/":       {{Name: "报告.PDF", Regular: true, Size: 7, Modified: time.Unix(100, 0)}, {Name: "nested", Directory: true}, {Name: "link"}},
		"/nested": {{Name: "a.txt", Regular: true, Size: 7, Modified: time.Unix(101, 0)}},
	}}
	s := testSource(c)
	var observed []connectors.SourceItem
	req := connectors.ScanRequest{Credential: connectors.Credential{ID: "cred", Config: map[string]any{"host": "example.com"}}}
	req.Emit = func(item connectors.SourceItem) error {
		observed = append(observed, item)
		content, err := s.OpenContent(context.Background(), connectors.ContentRequest{Credential: req.Credential, Item: item})
		if err != nil {
			return err
		}
		defer content.Body.Close()
		body, err := io.ReadAll(content.Body)
		if string(body) != "content" {
			t.Errorf("body=%q", body)
		}
		return err
	}
	res, err := s.Scan(context.Background(), req)
	if err != nil || !res.Complete || len(observed) != 2 || !c.closed {
		t.Fatalf("scan=%+v err=%v items=%v closed=%v", res, err, observed, c.closed)
	}
	if observed[0].Path != "/报告.PDF" || observed[0].Revision != "100000000000:7" {
		t.Fatalf("item=%+v", observed[0])
	}
	if len(c.opened) != 2 {
		t.Fatalf("opened=%v", c.opened)
	}
}

func TestInventoryCompletion(t *testing.T) {
	cases := []struct {
		name     string
		c        testClient
		cfg      map[string]any
		complete bool
		wantErr  bool
	}{
		{name: "empty", complete: true},
		{name: "failed root", c: testClient{failures: map[string]error{"/": errors.New("denied")}}, wantErr: true},
		{name: "failed descendant", c: testClient{directories: map[string][]Entry{"/": {{Name: "private", Directory: true}}}, failures: map[string]error{"/private": errors.New("denied")}}, wantErr: true},
		{name: "removed root", cfg: map[string]any{"rootPath": "/gone"}, c: testClient{failures: map[string]error{"/gone": errors.New("550 unavailable")}}, complete: true},
		{name: "removed ancestor", cfg: map[string]any{"rootPath": "/gone/child"}, c: testClient{failures: map[string]error{"/gone": errors.New("missing"), "/gone/child": errors.New("missing")}}, complete: true},
		{name: "denied existing root", cfg: map[string]any{"rootPath": "/private"}, c: testClient{directories: map[string][]Entry{"/": {{Name: "private", Directory: true}}}, failures: map[string]error{"/private": errors.New("denied")}}, wantErr: true},
		{name: "nonrecursive", cfg: map[string]any{"recursive": false}, c: testClient{directories: map[string][]Entry{"/": {{Name: "private", Directory: true}}}, failures: map[string]error{"/private": errors.New("denied")}}, complete: true},
		{name: "invalid entry", c: testClient{directories: map[string][]Entry{"/": {{Name: "../outside", Regular: true}}}}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := testSource(&tc.c).Scan(context.Background(), connectors.ScanRequest{Config: tc.cfg, Emit: func(connectors.SourceItem) error { return nil }})
			if (err != nil) != tc.wantErr || res.Complete != tc.complete {
				t.Fatalf("res=%+v err=%v", res, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := testSource(&testClient{}).Scan(ctx, connectors.ScanRequest{})
	if !errors.Is(err, context.Canceled) || res.Complete {
		t.Fatalf("cancelled res=%+v err=%v", res, err)
	}
}

func TestIdentityAndBrowse(t *testing.T) {
	cred := connectors.Credential{ID: "c", Config: map[string]any{"host": "HOST"}}
	first := Identity(cred, "sftp", "/A.txt")
	if len(first) != 64 || first == Identity(cred, "sftp", "/a.txt") || first == Identity(cred, "sftp", "/nested/A.txt") {
		t.Fatal("path identities collide")
	}
	cred.Config["host"] = "host"
	cred.Config["port"] = 22
	if first != Identity(cred, "sftp", "/A.txt") {
		t.Fatal("default port or hostname case changed identity")
	}
	c := &testClient{directories: map[string][]Entry{"/": {{Name: "z", Directory: true}, {Name: "a", Directory: true}, {Name: "file", Regular: true}}}}
	s := testSource(c)
	res, err := s.Browse(context.Background(), cred, connectors.BrowseRequest{Limit: 1})
	if err != nil || len(res.Items) != 1 || res.Items[0].Path != "/a" || res.NextCursor != "1" {
		t.Fatalf("browse=%+v err=%v", res, err)
	}
	res, err = s.Browse(context.Background(), cred, connectors.BrowseRequest{Limit: 1, Cursor: res.NextCursor})
	if err != nil || len(res.Items) != 1 || res.Items[0].Path != "/z" || res.NextCursor != "" {
		t.Fatalf("browse=%+v err=%v", res, err)
	}
}
