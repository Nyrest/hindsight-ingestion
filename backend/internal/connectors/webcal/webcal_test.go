package webcal

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func fixture(components ...string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//WebCAL tests//EN\r\n" + strings.Join(components, "\r\n") + "\r\nEND:VCALENDAR\r\n"
}

func event(uid, fields string) string {
	return "BEGIN:VEVENT\r\nUID:" + uid + "\r\nSUMMARY:Meeting\r\n" + fields + "\r\nEND:VEVENT"
}

func parsedItems(t testing.TB, body, zone string) ([]connectors.SourceItem, []string, error) {
	t.Helper()
	calendar, err := parseCalendar([]byte(body), 20000)
	if err != nil {
		return nil, nil, err
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	return calendarItems(calendar, location)
}

func itemContent(t testing.TB, calendarItem connectors.SourceItem) connectors.SourceContent {
	t.Helper()
	content, err := (&Source{}).OpenContent(context.Background(), connectors.ContentRequest{Item: calendarItem})
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func TestTimestamps(t *testing.T) {
	for _, tc := range []struct{ name, component, zone, expected string }{
		{"UTC future", event("one", "DTSTART:20301012T090000Z"), "Asia/Singapore", "2030-10-12T09:00:00Z"},
		{"floating", event("one", "DTSTART:20261012T090000"), "Asia/Singapore", "2026-10-12T01:00:00Z"},
		{"explicit TZID", event("one", "DTSTART;TZID=Asia/Tokyo:20261012T090000"), "Asia/Singapore", "2026-10-12T00:00:00Z"},
		{"all day", event("one", "DTSTART;VALUE=DATE:20261012\r\nDTEND;VALUE=DATE:20261013"), "Asia/Singapore", "2026-10-11T16:00:00Z"},
		{"DST gap", event("one", "DTSTART;TZID=America/New_York:20070311T023000"), "UTC", "2007-03-11T07:30:00Z"},
		{"DST overlap", event("one", "DTSTART;TZID=Europe/Paris:20261025T023000"), "UTC", "2026-10-25T00:30:00Z"},
		{"completed todo", "BEGIN:VTODO\r\nUID:todo\r\nCOMPLETED:20261010T020000Z\r\nDUE:20261011T090000Z\r\nDTSTART:20261001T080000Z\r\nSTATUS:COMPLETED\r\nEND:VTODO", "UTC", "2026-10-10T02:00:00Z"},
		{"due todo", "BEGIN:VTODO\r\nUID:todo\r\nDUE:20261011T090000Z\r\nEND:VTODO", "UTC", "2026-10-11T09:00:00Z"},
		{"unknown due does not fallback", "BEGIN:VTODO\r\nUID:todo\r\nDUE;TZID=Custom/Zone:20261011T090000\r\nDTSTART:20261001T080000Z\r\nEND:VTODO", "UTC", "unset"},
		{"unknown timezone", event("one", "DTSTART;TZID=Custom/Zone:20261012T090000\r\nLAST-MODIFIED:20261001T000000Z"), "UTC", "unset"},
		{"undated", event("one", "LAST-MODIFIED:20261001T000000Z"), "UTC", "unset"},
		{"series", event("one", "DTSTART:20261012T090000Z\r\nRRULE:FREQ=WEEKLY;BYDAY=MO"), "UTC", "unset"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items, _, err := parsedItems(t, fixture(tc.component), tc.zone)
			if err != nil || len(items) != 1 {
				t.Fatalf("parse: items=%d err=%v", len(items), err)
			}
			content := itemContent(t, items[0])
			actual := content.Timestamp.UTC().Format(time.RFC3339)
			if content.TimestampUnset {
				actual = "unset"
			}
			if actual != tc.expected {
				t.Fatalf("timestamp=%s want=%s", actual, tc.expected)
			}
			if tc.name == "all day" && !strings.Contains(content.Text, "exclusive end date") {
				t.Fatal("all-day end semantics lost")
			}
		})
	}
}

func TestSeriesGroupingAndRevision(t *testing.T) {
	master := event("Series", "DTSTART:20261012T090000Z\r\nRRULE:FREQ=WEEKLY;BYDAY=MO\r\nEXDATE:20261026T090000Z\r\nATTENDEE;CN=Alice;PARTSTAT=ACCEPTED:mailto:alice@example.org\r\nBEGIN:VALARM\r\nTRIGGER:-PT15M\r\nACTION:DISPLAY\r\nDESCRIPTION:Prepare agenda\r\nEND:VALARM\r\nDTSTAMP:20261001T000000Z")
	exception := event("Series", "RECURRENCE-ID:20261019T090000Z\r\nDTSTART:20261019T100000Z\r\nSTATUS:CANCELLED")
	items, _, err := parsedItems(t, fixture(master, exception, master), "UTC")
	if err != nil || len(items) != 1 {
		t.Fatalf("grouping: items=%d err=%v", len(items), err)
	}
	content := itemContent(t, items[0])
	for _, expected := range []string{"RRULE", "RECURRENCE-ID", "EXDATE", "VALARM", "alice@example.org", "PARTSTAT=ACCEPTED", "CANCELLED"} {
		if !strings.Contains(content.Text, expected) {
			t.Fatalf("content lost %s", expected)
		}
	}
	if _, hasEnd := items[0].Attributes["seriesEndAt"]; hasEnd || !content.TimestampUnset {
		t.Fatal("infinite series acquired a finite end or timestamp")
	}
	reordered := strings.Replace(master, "DTSTAMP:20261001T000000Z", "DTSTAMP:20261002T000000Z", 1)
	reordered = strings.Replace(reordered, "SUMMARY:Meeting", "SUMMARY:Meet\r\n ing", 1)
	reordered = strings.Replace(reordered, "FREQ=WEEKLY;BYDAY=MO", "BYDAY=MO;FREQ=WEEKLY", 1)
	second, _, err := parsedItems(t, fixture(exception, reordered), "UTC")
	if err != nil || items[0].ID != second[0].ID || items[0].Revision != second[0].Revision {
		t.Fatalf("format/export changes affected identity or revision: %v", err)
	}
	moved := strings.Replace(exception, "20261019T100000Z", "20261019T110000Z", 1)
	third, _, err := parsedItems(t, fixture(master, moved), "UTC")
	if err != nil || items[0].ID != third[0].ID || items[0].Revision == third[0].Revision {
		t.Fatalf("exception change did not update same document: %v", err)
	}
	orphan, _, err := parsedItems(t, fixture(exception), "UTC")
	if err != nil || orphan[0].ID != items[0].ID || !strings.Contains(itemContent(t, orphan[0]).Text, "master component is absent") {
		t.Fatalf("orphan exception: %v", err)
	}
}

func TestSnapshotValidation(t *testing.T) {
	valid := event("one", "DTSTART:20261012T090000Z")
	for _, body := range []string{
		fixture(valid) + "garbage",
		strings.TrimSuffix(fixture(valid), "END:VCALENDAR\r\n"),
		strings.Replace(fixture(valid), "VERSION:2.0", "VERSION:1.0", 1),
		strings.Replace(fixture(valid), "VERSION:2.0", "VERSION:2.0\r\nMETHOD:CANCEL", 1),
		fixture(valid, strings.Replace(valid, "SUMMARY:Meeting", "SUMMARY:Other", 1)),
		fixture(event("one", "DTSTART;BROKEN:secret-line")),
		fixture(event("one", "DTSTART:invalid-secret-date")),
		fixture(event("one", "RRULE:FREQ=INVALID")),
		fixture("BEGIN:VALARM\r\nACTION:DISPLAY\r\nEND:VALARM"),
		fixture("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nEND:VCALENDAR"),
	} {
		if _, _, err := parsedItems(t, body, "UTC"); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid snapshot accepted or leaked input: %v", err)
		}
	}
	missing := strings.Replace(valid, "UID:one\r\n", "", 1)
	items, warnings, err := parsedItems(t, fixture(valid, missing), "UTC")
	if err == nil || len(items) != 1 || len(warnings) == 0 {
		t.Fatalf("missing UID should preserve good item but mark incomplete: %d %v %v", len(items), warnings, err)
	}
	items, _, err = parsedItems(t, fixture(), "UTC")
	if err != nil || len(items) != 0 {
		t.Fatalf("empty complete calendar: %v", err)
	}
	if _, err := parseCalendar([]byte(fixture(valid)), 1); err == nil {
		t.Fatal("component limit ignored")
	}
	if _, err := parseCalendar([]byte("BEGIN:VCALENDAR\nVERSION:2.0\n"+strings.Repeat("BEGIN:X-NESTED\n", 33)), 20000); err == nil {
		t.Fatal("nesting limit ignored")
	}
	if _, err := parseCalendar([]byte(fixture(event("one", "ATTENDEE;CN="+strings.Repeat("x", 8200)+":mailto:test@example.org"))), 20000); err == nil {
		t.Fatal("parameter parsing work limit ignored")
	}
}

func TestSeriesBoundsAndTimezoneDefinitions(t *testing.T) {
	body := fixture(event("one", "DTSTART:20200101T090000Z\r\nDTEND:20200101T100000Z\r\nRRULE:FREQ=DAILY;UNTIL=20261012T090000Z\r\nRDATE:20271012T090000Z"),
		event("one", "RECURRENCE-ID:20261012T090000Z\r\nDTSTART:20281012T090000Z\r\nDTEND:20281012T100000Z"))
	items, _, err := parsedItems(t, body, "UTC")
	if err != nil || items[0].Attributes["seriesStartAt"].(time.Time).Year() != 2020 || items[0].Attributes["seriesEndAt"].(time.Time).Year() != 2028 {
		t.Fatalf("bounds: %v %v", items, err)
	}
	filter := connectors.Filter{Expression: connectors.FilterGroup{Rules: []connectors.FilterRule{{Field: "seriesStartAt", Operator: connectors.OpLEQ, Value: "2026-10-13T00:00:00Z"}}, Groups: []connectors.FilterGroup{{Combinator: "or", Rules: []connectors.FilterRule{{Field: "seriesEndAt", Operator: connectors.OpNotExists}, {Field: "seriesEndAt", Operator: connectors.OpGEQ, Value: "2026-10-12T00:00:00Z"}}}}}}
	matcher, err := connectors.CompileFilter(filter)
	if err != nil || !matcher(items[0].Attributes) {
		t.Fatalf("old start/current series filtered out: %v", err)
	}
	custom := "BEGIN:VTIMEZONE\r\nTZID:Custom/Zone\r\nBEGIN:STANDARD\r\nDTSTART:19700101T000000\r\nTZOFFSETFROM:+0800\r\nTZOFFSETTO:+0800\r\nEND:STANDARD\r\nEND:VTIMEZONE"
	unknown := event("two", "DTSTART;TZID=Custom/Zone:20261012T090000")
	before, warnings, err := parsedItems(t, fixture(custom, unknown), "UTC")
	if err != nil || len(warnings) == 0 || !strings.Contains(itemContent(t, before[0]).Text, "TZOFFSETTO") {
		t.Fatalf("custom zone missing: %v %v", warnings, err)
	}
	after, _, err := parsedItems(t, fixture(strings.ReplaceAll(custom, "+0800", "+0900"), unknown), "UTC")
	if err != nil || before[0].Revision == after[0].Revision {
		t.Fatal("timezone definition change ignored")
	}
}

func TestRecurrenceDatesAndCalendarDurations(t *testing.T) {
	for _, tc := range []struct{ name, fields, end string }{
		{"RDATE inherits duration", "DTSTART:20200101T090000Z\r\nDTEND:20200101T110000Z\r\nRDATE:20261012T090000Z", "2026-10-12T11:00:00Z"},
		{"RDATE explicit period", "DTSTART:20200101T090000Z\r\nRDATE;VALUE=PERIOD:20261012T090000Z/PT3H", "2026-10-12T12:00:00Z"},
		{"mixed duration across DST", "DTSTART;TZID=America/New_York:20260307T120000\r\nDURATION:P1DT1H", "2026-03-08T17:00:00Z"},
		{"nominal day ending in DST gap", "DTSTART;TZID=America/New_York:20260307T023000\r\nDURATION:P1D", "2026-03-08T07:30:00Z"},
		{"UTC UNTIL with nominal day", "DTSTART;TZID=America/New_York:20261020T090000\r\nDURATION:P1D\r\nRRULE:FREQ=DAILY;UNTIL=20261031T130000Z", "2026-11-01T14:00:00Z"},
		{"all day across DST", "DTSTART;VALUE=DATE:20260308\r\nDTEND;VALUE=DATE:20260309\r\nRDATE;VALUE=DATE:20260309", "2026-03-10T04:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items, _, err := parsedItems(t, fixture(event("one", tc.fields)), "America/New_York")
			if err != nil || len(items) != 1 || items[0].Attributes["seriesEndAt"].(time.Time).UTC().Format(time.RFC3339) != tc.end {
				t.Fatalf("series end: %+v %v", items, err)
			}
		})
	}
	for _, fields := range []string{
		"DTSTART:20261012T090000Z\r\nDTEND:20261012T080000Z",
		"DTSTART:20261012T090000Z\r\nDURATION:P9999999999999999999D",
		"DTSTART:20261012T090000Z\r\nDURATION:P",
		"DTSTART:20261012T090000Z\r\nDURATION:P1DT",
		"DTSTART;VALUE=TEXT:20261012T090000Z",
		"DTSTART:20261012T090000Z\r\nRRULE:FREQ=DAILY;COUNT=-1",
		"DTSTART:20261012T090000Z\r\nRRULE:FREQ=DAILY;INTERVAL=0",
		"DTSTART:20261012T090000Z\r\nRRULE:FREQ=DAILY;BYMONTH=13",
		"DTSTART:20261012T090000Z\r\nRRULE:FREQ=DAILY;COUNT=2;COUNT=3",
		"DTSTART:20261012T090000Z\r\nRDATE;VALUE=PERIOD:20261012T090000Z/20261012T080000Z",
	} {
		if _, _, err := parsedItems(t, fixture(event("one", fields)), "UTC"); err == nil {
			t.Fatal("invalid end or duration accepted")
		}
	}
}

func TestUIDDecodingAndComponentIdentity(t *testing.T) {
	items, _, err := parsedItems(t, fixture(
		event(`Case\,id`, "DTSTART:20261012T090000Z"),
		event(`case\,id`, "DTSTART:20261012T090000Z"),
		"BEGIN:VTODO\r\nUID:Case\\,id\r\nEND:VTODO"), "UTC")
	if err != nil || len(items) != 3 {
		t.Fatal("UIDs merged across case or component types", err)
	}
	ids := map[string]bool{}
	for _, item := range items {
		if ids[item.ID] || !strings.Contains(item.Metadata["ical_uid"], ",") {
			t.Fatal("UID decoding or identity failed")
		}
		ids[item.ID] = true
	}
}

func TestLastModifiedAndCustomConditionalHeaders(t *testing.T) {
	var seen []http.Header
	modified := "Mon, 12 Oct 2026 08:00:00 GMT"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Clone())
		w.Header().Set("Last-Modified", modified)
		if r.Header.Get("If-Modified-Since") == modified {
			w.WriteHeader(304)
			return
		}
		fmt.Fprint(w, fixture())
	}))
	defer server.Close()
	req := connectors.ScanRequest{Credential: connectors.Credential{Secrets: map[string]string{"feedUrl": server.URL}, Headers: map[string]string{"If-None-Match": "custom", "If-Modified-Since": "custom"}}, Emit: func(connectors.SourceItem) error { return nil }}
	source := &Source{}
	baseline, err := source.Scan(t.Context(), req)
	if err != nil || !baseline.Complete {
		t.Fatal(err)
	}
	req.Cursor = baseline.Cursor
	if next, err := source.Scan(t.Context(), req); err != nil || next.Complete {
		t.Fatal("Last-Modified 304 failed", err)
	}
	req.Full = true
	if full, err := source.Scan(t.Context(), req); err != nil || !full.Complete {
		t.Fatal("full was conditional", err)
	}
	if seen[0].Get("If-None-Match") != "" || seen[0].Get("If-Modified-Since") != "" || seen[2].Get("If-Modified-Since") != "" {
		t.Fatal("custom headers defeated forced inventory")
	}
}

func TestPublisherRefreshHint(t *testing.T) {
	for _, header := range []string{"REFRESH-INTERVAL;VALUE=DURATION:P1W", "X-PUBLISHED-TTL:PT30M"} {
		calendar, err := parseCalendar([]byte(strings.Replace(fixture(), "VERSION:2.0", "VERSION:2.0\r\n"+header, 1)), 20000)
		if err != nil {
			t.Fatal(err)
		}
		var messages []string
		logRefreshHint(calendar, func(_, message string) { messages = append(messages, message) })
		if len(messages) != 1 || !strings.Contains(messages[0], "schedule is unchanged") {
			t.Fatal("publisher refresh hint missing")
		}
	}
}

func TestCategoryEscapesAndThisAndFuture(t *testing.T) {
	master := event("one", "DTSTART:20261012T090000Z\r\nRRULE:FREQ=DAILY;COUNT=3\r\nCATEGORIES:Work\\, travel,Other")
	exception := event("one", "RECURRENCE-ID;RANGE=THISANDFUTURE:20261013T090000Z\r\nDTSTART:20261013T080000Z")
	items, _, err := parsedItems(t, fixture(master, exception), "UTC")
	if err != nil || len(items) != 1 {
		t.Fatal(err)
	}
	content := itemContent(t, items[0])
	if !strings.Contains(content.Text, "Work, travel") || !strings.Contains(content.Text, "THISANDFUTURE") {
		t.Fatal("escaped category or recurrence range lost")
	}
	if _, exists := items[0].Attributes["seriesStartAt"]; exists {
		t.Fatal("THISANDFUTURE lower boundary must remain unknown")
	}
	if _, exists := items[0].Attributes["seriesEndAt"]; exists {
		t.Fatal("COUNT series acquired a guessed end")
	}
}

func TestUnchangedBodyShortcutAndCursorVersions(t *testing.T) {
	body := fixture(event("one", "DTSTART:20261012T090000Z"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
	defer server.Close()
	emitted := 0
	req := connectors.ScanRequest{Credential: connectors.Credential{Secrets: map[string]string{"feedUrl": server.URL}}, Emit: func(connectors.SourceItem) error { emitted++; return nil }}
	source := &Source{}
	baseline, err := source.Scan(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.Cursor = baseline.Cursor
	unchanged, err := source.Scan(t.Context(), req)
	if err != nil || unchanged.Complete || emitted != 1 {
		t.Fatalf("same body: %+v %v", unchanged, err)
	}
	req.Full = true
	if full, err := source.Scan(t.Context(), req); err != nil || !full.Complete || emitted != 2 {
		t.Fatal("full skipped parsing", err)
	}
	req.Full, req.Cursor = false, []byte(strings.Replace(string(baseline.Cursor), `"version":1`, `"version":0`, 1))
	if old, err := source.Scan(t.Context(), req); err != nil || !old.Complete || emitted != 3 {
		t.Fatal("old cursor skipped parsing", err)
	}
}

func TestConditionalHTTPAndForcedScan(t *testing.T) {
	var requested []http.Header
	body := fixture(event("one", "DTSTART:20261012T090000Z"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.Header.Clone())
		w.Header().Set("ETag", `"one"`)
		if r.Header.Get("If-None-Match") == `"one"` {
			w.WriteHeader(304)
			return
		}
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	source := &Source{}
	emitted := 0
	req := connectors.ScanRequest{Credential: connectors.Credential{Secrets: map[string]string{"feedUrl": server.URL}}, Timezone: "UTC", Emit: func(connectors.SourceItem) error { emitted++; return nil }}
	first, err := source.Scan(t.Context(), req)
	if err != nil || !first.Complete || emitted != 1 {
		t.Fatalf("first: %+v %v", first, err)
	}
	req.Cursor = first.Cursor
	second, err := source.Scan(t.Context(), req)
	if err != nil || second.Complete || emitted != 1 || !bytes.Equal(second.Cursor, first.Cursor) {
		t.Fatalf("304: %+v %v", second, err)
	}
	req.Full = true
	third, err := source.Scan(t.Context(), req)
	if err != nil || !third.Complete || emitted != 2 || requested[2].Get("If-None-Match") != "" {
		t.Fatalf("full: %+v %v", third, err)
	}
	req.Full, req.Timezone = false, "Asia/Singapore"
	if _, err := source.Scan(t.Context(), req); err != nil || requested[3].Get("If-None-Match") != "" {
		t.Fatalf("changed context reused validator: %v", err)
	}
}

func TestHTTPCompressionRedirectAndRedaction(t *testing.T) {
	var seen http.Header
	end := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		if r.URL.Path == "/first" {
			http.Redirect(w, r, "/last", http.StatusFound)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		compressed := gzip.NewWriter(w)
		fmt.Fprint(compressed, fixture())
		compressed.Close()
	}))
	defer end.Close()
	start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, end.URL+"/first", http.StatusFound) }))
	defer start.Close()
	cred := connectors.Credential{Secrets: map[string]string{"feedUrl": start.URL + "/secret-path?token=secret-query"}, Headers: map[string]string{"Authorization": "Bearer secret", "X-Secret": "secret", "Cookie": "secret", "Accept-Encoding": "gzip"}}
	response, err := fetch(t.Context(), fetchRequest{credential: cred, limits: limits{bytes: 1 << 20}})
	if err != nil || len(response.body) == 0 {
		t.Fatalf("compressed redirect: %v", err)
	}
	for _, key := range []string{"Authorization", "X-Secret", "Cookie"} {
		if seen.Get(key) != "" {
			t.Fatalf("redirect leaked %s", key)
		}
	}
	if _, err := fetch(t.Context(), fetchRequest{credential: cred, limits: limits{bytes: 10}}); err == nil {
		t.Fatal("decompressed size limit ignored")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403); fmt.Fprint(w, "secret-response") }))
	defer bad.Close()
	cred.Secrets["feedUrl"] = bad.URL + "/secret-path?token=secret-query"
	_, err = fetch(t.Context(), fetchRequest{credential: cred, limits: limits{bytes: 100}})
	if err == nil || strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "403") {
		t.Fatalf("error not redacted: %v", err)
	}
	normalized, err := subscriptionURL("webcal://example.org/calendar")
	if err != nil || normalized.Scheme != "https" {
		t.Fatal("webcal normalization failed")
	}
	secure, _ := http.NewRequest("GET", "https://example.org", nil)
	plain, _ := http.NewRequest("GET", "http://example.org", nil)
	if newClient(cred).HTTP.CheckRedirect(plain, []*http.Request{secure}) == nil {
		t.Fatal("HTTPS downgrade accepted")
	}
}

func BenchmarkCalendarGrouping(b *testing.B) {
	components := make([]string, 1000)
	for index := range components {
		components[index] = event(fmt.Sprintf("event-%d", index), "DTSTART:20261012T090000Z\r\nDESCRIPTION:Benchmark meeting")
	}
	body := fixture(components...)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		if _, _, err := parsedItems(b, body, "UTC"); err != nil {
			b.Fatal(err)
		}
	}
}

func FuzzCalendarSnapshot(f *testing.F) {
	f.Add([]byte(fixture(event("one", "DTSTART:20261012T090000Z"))))
	f.Add([]byte(fixture(event("one", "DTSTART;BROKEN:secret"))))
	f.Add([]byte(fixture()))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 1<<20 {
			return
		}
		calendar, err := parseCalendar(body, 2000)
		if err == nil {
			calendarItems(calendar, time.UTC)
		}
	})
}
