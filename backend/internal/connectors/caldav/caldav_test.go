package caldav

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

const calendarProperties = `<d:resourcetype><d:collection/><c:calendar/></d:resourcetype><d:displayname>Work &amp; life</d:displayname>`

func multi(responses ...string) string {
	return `<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">` + strings.Join(responses, "") + `</d:multistatus>`
}

func entry(href, props string) string {
	return `<d:response><d:href>` + href + `</d:href><d:propstat><d:prop>` + props + `</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`
}

func resource(href, calendar string) string {
	var escaped strings.Builder
	if err := xml.EscapeText(&escaped, []byte(calendar)); err != nil {
		panic(err)
	}
	return entry(href, `<d:getetag>"1"</d:getetag><c:calendar-data>`+escaped.String()+`</c:calendar-data>`)
}

func ics(components ...string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" + strings.Join(components, "\r\n") + "\r\nEND:VCALENDAR\r\n"
}

func event(uid, name string) string {
	return "BEGIN:VEVENT\r\nUID:" + uid + "\r\nSUMMARY:" + name + "\r\nDTSTART:20261021T090000\r\nEND:VEVENT"
}

func scan(t *testing.T, report string, cfg map[string]any) ([]connectors.SourceItem, connectors.ScanResult, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(207)
		if r.Method == "PROPFIND" {
			fmt.Fprint(w, multi(entry("/cal/", calendarProperties)))
			return
		}
		if r.Method != "REPORT" || r.Header.Get("Depth") != "1" {
			t.Errorf("unexpected request: %s %s", r.Method, r.Header.Get("Depth"))
		}
		fmt.Fprint(w, report)
	}))
	t.Cleanup(server.Close)
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg["calendarUrl"] = "/cal/"
	items := []connectors.SourceItem{}
	result, err := (&Source{}).Scan(t.Context(), connectors.ScanRequest{
		Credential: connectors.Credential{Config: map[string]any{"baseUrl": server.URL}}, Config: cfg, Timezone: "Asia/Singapore",
		Emit: func(item connectors.SourceItem) error { items = append(items, item); return nil },
	})
	return items, result, err
}

func TestScanCalendarContentAndStableIdentity(t *testing.T) {
	recurring := event("series", "Weekly")
	recurring = strings.Replace(recurring, "END:VEVENT", "RRULE:FREQ=WEEKLY\r\nEND:VEVENT", 1)
	exception := strings.Replace(event("series", "Moved"), "DTSTART:", "RECURRENCE-ID:20261028T090000\r\nDTSTART:", 1)
	todo := "BEGIN:VTODO\r\nUID:todo\r\nSUMMARY:Done\r\nSTATUS:COMPLETED\r\nCOMPLETED:20261020T120000Z\r\nEND:VTODO"
	items, result, err := scan(t, multi(resource("/cal/a.ics", ics(recurring, exception)), resource("/cal/b.ics", ics(todo))), nil)
	if err != nil || !result.Complete || len(items) != 2 {
		t.Fatalf("scan: %+v %v %d", result, err, len(items))
	}
	var series, task connectors.SourceItem
	for _, item := range items {
		if item.Name == "Weekly" {
			series = item
		} else {
			task = item
		}
	}
	content, err := (&Source{}).OpenContent(t.Context(), connectors.ContentRequest{Item: series})
	if err != nil || !content.TimestampUnset || !strings.Contains(content.Text, "Moved") || !strings.Contains(content.Text, "FREQ=WEEKLY") {
		t.Fatalf("series: %+v %v", content, err)
	}
	content, err = (&Source{}).OpenContent(t.Context(), connectors.ContentRequest{Item: task})
	if err != nil || content.Timestamp.Format("2006-01-02T15:04:05Z") != "2026-10-20T12:00:00Z" || !strings.Contains(content.Text, "COMPLETED") {
		t.Fatalf("todo: %+v %v", content, err)
	}
	moved, _, err := scan(t, multi(resource("/cal/renamed.ics", ics(recurring, exception))), nil)
	if err != nil || moved[0].ID != series.ID || moved[0].Revision != series.Revision {
		t.Fatal("resource rename changed logical identity or content revision", err)
	}
	plain, _, err := scan(t, multi(resource("/cal/plain.ics", ics(event("single", "One")))), nil)
	if err != nil {
		t.Fatal(err)
	}
	content, _ = (&Source{}).OpenContent(t.Context(), connectors.ContentRequest{Item: plain[0]})
	if content.Timestamp.UTC().Format("2006-01-02T15:04:05Z") != "2026-10-21T01:00:00Z" {
		t.Fatalf("floating time: %v", content.Timestamp)
	}
}

func TestIncompleteResponsesNeverEmitOrComplete(t *testing.T) {
	good := resource("/cal/a.ics", ics(event("one", "One")))
	for name, report := range map[string]string{
		"truncated XML":         strings.TrimSuffix(multi(good), "</d:multistatus>"),
		"trailing XML":          multi(good) + "<extra/>",
		"wrong namespace":       `<multistatus/>`,
		"missing calendar data": multi(good, entry("/cal/b.ics", `<d:getetag>"2"</d:getetag>`)),
		"property forbidden":    multi(good, strings.Replace(resource("/cal/b.ics", ics(event("two", "Two"))), "200 OK", "403 Forbidden", 1)),
		"limit response":        multi(good, `<d:response><d:href>/cal/</d:href><d:status>HTTP/1.1 507 Insufficient Storage</d:status></d:response>`),
		"missing UID":           multi(good, resource("/cal/b.ics", ics(strings.Replace(event("two", "Two"), "UID:two\r\n", "", 1)))),
		"duplicate UID":         multi(good, resource("/cal/b.ics", ics(event("one", "Conflicting")))),
		"outside collection":    multi(resource("/other/a.ics", ics(event("one", "One")))),
		"cross origin":          multi(resource("https://elsewhere.invalid/cal/a.ics", ics(event("one", "One")))),
		"invalid calendar":      multi(good, resource("/cal/b.ics", "BEGIN:VCALENDAR\r\n")),
		"empty calendar data":   multi(good, resource("/cal/b.ics", "")),
	} {
		t.Run(name, func(t *testing.T) {
			items, result, err := scan(t, report, nil)
			if err == nil || result.Complete || len(items) != 0 {
				t.Fatalf("unsafe scan: %+v %v %d", result, err, len(items))
			}
		})
	}
	for name, cfg := range map[string]map[string]any{
		"aggregate components": {"maxComponents": 3},
		"response bytes":       {"maxFeedSizeMB": 1},
	} {
		t.Run(name, func(t *testing.T) {
			report := multi(good, resource("/cal/b.ics", ics(event("two", "Two"))))
			if name == "response bytes" {
				report = multi(resource("/cal/big.ics", ics(event("big", strings.Repeat("a", 1<<20)))))
			}
			items, result, err := scan(t, report, cfg)
			if err == nil || result.Complete || len(items) != 0 {
				t.Fatalf("limit ignored: %+v %v", result, err)
			}
		})
	}
	items, result, err := scan(t, multi(), nil)
	if err != nil || !result.Complete || len(items) != 0 {
		t.Fatalf("valid empty collection: %+v %v", result, err)
	}
}

func TestDiscoveryAuthenticationAndRedirects(t *testing.T) {
	for _, mode := range []string{"principal", "home", "calendar", "well-known", "bearer"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "PROPFIND" {
					t.Errorf("method changed: %s", r.Method)
				}
				if mode == "bearer" {
					if r.Header.Get("Authorization") != "Bearer token" {
						t.Error("missing bearer token")
					}
				} else if user, password, ok := r.BasicAuth(); !ok || user != "alice" || password != "app-password" {
					t.Error("missing Basic auth")
				}
				if r.Header.Get("X-Custom") != "secret" {
					t.Error("missing custom header")
				}
				if mode == "well-known" && r.URL.Path == "/" {
					http.NotFound(w, r)
					return
				}
				if r.URL.Path == "/.well-known/caldav" {
					http.Redirect(w, r, "/service/", 303)
					return
				}
				w.WriteHeader(207)
				switch r.URL.Path {
				case "/", "/service/":
					fmt.Fprint(w, multi(entry(r.URL.Path, `<d:current-user-principal><d:href>/principal/</d:href></d:current-user-principal>`)))
				case "/principal/":
					fmt.Fprint(w, multi(entry("/principal/", `<c:calendar-home-set><d:href>/home/</d:href></c:calendar-home-set>`)))
				case "/home/":
					fmt.Fprint(w, multi(entry("/home/", `<d:resourcetype><d:collection/></d:resourcetype>`), entry("/home/work/", calendarProperties)))
				case "/home/work/":
					fmt.Fprint(w, multi(entry("/home/work/", calendarProperties)))
				default:
					t.Errorf("unexpected discovery URL: %s", r.URL.Path)
				}
			}))
			defer server.Close()
			base := server.URL
			if mode == "home" {
				base += "/home/"
			}
			if mode == "calendar" {
				base += "/home/work/"
			}
			cred := connectors.Credential{Config: map[string]any{"baseUrl": base, "username": "alice"}, Secrets: map[string]string{"password": "app-password"}, Headers: map[string]string{"X-Custom": "secret"}}
			if mode == "bearer" {
				cred.Secrets["bearerToken"] = "token"
			}
			result, err := (&Source{}).Browse(t.Context(), cred, connectors.BrowseRequest{})
			if err != nil || len(result.Items) != 1 || result.Items[0].ID != server.URL+"/home/work/" || result.Items[0].Name != "Work & life" || !result.Items[0].Selectable {
				t.Fatalf("browse: %+v %v", result, err)
			}
			if _, err := (&Source{}).ValidateCredential(t.Context(), cred); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRejectCrossOriginBeforeSendingSecrets(t *testing.T) {
	called := false
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer other.Close()
	for _, mode := range []string{"redirect", "principal", "home"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					http.Redirect(w, r, other.URL, 302)
					return
				}
				property := `<d:current-user-principal><d:href>` + other.URL + `</d:href></d:current-user-principal>`
				if mode == "home" {
					property = `<c:calendar-home-set><d:href>` + other.URL + `</d:href></c:calendar-home-set>`
				}
				w.WriteHeader(207)
				fmt.Fprint(w, multi(entry("/", property)))
			}))
			defer server.Close()
			_, err := (&Source{}).Browse(t.Context(), connectors.Credential{Config: map[string]any{"baseUrl": server.URL}, Headers: map[string]string{"X-Secret": "secret"}}, connectors.BrowseRequest{})
			if err == nil || called {
				t.Fatalf("cross-origin accepted: %v, called=%v", err, called)
			}
		})
	}
}

func TestValidationCancellationAndErrorRedaction(t *testing.T) {
	for _, address := range []string{"file:///calendar", "https://user:password@example.com/", "https://example.com/?token=secret", "//example.com/", "/relative"} {
		if _, err := newClient(connectors.Credential{Config: map[string]any{"baseUrl": address}}); err == nil {
			t.Errorf("accepted %s", address)
		}
	}
	for _, cfg := range []map[string]any{{}, {"calendarUrl": "/cal/", "maxComponents": 0}, {"calendarUrl": "/cal/", "maxFeedSizeMB": 1.5}} {
		if err := (&Source{}).ValidateConfig(cfg, connectors.Filter{}); err == nil {
			t.Errorf("accepted config: %+v", cfg)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403); fmt.Fprint(w, "echoed-password") }))
	defer server.Close()
	c, err := newClient(connectors.Credential{Config: map[string]any{"baseUrl": server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.calendars(t.Context())
	if err == nil || strings.Contains(err.Error(), "echoed-password") {
		t.Fatalf("unsafe error: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = c.calendars(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
