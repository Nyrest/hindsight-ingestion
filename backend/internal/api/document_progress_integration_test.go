package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDocumentCleanupLiveProgressAndRetry(t *testing.T) {
	e := newEnv(t)
	listing := make(chan struct{}, 1)
	listRelease := make(chan struct{}, 1)
	deleting := make(chan struct{}, 1)
	deleteRelease := make(chan bool, 1)
	var deleted atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			listing <- struct{}{}
			select {
			case <-listRelease:
			case <-r.Context().Done():
				return
			}
			items := []map[string]any{}
			for i := deleted.Load(); i < 2; i++ {
				items = append(items, map[string]any{"id": fmt.Sprint("doc", i), "tags": []string{r.URL.Query().Get("tags")}})
			}
			json.NewEncoder(w).Encode(map[string]any{"items": items, "total": len(items)})
			return
		}
		deleting <- struct{}{}
		select {
		case success := <-deleteRelease:
			if !success {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		case <-r.Context().Done():
			return
		}
		deleted.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	id := makeTask(t, e, upstream.URL, "filesystem")
	path := "/api/tasks/" + id + "/documents"
	readProgress := func() map[string]any {
		t.Helper()
		var out map[string]any
		if code := e.do("GET", path+"/progress", nil, &out); code != 200 {
			t.Fatal(code, out)
		}
		return out
	}
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("cleanup did not reach upstream")
		}
	}
	start := func() <-chan int {
		result := make(chan int, 1)
		go func() {
			req, _ := http.NewRequestWithContext(t.Context(), "DELETE", e.srv.URL+path, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				result <- 0
				return
			}
			resp.Body.Close()
			result <- resp.StatusCode
		}()
		return result
	}
	finish := func(result <-chan int, expected int) {
		t.Helper()
		select {
		case code := <-result:
			if code != expected {
				t.Fatalf("cleanup returned %d, want %d", code, expected)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cleanup did not finish")
		}
		if out := readProgress(); out["phase"] != "idle" || out["totalCount"] != nil {
			t.Fatal("completed request leaked progress", out)
		}
	}
	if out := readProgress(); out["phase"] != "idle" {
		t.Fatal(out)
	}
	result := start()
	wait(listing)
	if out := readProgress(); out["phase"] != "preparing" || out["totalCount"] != nil || out["estimatedRemainingSeconds"] != nil {
		t.Fatal("preparation", out)
	}
	if code := e.do("DELETE", path, nil, nil); code != 409 {
		t.Fatal("duplicate cleanup was allowed", code)
	}
	listRelease <- struct{}{}
	wait(deleting)
	if out := readProgress(); out["totalCount"] != float64(2) || out["completedCount"] != float64(0) || out["estimatedRemainingSeconds"] != nil {
		t.Fatal("first deletion", out)
	}
	deleteRelease <- true
	wait(deleting)
	if out := readProgress(); out["completedCount"] != float64(1) || out["deletedCount"] != float64(1) || out["estimatedRemainingSeconds"] == nil {
		t.Fatal("partial progress", out)
	}
	deleteRelease <- false
	finish(result, 502)
	result = start()
	wait(listing)
	if out := readProgress(); out["completedCount"] != float64(0) || out["totalCount"] != nil {
		t.Fatal("retry retained stale counts", out)
	}
	listRelease <- struct{}{}
	wait(deleting)
	if out := readProgress(); out["totalCount"] != float64(1) {
		t.Fatal("retry did not recount remaining documents", out)
	}
	deleteRelease <- true
	finish(result, 200)
	result = start()
	wait(listing)
	listRelease <- struct{}{}
	finish(result, 200)
	if code := e.do("GET", "/api/tasks/missing/documents/progress", nil, nil); code != 404 {
		t.Fatal("progress for nonexistent task", code)
	}
}
