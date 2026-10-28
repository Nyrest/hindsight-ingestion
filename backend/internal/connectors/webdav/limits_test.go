package webdav

import (
	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"strings"
	"testing"
)

func TestPartialMultistatusNeverReturnsInventory(t *testing.T) {
	cl, err := newClient(connectors.Credential{Config: map[string]any{"baseUrl": "https://dav.example"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`<d:multistatus xmlns:d="DAV:"><d:response><d:href>/a</d:href></d:response>`,
		`<html><body>upstream error</body></html>`,
	} {
		entries, token, err := cl.parse(strings.NewReader(body))
		if err == nil || len(entries) != 0 || token != "" {
			t.Fatal("partial response authorized inventory", len(entries), token, err)
		}
	}
}

func TestMultistatusEntryBound(t *testing.T) {
	cl, err := newClient(connectors.Credential{Config: map[string]any{"baseUrl": "https://dav.example"}})
	if err != nil {
		t.Fatal(err)
	}
	body := `<d:multistatus xmlns:d="DAV:">` + strings.Repeat(`<d:response><d:href>/a</d:href></d:response>`, maxMetadataEntries+1) + `</d:multistatus>`
	entries, _, err := cl.parse(strings.NewReader(body))
	if err == nil || len(entries) != 0 {
		t.Fatal("oversized inventory accepted", len(entries), err)
	}
}
