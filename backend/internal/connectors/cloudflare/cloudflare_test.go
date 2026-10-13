package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func testCredential() connectors.Credential {
	return connectors.Credential{Config: map[string]any{"accountId": strings.Repeat("a", 32), "apiToken": "secret"}}
}

func testRequest() connectors.ScanRequest {
	return connectors.ScanRequest{Credential: testCredential(), Config: map[string]any{"url": "https://example.com/docs/"}, Full: true, Filter: connectors.Filter{Mode: "simple"}}
}

func TestRequestModesAndWatermark(t *testing.T) {
	c := &Connector{}
	req := testRequest()
	body, job, err := c.crawlRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if !job.Full || job.MaxAge != 0 || body["source"] != "all" || body["render"] != false || body["limit"] != int64(100) || body["depth"] != int64(20) || body["contentUse"] != "reference" {
		t.Fatalf("unexpected default request: %#v", body)
	}
	for _, key := range []string{"gotoOptions", "waitForSelector", "waitForTimeout", "rejectResourceTypes", "modifiedSince"} {
		if _, ok := body[key]; ok {
			t.Fatalf("static crawl sent %s", key)
		}
	}
	req.Config["render"] = true
	req.Config["source"] = "sitemaps"
	req.Config["waitForSelector"] = "article"
	req.Config["includePatterns"] = []any{"https://example.com/docs/**"}
	body, _, err = c.crawlRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if body["gotoOptions"].(map[string]any)["timeout"] != int64(60000) || body["waitForSelector"].(map[string]any)["selector"] != "article" || len(body["rejectResourceTypes"].([]string)) != 3 {
		t.Fatalf("bad render options: %#v", body)
	}
	previous := watermark{StartedAt: time.Now().UTC().Add(-time.Hour), MaxAge: 3600}
	req.Full = false
	req.Cursor, _ = json.Marshal(previous)
	body, job, err = c.crawlRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if job.Full || body["maxAge"] != int64(3600) || body["modifiedSince"] != previous.StartedAt.Add(-70*time.Minute).Unix() {
		t.Fatalf("bad incremental request: %#v", body)
	}
	previous.StartedAt = time.Now().AddDate(-2, 0, 0)
	req.Cursor, _ = json.Marshal(previous)
	body, job, err = c.crawlRequest(req)
	if err != nil || !job.Full || body["maxAge"] != int64(0) || body["modifiedSince"] != nil {
		t.Fatalf("old watermark did not force fresh full: %#v %v", body, err)
	}
}

func TestIdentityAndPageValidation(t *testing.T) {
	record := crawlRecord{URL: "https://example.com/redirect", Status: "completed", Markdown: "# Content"}
	record.Metadata.URL = "https://EXAMPLE.com:443/" + strings.Repeat("long", 200) + "#part"
	record.Metadata.Title = "Title"
	record.Metadata.Status = 200
	item, err := recordItem(record, testRequest().Config)
	if err != nil || len(item.ID) != 64 || item.ContentError != "" || len(item.Metadata["url"]) < 512 {
		t.Fatalf("bad long URL item: %#v %v", item, err)
	}
	original := item.Revision
	record.Metadata.Title = "New title"
	next, _ := recordItem(record, testRequest().Config)
	if next.ID != item.ID || next.Revision != original {
		t.Fatal("title changed body revision or identity")
	}
	for _, status := range []string{"errored", "disallowed", "queued", "unknown"} {
		record.Status = status
		bad, _ := recordItem(record, testRequest().Config)
		if bad.ContentError == "" {
			t.Fatalf("accepted status %s", status)
		}
	}
	record.Status = "completed"
	record.Markdown = " "
	bad, _ := recordItem(record, testRequest().Config)
	if bad.ContentError == "" {
		t.Fatal("accepted empty Markdown")
	}
	record.Markdown = strings.Repeat("x", (1<<20)+1)
	bad, _ = recordItem(record, map[string]any{"maxPageSizeMB": 1})
	if bad.ContentError == "" {
		t.Fatal("accepted oversized Markdown")
	}
}

func TestResultPaginationAndCompleteness(t *testing.T) {
	for _, cursor := range []string{`2`, `"2"`, `"opaque-next"`} {
		t.Run(cursor, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				suffix := `"cursor":null`
				pageURL := "https://example.com/docs/two"
				if requests == 1 {
					suffix = `"cursor":` + cursor
					pageURL = "https://example.com/docs/one"
				} else if r.URL.Query().Get("cursor") == "" {
					t.Error("missing second page cursor")
				}
				fmt.Fprintf(w, `{"success":true,"result":{"id":"job","status":"completed","total":2,"finished":2,%s,"records":[{"url":%q,"status":"completed","markdown":"text","metadata":{"status":200}}]}}`, suffix, pageURL)
			}))
			defer server.Close()
			c := &Connector{APIBase: server.URL}
			req := testRequest()
			items := []connectors.SourceItem{}
			req.Emit = func(item connectors.SourceItem) error { items = append(items, item); return nil }
			result, err := c.ReadJob(t.Context(), req, connectors.SourceJob{ID: "job", Full: true, StartedAt: time.Now()})
			if err != nil || result.Error != nil || !result.Complete || len(items) != 2 || requests != 2 {
				t.Fatalf("pagination: %#v %v items=%d", result, err, len(items))
			}
		})
	}
	cases := []struct {
		name, status, markdown string
		total, finished, limit int
		outside                bool
		complete               bool
	}{
		{"complete", "completed", "body", 1, 1, 100, false, true},
		{"limit", "completed", "body", 1, 1, 1, false, false},
		{"failed", "errored", "", 1, 1, 100, false, false},
		{"blocked", "disallowed", "", 1, 1, 100, false, false},
		{"empty", "completed", "", 1, 1, 100, false, false},
		{"unknown", "mystery", "", 1, 1, 100, false, false},
		{"in-scope-skip", "skipped", "", 0, 0, 100, false, false},
		{"outside-skip", "skipped", "", 0, 0, 100, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rawURL := "https://example.com/docs/a"
			if tc.outside {
				rawURL = "https://other.example/a"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"success":true,"result":{"id":"job","status":"completed","total":%d,"finished":%d,"records":[{"url":%q,"status":%q,"markdown":%q,"metadata":{"status":200}}]}}`, tc.total, tc.finished, rawURL, tc.status, tc.markdown)
			}))
			defer server.Close()
			req := testRequest()
			req.Config["limit"] = tc.limit
			req.Emit = func(connectors.SourceItem) error { return nil }
			result, err := (&Connector{APIBase: server.URL}).ReadJob(t.Context(), req, connectors.SourceJob{ID: "job", Full: true})
			if err != nil || result.Complete != tc.complete || (result.Error == nil) != tc.complete {
				t.Fatalf("result: %#v %v", result, err)
			}
		})
	}
}

func TestCreationNeverRetriesAndTokenCheckIsReadOnly(t *testing.T) {
	posts, gets := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing authentication")
		}
		if r.Method == http.MethodPost {
			posts++
			if r.URL.Path != "/accounts/"+strings.Repeat("a", 32)+"/browser-rendering/crawl" {
				t.Error("creation used an unexpected API path", r.URL.Path)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["render"] != false || body["contentUse"] != "reference" || body["formats"].([]any)[0] != "markdown" || body["crawlPurposes"].([]any)[0] != "ai-input" {
				t.Error("incorrect wire request", body)
			}
			w.WriteHeader(503)
			return
		}
		gets++
		if r.URL.Path != "/user/tokens/verify" {
			t.Errorf("verification touched %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"success":true,"result":{"status":"active"}}`)
	}))
	defer server.Close()
	c := &Connector{APIBase: server.URL}
	if _, err := c.ValidateCredential(t.Context(), testCredential()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartJob(t.Context(), testRequest()); err == nil {
		t.Fatal("accepted failed creation")
	}
	if gets != 1 || posts != 1 {
		t.Fatalf("gets=%d posts=%d", gets, posts)
	}
	// A connection lost after submission is also ambiguous and must not replay POST.
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		connection, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			connection.Close()
		}
	})
	if _, err := c.StartJob(t.Context(), testRequest()); err == nil || posts != 2 {
		t.Fatalf("ambiguous POST replayed: %d %v", posts, err)
	}
}

func TestAccountTokenValidationUsesReadOnlyVerificationFallback(t *testing.T) {
	paths := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("verification was not read-only")
		}
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/user/tokens/verify" {
			fmt.Fprint(w, `{"success":false,"errors":[{"code":1000,"message":"not a user token"}]}`)
			return
		}
		fmt.Fprint(w, `{"success":true,"result":{"status":"active"}}`)
	}))
	defer server.Close()
	if _, err := (&Connector{APIBase: server.URL}).ValidateCredential(t.Context(), testCredential()); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[1] != "/accounts/"+strings.Repeat("a", 32)+"/tokens/verify" {
		t.Fatal(paths)
	}
}

func TestIncrementalMissingResultsAndRepeatedCursorAreFailures(t *testing.T) {
	for _, repeated := range []bool{false, true} {
		t.Run(fmt.Sprint(repeated), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				cursor := "null"
				if repeated {
					cursor = `"again"`
				}
				fmt.Fprintf(w, `{"success":true,"result":{"id":"job","status":"completed","total":2,"finished":2,"cursor":%s,"records":[]}}`, cursor)
			}))
			defer server.Close()
			req := testRequest()
			req.Emit = func(connectors.SourceItem) error { return nil }
			result, err := (&Connector{APIBase: server.URL}).ReadJob(t.Context(), req, connectors.SourceJob{ID: "job", Full: false})
			if result.Complete || (err == nil && result.Error == nil) {
				t.Fatal("incomplete/repeated results accepted", result)
			}
		})
	}
}

func TestSkippedRecordsMustBeFullyRead(t *testing.T) {
	for _, full := range []bool{false, true} {
		for _, returned := range []bool{false, true} {
			t.Run(fmt.Sprintf("full=%t/returned=%t", full, returned), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					records, cursor := `[]`, `null`
					if r.URL.Query().Get("cursor") == "" {
						records = `[{"url":"https://example.com/docs/a","status":"completed","markdown":"body","metadata":{"status":200}}]`
						cursor = `"next"`
					} else if returned {
						records = `[{"url":"https://other.example/outside","status":"skipped"}]`
					}
					fmt.Fprintf(w, `{"success":true,"result":{"id":"job","status":"completed","total":1,"finished":1,"skipped":1,"cursor":%s,"records":%s}}`, cursor, records)
				}))
				defer server.Close()
				emitted := 0
				req := testRequest()
				req.Emit = func(connectors.SourceItem) error { emitted++; return nil }
				result, err := (&Connector{APIBase: server.URL}).ReadJob(t.Context(), req, connectors.SourceJob{ID: "job", Full: full})
				if err != nil || emitted != 1 || result.Complete != (full && returned) || (result.Error == nil) != returned {
					t.Fatalf("skipped inventory: %#v %v emitted=%d", result, err, emitted)
				}
			})
		}
	}
}

func TestPollingRetriesCancellationAndTerminalStates(t *testing.T) {
	for _, code := range []int{429, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				if attempts == 1 {
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(code)
					return
				}
				fmt.Fprint(w, `{"success":true,"result":{"id":"job","status":"completed","total":2,"finished":2}}`)
			}))
			defer server.Close()
			_, err := (&Connector{APIBase: server.URL}).PollJob(t.Context(), testCredential(), connectors.SourceJob{ID: "job"})
			if err != nil || attempts != 2 {
				t.Fatalf("retry: %d %v", attempts, err)
			}
		})
	}
	for _, status := range []string{"cancelled_due_to_timeout", "cancelled_due_to_limits", "cancelled_by_user", "errored", "unknown"} {
		t.Run(status, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"success":true,"result":{"id":"job","status":%q}}`, status)
			}))
			defer server.Close()
			if _, err := (&Connector{APIBase: server.URL}).PollJob(t.Context(), testCredential(), connectors.SourceJob{ID: "job"}); err == nil {
				t.Fatal("accepted terminal failure")
			}
		})
	}
	for _, code := range []int{404, 410} {
		t.Run(fmt.Sprintf("expired=%d", code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
			defer server.Close()
			c := &Connector{APIBase: server.URL}
			job, err := c.PollJob(context.Background(), testCredential(), connectors.SourceJob{ID: "expired"})
			if err == nil || job.Status != "expired" {
				t.Fatal("accepted expired job", job, err)
			}
			if err := c.CancelJob(t.Context(), testCredential(), job); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConfigurationRejectsInvalidListsAndBounds(t *testing.T) {
	for key, value := range map[string]any{"limit": 100001, "maxAge": 604801, "depth": 1.5, "includePatterns": []any{42}, "excludePatterns": []string{" "}, "source": "invalid"} {
		req := testRequest()
		req.Config[key] = value
		if err := (&Connector{}).ValidateConfig(req.Config, req.Filter); err == nil {
			t.Errorf("accepted %s=%v", key, value)
		}
	}
	req := testRequest()
	req.Config["render"] = true
	req.Config["rejectResourceTypes"] = []any{"not-a-resource"}
	if err := (&Connector{}).ValidateConfig(req.Config, req.Filter); err == nil {
		t.Fatal("accepted invalid resources")
	}
}
