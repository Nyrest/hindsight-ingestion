package httpx

import (
	"errors"
	"io"
)

// MaxJSONResponseBytes bounds metadata responses independently of streamed files.
const MaxJSONResponseBytes = 64 << 20

var ErrResponseTooLarge = errors.New("upstream response exceeds size limit")

type responseReader struct {
	reader    io.Reader
	remaining int64
}

// LimitResponse detects overflow instead of treating a truncated response as EOF.
// The limit applies to decoded/decompressed bytes read by the caller.
func LimitResponse(reader io.Reader, limit int64) io.Reader {
	return &responseReader{reader: reader, remaining: limit}
}

func (r *responseReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if int64(len(p)) > r.remaining+1 {
		p = p[:r.remaining+1]
	}
	n, err := r.reader.Read(p)
	if int64(n) > r.remaining {
		return 0, ErrResponseTooLarge
	}
	r.remaining -= int64(n)
	return n, err
}
