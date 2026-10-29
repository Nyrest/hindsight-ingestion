package httpx

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestResponseByteBoundary(t *testing.T) {
	for _, size := range []int{0, 15, 16, 17, 100} {
		data, err := io.ReadAll(LimitResponse(strings.NewReader(strings.Repeat("x", size)), 16))
		if size <= 16 {
			if err != nil || len(data) != size {
				t.Fatal(size, len(data), err)
			}
		} else if !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("overflow=%v", err)
		}
	}
}
