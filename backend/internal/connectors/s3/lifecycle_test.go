package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/minio/minio-go/v7"
)

func TestListingEarlyExitReleasesProducer(t *testing.T) {
	srv := httptest.NewServer(gofakes3.New(s3mem.New()).Server())
	defer srv.Close()
	cred := connectors.Credential{Config: map[string]any{"endpoint": srv.URL, "accessKeyId": "test", "pathStyle": true, "region": "us-east-1"}, Secrets: map[string]string{"secretAccessKey": "test-secret"}}
	cl, err := newClient(cred)
	if err != nil {
		t.Fatal(err)
	}
	if err := cl.MakeBucket(t.Context(), "lifecycle", minio.MakeBucketOptions{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1010; i++ {
		if _, err := cl.PutObject(t.Context(), "lifecycle", fmt.Sprintf("%04d.txt", i), bytes.NewReader([]byte("x")), 1, minio.PutObjectOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	producers := func() int {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		return strings.Count(string(buf[:n]), "(*Client).ListObjects.func1()")
	}
	before := producers()
	source := &Connector{}
	rejected := errors.New("destination rejected upload")
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithCancel(t.Context())
		_, err := source.Scan(ctx, connectors.ScanRequest{Credential: cred, Config: map[string]any{"bucket": "lifecycle", "recursive": true}, Emit: func(connectors.SourceItem) error { time.Sleep(5 * time.Millisecond); return rejected }})
		cancel()
		if !errors.Is(err, rejected) {
			t.Fatalf("scan: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	result, err := source.Browse(ctx, cred, connectors.BrowseRequest{ParentID: "lifecycle"})
	cancel()
	if err != nil || len(result.Items) != 1000 {
		t.Fatalf("browse count=%d error=%v", len(result.Items), err)
	}
	deadline := time.Now().Add(time.Second)
	for producers() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := producers(); n > before {
		t.Fatalf("listing producers leaked: before=%d after=%d", before, n)
	}
}
