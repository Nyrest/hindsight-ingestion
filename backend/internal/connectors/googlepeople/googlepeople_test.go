package googlepeople

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type fixture struct {
	connector *Connector
	request   connectors.ScanRequest
	queries   []url.Values
	respond   func(http.ResponseWriter, *http.Request)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := database.Open("sqlite", filepath.Join(t.TempDir(), "people.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sql, _ := db.DB(); sql.Close() })
	f := &fixture{connector: &Connector{DB: db}, request: connectors.ScanRequest{TaskID: uuid.NewString(), RunID: uuid.NewString(),
		Credential: connectors.Credential{Type: SourceType, OAuthSubject: "account", OAuth: &connectors.OAuthToken{AccessToken: "access"}},
		Config:     map[string]any{}, Log: func(string, string) {},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/people/me/connections" || r.Header.Get("Authorization") != "Bearer access" {
			t.Error("unexpected endpoint or auth", r.URL.Path)
		}
		f.queries = append(f.queries, r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		f.respond(w, r)
	}))
	t.Cleanup(server.Close)
	previous := apiBase
	apiBase = server.URL
	t.Cleanup(func() { apiBase = previous })
	return f
}

func contact(id, name string) person {
	var p person
	p.ResourceName = "people/" + id
	p.Metadata.Sources = []personSource{{Type: "CONTACT", ID: id}}
	p.Names = []personField{{DisplayName: name}}
	return p
}

func writePage(w http.ResponseWriter, contacts []person, page, token string) {
	json.NewEncoder(w).Encode(connectionsPage{Connections: contacts, NextPageToken: page, NextSyncToken: token})
}

func (f *fixture) scan(t *testing.T) (connectors.ScanResult, []connectors.SourceItem, error) {
	t.Helper()
	observed := []connectors.SourceItem{}
	f.request.Emit = func(contact connectors.SourceItem) error { observed = append(observed, contact); return nil }
	result, err := f.connector.Scan(t.Context(), f.request)
	return result, observed, errors.Join(err, result.Error)
}

func (f *fixture) commit(t *testing.T, result connectors.ScanResult, contacts []connectors.SourceItem) {
	t.Helper()
	err := f.connector.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&models.TaskRun{ID: f.request.RunID, TaskID: f.request.TaskID}).Error; err != nil {
			return err
		}
		for _, contact := range contacts {
			if err := tx.Create(&models.TaskItem{TaskID: f.request.TaskID, SourceItemID: contact.ID, DestinationPresent: true}).Error; err != nil {
				return err
			}
		}
		if err := f.connector.CommitScan(t.Context(), f.request.RunID, tx); err != nil {
			return err
		}
		return tx.Create(&models.TaskState{TaskID: f.request.TaskID, CommittedCursorJSON: string(result.Cursor)}).Error
	})
	if err != nil {
		t.Fatal(err)
	}
	f.request.Cursor, f.request.RunID = result.Cursor, uuid.NewString()
}

func TestPaginationCoalescingAndAliasDeletion(t *testing.T) {
	f := newFixture(t)
	f.respond = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pageToken") == "" {
			writePage(w, []person{contact("1", "Earlier")}, "page2", "")
			return
		}
		updated := contact("1", "Latest")
		updated.ResourceName = "people/new1"
		updated.Metadata.PreviousResourceNames = []string{"people/1"}
		writePage(w, []person{updated, contact("2", "Second")}, "", "baseline")
	}
	result, observed, err := f.scan(t)
	if err != nil || !result.Complete || len(observed) != 2 || observed[0].Name != "Latest" {
		t.Fatal(result, observed, err)
	}
	if len(f.queries) != 2 {
		t.Fatal("unexpected per-contact requests", f.queries)
	}
	for _, query := range f.queries {
		if query.Get("personFields") != personFields || query.Get("pageSize") != "1000" || query.Get("sources") != "READ_SOURCE_TYPE_CONTACT" || query.Get("requestSyncToken") != "true" {
			t.Fatal("incorrect fixed query", query)
		}
	}
	f.commit(t, result, observed)
	f.respond = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("syncToken") != "baseline" {
			t.Error("lost incremental token")
		}
		deleted := person{ResourceName: "people/1"}
		deleted.Metadata.Deleted = true
		writePage(w, []person{deleted}, "", "incremental")
	}
	result, observed, err = f.scan(t)
	if err != nil || result.Complete || len(observed) != 1 || !observed[0].Deleted || observed[0].ID != "contact:1" {
		t.Fatal(result, observed, err)
	}
	var cursor cursorState
	if err := json.Unmarshal(result.Cursor, &cursor); err != nil || cursor.SyncToken != "incremental" {
		t.Fatal(cursor, err)
	}
}

func TestMidPageRecoveryDiscardsEarlierDelta(t *testing.T) {
	for _, failure := range []string{"expired_reason", "expired_410", "unknown_deletion"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			f.respond = func(w http.ResponseWriter, _ *http.Request) {
				writePage(w, []person{contact("1", "Original")}, "", "baseline")
			}
			result, observed, err := f.scan(t)
			if err != nil {
				t.Fatal(err)
			}
			f.commit(t, result, observed)
			f.queries = nil
			f.respond = func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("syncToken") == "" {
					writePage(w, []person{contact("1", "Rebuilt")}, "", "rebuilt")
					return
				}
				if r.URL.Query().Get("pageToken") == "" {
					writePage(w, []person{contact("stale", "Discard me")}, "page2", "")
					return
				}
				switch failure {
				case "expired_reason":
					w.WriteHeader(400)
					io.WriteString(w, `{"error":{"details":[{"reason":"EXPIRED_SYNC_TOKEN"}]}}`)
				case "expired_410":
					w.WriteHeader(410)
				case "unknown_deletion":
					deleted := person{ResourceName: "people/unknown"}
					deleted.Metadata.Deleted = true
					writePage(w, []person{deleted}, "", "unused")
				}
			}
			result, observed, err = f.scan(t)
			if err != nil || !result.Complete || len(observed) != 1 || observed[0].Name != "Rebuilt" || len(f.queries) != 3 {
				t.Fatal(result, observed, f.queries, err)
			}
			if f.queries[1].Get("syncToken") != "baseline" || f.queries[1].Get("pageToken") != "page2" {
				t.Fatal("pagination changed original parameters")
			}
		})
	}
}

func TestInvalidInventoryNeverEmits(t *testing.T) {
	for _, failure := range []string{"missing_token", "missing_id", "conflicting_id", "conflicting_alias", "repeated_page", "permission"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			f.respond = func(w http.ResponseWriter, _ *http.Request) {
				p := contact("1", "Name")
				switch failure {
				case "missing_token":
					writePage(w, []person{p}, "", "")
					return
				case "missing_id":
					p.Metadata.Sources = nil
				case "conflicting_id":
					p.Metadata.Sources = append(p.Metadata.Sources, personSource{Type: "CONTACT", ID: "2"})
				case "conflicting_alias":
					other := contact("2", "Other")
					other.ResourceName = p.ResourceName
					writePage(w, []person{p, other}, "", "token")
					return
				case "repeated_page":
					writePage(w, []person{p}, "loop", "")
					return
				case "permission":
					w.WriteHeader(403)
					return
				}
				writePage(w, []person{p}, "", "token")
			}
			_, observed, err := f.scan(t)
			if err == nil || len(observed) != 0 {
				t.Fatal("incomplete inventory emitted contacts", observed, err)
			}
			var aliases int64
			f.connector.DB.Model(&models.GooglePeopleIdentity{}).Count(&aliases)
			if aliases != 0 {
				t.Fatal("failed scan committed aliases")
			}
		})
	}
}

func TestBaselineAgeQueryChangeAndAccountGuard(t *testing.T) {
	for _, change := range []string{"age", "query", "version", "account"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t)
			f.respond = func(w http.ResponseWriter, _ *http.Request) { writePage(w, nil, "", "baseline") }
			result, observed, err := f.scan(t)
			if err != nil {
				t.Fatal(err)
			}
			f.commit(t, result, observed)
			var cursor cursorState
			json.Unmarshal(result.Cursor, &cursor)
			switch change {
			case "age":
				cursor.FullSyncAt = time.Now().Add(-maxBaselineAge)
			case "query":
				cursor.QueryHash = "different"
			case "version":
				cursor.Version++
			case "account":
				f.request.Credential.OAuthSubject = "different"
			}
			f.request.Cursor, _ = json.Marshal(cursor)
			f.queries = nil
			result, _, err = f.scan(t)
			if change == "account" {
				if err == nil || len(f.queries) != 0 {
					t.Fatal("changed account was scanned", err)
				}
				return
			}
			if err != nil || !result.Complete || len(f.queries) != 1 || f.queries[0].Get("syncToken") != "" {
				t.Fatal("baseline was not rebuilt", result, f.queries, err)
			}
		})
	}
}

func TestPreviewAndTransactionalAliasCommit(t *testing.T) {
	f := newFixture(t)
	f.respond = func(w http.ResponseWriter, _ *http.Request) {
		writePage(w, []person{contact("1", "Name")}, "", "baseline")
	}
	f.request.Preview = true
	_, observed, err := f.scan(t)
	if err != nil || len(observed) != 1 {
		t.Fatal(observed, err)
	}
	for _, model := range []any{&models.GooglePeopleIdentity{}, &models.GooglePeopleScanAlias{}, &models.GooglePeopleScanContact{}, &models.TaskState{}, &models.TaskItem{}, &models.TaskRun{}} {
		var count int64
		if err := f.connector.DB.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("preview changed durable state", model, count, err)
		}
	}
	f.request.Preview = false
	_, observed, err = f.scan(t)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.connector.DB.Create(&models.TaskRun{ID: f.request.RunID, TaskID: f.request.TaskID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.connector.DB.Create(&models.TaskItem{TaskID: f.request.TaskID, SourceItemID: observed[0].ID}).Error; err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("cursor commit failed")
	err = f.connector.DB.Transaction(func(tx *gorm.DB) error {
		if err := f.connector.CommitScan(t.Context(), f.request.RunID, tx); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	var committed, staged int64
	f.connector.DB.Model(&models.GooglePeopleIdentity{}).Count(&committed)
	f.connector.DB.Model(&models.GooglePeopleScanAlias{}).Count(&staged)
	if committed != 0 || staged != 1 {
		t.Fatal("alias commit did not roll back", committed, staged)
	}
	if err := f.connector.AbortScan(t.Context(), f.request.RunID); err != nil {
		t.Fatal(err)
	}
}

func TestContentIsStableAndIncludesCoreFields(t *testing.T) {
	var p person
	err := json.Unmarshal([]byte(`{"resourceName":"people/old","metadata":{"sources":[{"type":"CONTACT","id":"1"}]},"names":[{"displayName":"Alice","metadata":{"primary":true}}],"nicknames":[{"value":"Al"}],"emailAddresses":[{"value":"z@example.com"},{"value":"a@example.com","metadata":{"primary":true}}],"phoneNumbers":[{"value":"123","type":"work"}],"organizations":[{"name":"Company","title":"Engineer","department":"R&D"}],"addresses":[{"formattedValue":"Street, City","type":"home"}],"birthdays":[{"date":{"month":3,"day":14}}],"events":[{"type":"anniversary","date":{"year":2020,"month":1,"day":2}}],"relations":[{"person":"Bob","type":"friend"}],"biographies":[{"value":"<p>Works on <b>search</b>.</p>","contentType":"TEXT_HTML"}],"urls":[{"value":"https://example.com"}],"memberships":[{"contactGroupMembership":{"contactGroupResourceName":"contactGroups/work"}}]}`), &p)
	if err != nil {
		t.Fatal(err)
	}
	before, err := normalize(p, "contact:1")
	if err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{"Alice", "Al", "a@example.com (primary)", "123 (work)", "Company — Engineer — R&D", "Street, City (home)", "--03-14", "2020-01-02 (anniversary)", "Bob (friend)", "Works on **search**.", "https://example.com", "contactGroups/work"} {
		if !strings.Contains(before.Text, phrase) {
			t.Error("missing core content", phrase, before.Text)
		}
	}
	p.ResourceName = "people/new"
	p.Metadata.Sources[0].UpdateTime = time.Now()
	p.EmailAddresses[0], p.EmailAddresses[1] = p.EmailAddresses[1], p.EmailAddresses[0]
	after, err := normalize(p, "contact:1")
	if err != nil || before.sourceItem().Revision != after.sourceItem().Revision || !reflect.DeepEqual(before.Emails, after.Emails) {
		t.Fatal("identity or ordering change altered content", err)
	}
	content, err := (&Connector{}).OpenContent(t.Context(), connectors.ContentRequest{Item: after.sourceItem()})
	if err != nil || !content.TimestampUnset || content.Text != before.Text {
		t.Fatal(content, err)
	}
}

func TestContactFieldPresenceFilters(t *testing.T) {
	f := newFixture(t)
	f.respond = func(w http.ResponseWriter, _ *http.Request) {
		missing := contact("1", "")
		missing.EmailAddresses = []personField{{Value: " "}}
		missing.PhoneNumbers = []personField{}
		present := contact("2", "Alice")
		present.EmailAddresses = []personField{{Value: "alice@example.com"}}
		present.PhoneNumbers = []personField{{Value: "123"}}
		present.Organizations = []personField{{Name: "Company"}}
		writePage(w, []person{missing, present}, "", "baseline")
	}
	result, observed, err := f.scan(t)
	if err != nil || !result.Complete || len(observed) != 2 {
		t.Fatal(result, observed, err)
	}
	for _, field := range []string{"name", "email", "phone", "organization"} {
		for _, operator := range []string{connectors.OpExists, connectors.OpNotExists} {
			t.Run(field+"/"+operator, func(t *testing.T) {
				filter := connectors.Filter{Mode: "simple", Expression: connectors.FilterGroup{
					Rules: []connectors.FilterRule{{Field: field, Operator: operator}},
				}}
				if err := f.connector.ValidateConfig(nil, filter); err != nil {
					t.Fatal(err)
				}
				matches, err := connectors.CompileFilter(filter)
				if err != nil {
					t.Fatal(err)
				}
				matched := []string{}
				for _, contact := range observed {
					if matches(contact.Attributes) {
						matched = append(matched, contact.ID)
					}
				}
				want := "contact:2"
				if operator == connectors.OpNotExists {
					want = "contact:1"
				}
				if len(matched) != 1 || matched[0] != want {
					t.Fatalf("matched %v; want [%s]", matched, want)
				}
			})
		}
	}
}

func TestLargeInventoryUsesOnlyPagedRequests(t *testing.T) {
	for _, total := range []int{10000, 100000} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			f := newFixture(t)
			f.respond = func(w http.ResponseWriter, r *http.Request) {
				start := 0
				fmt.Sscan(r.URL.Query().Get("pageToken"), &start)
				contacts := make([]person, 0, 1000)
				for index := start; index < min(start+1000, total); index++ {
					contacts = append(contacts, contact(fmt.Sprint(index), "Contact"))
				}
				next := ""
				if start+1000 < total {
					next = fmt.Sprint(start + 1000)
				}
				writePage(w, contacts, next, "baseline")
			}
			emitted := 0
			f.request.Emit = func(connectors.SourceItem) error { emitted++; return nil }
			result, err := f.connector.Scan(t.Context(), f.request)
			if err != nil || result.Error != nil || !result.Complete || emitted != total || len(f.queries) != total/1000 || len(result.Cursor) > 1024 {
				t.Fatal(result, emitted, len(f.queries), err)
			}
			if err := f.connector.AbortScan(context.Background(), f.request.RunID); err != nil {
				t.Fatal(err)
			}
			var remaining int64
			f.connector.DB.Model(&models.GooglePeopleScanContact{}).Count(&remaining)
			if remaining != 0 {
				t.Fatal("staging retained contact payloads")
			}
		})
	}
}

func TestTransientFailureAndCancellationDoNotRebaseline(t *testing.T) {
	for _, status := range []int{429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := newFixture(t)
			f.respond = func(w http.ResponseWriter, _ *http.Request) { writePage(w, nil, "", "baseline") }
			baseline, contacts, err := f.scan(t)
			if err != nil {
				t.Fatal(err)
			}
			f.commit(t, baseline, contacts)
			f.queries = nil
			f.respond = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			result, err := f.connector.Scan(ctx, f.request)
			if errors.Join(err, result.Error) == nil || len(result.Cursor) != 0 || result.Complete {
				t.Fatal("transient failure became successful baseline", result, err)
			}
			for _, query := range f.queries {
				if query.Get("syncToken") != "baseline" {
					t.Fatal("transient failure reset cursor", query)
				}
			}
			var state models.TaskState
			if err := f.connector.DB.First(&state, "task_id = ?", f.request.TaskID).Error; err != nil || state.CommittedCursorJSON != string(baseline.Cursor) {
				t.Fatal("transient failure changed committed cursor", err)
			}
		})
	}
}

func TestPreviewCleanupFailureIsReported(t *testing.T) {
	f := newFixture(t)
	f.request.Preview = true
	f.respond = func(w http.ResponseWriter, _ *http.Request) {
		writePage(w, []person{contact("1", "Alice")}, "", "baseline")
	}
	emitted := false
	f.request.Emit = func(connectors.SourceItem) error { emitted = true; return nil }
	cleanupErr := errors.New("preview cleanup failed")
	if err := f.connector.DB.Callback().Delete().Before("gorm:delete").Register("fail_people_preview_cleanup", func(tx *gorm.DB) {
		if emitted {
			tx.AddError(cleanupErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	result, err := f.connector.Scan(t.Context(), f.request)
	if !errors.Is(errors.Join(err, result.Error), cleanupErr) {
		t.Fatal("preview cleanup failure was hidden", err, result.Error)
	}
	if err := f.connector.DB.Callback().Delete().Remove("fail_people_preview_cleanup"); err != nil {
		t.Fatal(err)
	}
	if err := f.connector.AbortScan(t.Context(), f.request.RunID); err != nil {
		t.Fatal(err)
	}
}
