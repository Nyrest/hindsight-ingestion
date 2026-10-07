package s3

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/minio/minio-go/v7"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

// TestS3 runs against an in-process S3-compatible server (gofakes3), or a
// real one (e.g. MinIO) when TEST_S3_ENDPOINT is set.
func TestS3(t *testing.T) {
	endpoint, key, secret := os.Getenv("TEST_S3_ENDPOINT"), os.Getenv("TEST_S3_ACCESS_KEY"), os.Getenv("TEST_S3_SECRET_KEY")
	if endpoint == "" {
		srv := httptest.NewServer(gofakes3.New(s3mem.New()).Server())
		defer srv.Close()
		endpoint, key, secret = srv.URL, "test", "test-secret"
	}
	cred := connectors.Credential{
		Config:  map[string]any{"endpoint": endpoint, "accessKeyId": key, "pathStyle": true, "region": "us-east-1"},
		Secrets: map[string]string{"secretAccessKey": secret},
	}
	ctx := context.Background()
	cl, err := newClient(cred)
	if err != nil {
		t.Fatal(err)
	}
	bucket := "hi-test"
	_ = cl.MakeBucket(ctx, bucket, minio.MakeBucketOptions{})
	put := func(key, body string) {
		if _, err := cl.PutObject(ctx, bucket, key, bytes.NewReader([]byte(body)), int64(len(body)), minio.PutObjectOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	put("docs/a.md", "# A")
	put("docs/sub/b.pdf", "%PDF")
	put("other/c.txt", "c")

	c := &Connector{}
	if msg, err := c.ValidateCredential(ctx, cred); err != nil {
		t.Fatalf("validate: %v", err)
	} else {
		t.Log(msg)
	}

	scan := func(recursive bool) []connectors.SourceItem {
		var items []connectors.SourceItem
		res, err := c.Scan(ctx, connectors.ScanRequest{
			Credential: cred, Config: map[string]any{"bucket": bucket, "prefix": "docs/", "recursive": recursive}, Full: true,
			Emit: func(it connectors.SourceItem) error { items = append(items, it); return nil }, Log: func(string, string) {},
		})
		if err != nil || !res.Complete {
			t.Fatalf("scan: %v complete=%v", err, res.Complete)
		}
		return items
	}
	if items := scan(true); len(items) != 2 {
		t.Fatalf("recursive scan: %+v", items)
	}
	items := scan(false)
	if len(items) != 1 || items[0].ID != bucket+"/docs/a.md" || items[0].Revision == "" {
		t.Fatalf("flat scan: %+v", items)
	}
	content, err := c.OpenContent(ctx, connectors.ContentRequest{Credential: cred, Item: items[0]})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(content.Body)
	content.Body.Close()
	if string(b) != "# A" {
		t.Fatalf("content = %q", b)
	}

	res, err := c.Browse(ctx, cred, connectors.BrowseRequest{ParentID: bucket})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, it := range res.Items {
		names = append(names, it.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "docs") {
		t.Fatalf("browse: %v", names)
	}
}
