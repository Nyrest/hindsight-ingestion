package sync_test

import (
	"path/filepath"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/sync"
)

func TestCanonicalIdentity(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name, source string
		credential   connectors.Credential
		config       map[string]any
		item         connectors.SourceItem
		want         string
	}{
		{"notion", "notion", connectors.Credential{}, nil, connectors.SourceItem{ID: "page-id"}, "notion_page:page-id"},
		{"siyuan instance", "siyuan", connectors.Credential{Config: map[string]any{"instanceId": "notes", "baseUrl": "https://siyuan.example"}}, nil, connectors.SourceItem{ID: "doc"}, "siyuan:notes:doc"},
		{"siyuan URL", "siyuan", connectors.Credential{Config: map[string]any{"baseUrl": "https://siyuan.example:6806/"}}, nil, connectors.SourceItem{ID: "doc"}, "siyuan:siyuan.example:6806:doc"},
		{"s3", "s3", connectors.Credential{Config: map[string]any{"endpoint": "http://storage.example:9000/"}}, map[string]any{"bucket": "notes"}, connectors.SourceItem{ID: "notes/a:b.txt", Path: "a:b.txt"}, "s3:storage.example:9000:notes:a:b.txt"},
		{"AWS", "s3", connectors.Credential{}, map[string]any{"bucket": "notes"}, connectors.SourceItem{Path: "a.txt"}, "s3:s3.amazonaws.com:notes:a.txt"},
		{"webdav", "webdav", connectors.Credential{Config: map[string]any{"baseUrl": "https://cloud.example/dav/"}}, nil, connectors.SourceItem{Path: "/notes/a.txt"}, "webdav:cloud.example/dav:/notes/a.txt"},
		{"google drive", "google_drive", connectors.Credential{}, nil, connectors.SourceItem{ID: "file", Metadata: map[string]string{"google_drive_id": "drive"}}, "google_drive:drive:file"},
		{"google configured drive", "google_drive", connectors.Credential{}, map[string]any{"driveId": "drive"}, connectors.SourceItem{ID: "file"}, "google_drive:drive:file"},
		{"onedrive", "onedrive", connectors.Credential{}, nil, connectors.SourceItem{ID: "drive!item", Metadata: map[string]string{"onedrive_drive_id": "drive", "onedrive_item_id": "item"}}, "onedrive:drive:item"},
		{"onedrive deletion", "onedrive", connectors.Credential{}, nil, connectors.SourceItem{ID: "drive!item", Deleted: true}, "onedrive:drive:item"},
		{"filesystem", "filesystem", connectors.Credential{Config: map[string]any{"rootPath": root}}, nil, connectors.SourceItem{ID: "notes/a.txt"}, "filesystem:" + filepath.Join(root, "notes", "a.txt")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sync.CanonicalIdentity(tt.source, tt.credential, tt.config, tt.item); got != tt.want {
				t.Fatalf("identity = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIdentityURLSchemeIndependence(t *testing.T) {
	for _, source := range []string{"siyuan", "s3", "webdav"} {
		t.Run(source, func(t *testing.T) {
			var previous string
			for _, endpoint := range []string{"https://host.example/service/", "http://host.example/service", "host.example/service", "HTTPS://host.example/service/"} {
				cred := connectors.Credential{Config: map[string]any{"baseUrl": endpoint, "endpoint": endpoint}}
				got := sync.CanonicalIdentity(source, cred, map[string]any{"bucket": "bucket"}, connectors.SourceItem{ID: "doc", Path: "file"})
				if previous != "" && got != previous {
					t.Fatalf("scheme changed identity: %q != %q", got, previous)
				}
				previous = got
			}
		})
	}
}
