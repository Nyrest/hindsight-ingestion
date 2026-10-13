package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeRequiresSingleBoundedJSONValue(t *testing.T) {
	for _, test := range []struct {
		name, body string
		accepted   bool
	}{
		{"valid", `{"name":"task"}` + "\n ", true},
		{"empty", "", false},
		{"multiple", `{"name":"task"} {"name":"other"}`, false},
		{"trailing garbage", `{"name":"task"} invalid`, false},
		{"large trailing whitespace", `{"name":"task"}` + strings.Repeat(" ", 1<<20), false},
		{"secret in invalid body", `{"secret":"private-token","number":wrong}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/tasks", strings.NewReader(test.body))
			response := httptest.NewRecorder()
			var payload map[string]any
			if accepted := decode(response, req, &payload); accepted != test.accepted {
				t.Fatalf("accepted=%v, response=%s", accepted, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "private-token") {
				t.Fatal("decode diagnostic echoed secret input")
			}
		})
	}
}
