package sync_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/sync"
)

func TestDocumentID(t *testing.T) {
	const wantHash = "4c7f58f159515e2094f0bb73b213a31dff673c85002504a56f1a4149d46762db"
	for _, source := range []string{"notion", "siyuan", "s3", "webdav", "google_drive", "onedrive", "filesystem", "cloudflare_crawl", "custom"} {
		t.Run(source, func(t *testing.T) {
			id := sync.DocumentID(source, "task", "item")
			if id != source+":"+wantHash {
				t.Fatalf("document ID = %q, want %q", id, source+":"+wantHash)
			}
			if id != sync.DocumentID(source, "task", "item") {
				t.Fatal("same task and item changed ID")
			}
			if id == sync.DocumentID(source, "other-task", "item") || id == sync.DocumentID(source, "task", "other-item") {
				t.Fatal("different task or item reused ID")
			}
		})
	}
}

func TestDocumentIDPreservesTupleBoundaries(t *testing.T) {
	pairs := [][2]string{{"ab", "c"}, {"a", "bc"}, {"a:b", "c"}, {"a", "b:c"}, {"a\x00b", "c"}, {"a", "b\x00c"}, {"a\"", "b"}, {"a", "\"b"}, {"", "a"}, {"a", ""}, {"task", "\xff"}, {"task", "\xfe"}, {"task", "笔记/文件.txt"}, {"task", strings.Repeat("long/path/", 1000)}}
	seen := map[string]bool{}
	for _, pair := range pairs {
		id := sync.DocumentID("source", pair[0], pair[1])
		if seen[id] {
			t.Fatalf("ambiguous tuple: %q", pair)
		}
		seen[id] = true
		digest, err := hex.DecodeString(strings.TrimPrefix(id, "source:"))
		if err != nil || len(digest) != 32 {
			t.Fatalf("invalid SHA-256 ID: %q", id)
		}
	}
}
