package sync

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestLimitedReaderSizeBoundary(t *testing.T) {
	for _, limit := range []int64{0, 1, 512, 1024} {
		for _, size := range []int64{0, max(0, limit-1), limit, limit + 1} {
			t.Run(fmt.Sprintf("limit=%d/size=%d", limit, size), func(t *testing.T) {
				content := strings.Repeat("x", int(size))
				reader := &limitedReader{r: strings.NewReader(content), n: limit}
				read, err := io.ReadAll(reader)
				if size <= limit {
					if err != nil || string(read) != content {
						t.Fatalf("read=%d error=%v, want %d bytes without error", len(read), err, size)
					}
				} else if err == nil || err.Error() != "file exceeds the configured maximum size" {
					t.Fatalf("error=%v, want size limit error", err)
				}
			})
		}
	}
}

func TestLimitedReaderZeroLengthRead(t *testing.T) {
	reader := &limitedReader{r: strings.NewReader("x"), n: 0}
	if n, err := reader.Read(nil); n != 0 || err != nil {
		t.Fatalf("zero-length read=(%d, %v), want (0, nil)", n, err)
	}
	if _, err := io.ReadAll(reader); err == nil {
		t.Fatal("zero-length read must not hide oversized content")
	}
}

func TestLimitedReaderPreservesReadError(t *testing.T) {
	wantErr := errors.New("source read failed")
	reader := &limitedReader{r: failingReader{wantErr}, n: 0}
	if _, err := io.ReadAll(reader); !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want %v", err, wantErr)
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }
