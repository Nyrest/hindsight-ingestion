package email

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/proxy"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type literal struct {
	*strings.Reader
	length int64
}

func (l literal) Size() int64 { return l.length }

type fixture struct {
	t                      testing.TB
	user                   *imapmemserver.User
	server                 *imapserver.Server
	address                string
	connector              *Connector
	mu                     sync.Mutex
	bodyCalls, headerCalls int
	failFetch              bool
	invalidInventory       bool
}
type fixtureSession struct {
	imapserver.Session
	fixture *fixture
}

func (s *fixtureSession) Select(name string, options *imap.SelectOptions) (*imap.SelectData, error) {
	if !options.ReadOnly {
		return nil, errors.New("sync must use EXAMINE")
	}
	data, err := s.Session.Select(name, options)
	s.fixture.mu.Lock()
	invalid := s.fixture.invalidInventory
	s.fixture.mu.Unlock()
	if err == nil && invalid {
		data.UIDNext = 1
	}
	return data, err
}
func (s *fixtureSession) Fetch(w *imapserver.FetchWriter, set imap.NumSet, options *imap.FetchOptions) error {
	s.fixture.mu.Lock()
	fail := s.fixture.failFetch
	for _, section := range options.BodySection {
		if !section.Peek {
			s.fixture.mu.Unlock()
			return errors.New("sync must use PEEK")
		}
		if section.Specifier == imap.PartSpecifierHeader {
			s.fixture.headerCalls++
		} else {
			s.fixture.bodyCalls++
			if len(section.Part) == 0 || section.Part[0] != 1 {
				s.fixture.mu.Unlock()
				return errors.New("attachment or entire message requested")
			}
		}
	}
	s.fixture.mu.Unlock()
	if fail {
		return errors.New("injected FETCH failure")
	}
	return s.Session.Fetch(w, set, options)
}
func newFixture(t testing.TB) *fixture {
	t.Helper()
	db, err := database.Open("sqlite", filepath.Join(t.TempDir(), "email.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sql, _ := db.DB(); _ = sql.Close() })
	memory := imapmemserver.New()
	user := imapmemserver.NewUser("user", "password")
	memory.AddUser(user)
	for _, name := range []string{"INBOX", "Sent", "INBOX/Child"} {
		if err := user.Create(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	f := &fixture{t: t, user: user, connector: &Connector{DB: db}}
	f.server = imapserver.New(&imapserver.Options{InsecureAuth: true, NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
		return &fixtureSession{Session: memory.NewSession(), fixture: f}, nil, nil
	}})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.address = listener.Addr().String()
	go f.server.Serve(listener)
	t.Cleanup(func() { _ = f.server.Close() })
	return f
}
func (f *fixture) append(folder, id, body string) {
	f.t.Helper()
	header := "From: Alice <alice@example.com>\r\nTo: Bob <bob@example.com>\r\nSubject: Project\r\nDate: Mon, 12 Oct 2026 11:00:00 +0800\r\n"
	if id != "" {
		header += "Message-ID: <" + id + "@example.com>\r\n"
	}
	raw := header + "MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n--m\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n--m\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=report.pdf\r\nContent-Transfer-Encoding: base64\r\n\r\nUEZERklMRQ==\r\n--m--\r\n"
	_, err := f.user.Append(folder, literal{strings.NewReader(raw), int64(len(raw))}, &imap.AppendOptions{Time: time.Now()})
	if err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) session() *session {
	f.t.Helper()
	conn, err := net.Dial("tcp", f.address)
	if err != nil {
		f.t.Fatal(err)
	}
	s := &session{conn: conn, timeout: 5 * time.Second, stop: func() bool { return false }, client: imapclient.New(conn, nil)}
	if err := s.client.Login("user", "password").Wait(); err != nil {
		f.t.Fatal(err)
	}
	return s
}
func (f *fixture) scan(task string, cfg map[string]any, cursor json.RawMessage, full, preview, read bool) (connectors.ScanResult, map[string]connectors.SourceItem, map[string]connectors.SourceContent, string) {
	f.t.Helper()
	runID := uuid.NewString()
	control, content := f.session(), f.session()
	defer control.close()
	defer content.close()
	active := &scanSession{content: content, cache: map[string]connectors.SourceContent{}}
	f.connector.sessions.Store(runID, active)
	defer f.connector.sessions.Delete(runID)
	items := map[string]connectors.SourceItem{}
	contents := map[string]connectors.SourceContent{}
	cred := connectors.Credential{Config: map[string]any{"host": "127.0.0.1", "port": 993, "username": "user"}}
	req := connectors.ScanRequest{TaskID: task, RunID: runID, Credential: cred, Config: cfg, Cursor: cursor, Full: full, Preview: preview, Log: func(string, string) {}}
	req.Emit = func(item connectors.SourceItem) error {
		items[item.ID] = item
		if read {
			text, err := f.connector.OpenContent(f.t.Context(), connectors.ContentRequest{Config: cfg, Item: item})
			if err != nil {
				return err
			}
			contents[item.ID] = text
		}
		return nil
	}
	if !preview {
		if err := f.connector.DB.Create(&models.TaskRun{ID: runID, TaskID: task, SyncMode: models.SyncFull}).Error; err != nil {
			f.t.Fatal(err)
		}
	}
	scan := scanner{connector: f.connector, ctx: f.t.Context(), req: req, control: control, active: active, account: AccountKey(cred), full: full, emitted: map[string]bool{}}
	res, err := scan.run()
	if err != nil {
		res.Error = err
	}
	if res.Error == nil && !preview {
		mode := models.SyncIncremental
		if res.Complete {
			mode = models.SyncFull
		}
		f.connector.DB.Model(&models.TaskRun{}).Where("id = ?", runID).Update("sync_mode", mode)
		if err := f.connector.DB.Transaction(func(tx *gorm.DB) error { return f.connector.CommitScan(f.t.Context(), runID, tx) }); err != nil {
			f.t.Fatal(err)
		}
	}
	return res, items, contents, runID
}
func config() map[string]any {
	return map[string]any{"folders": []string{"INBOX", "Sent"}, "includeSubfolders": true, "stripQuotedText": true}
}

func TestLogicalMailCopiesConflictsAndMoves(t *testing.T) {
	f := newFixture(t)
	f.append("INBOX", "same", "Answer\n\nOn Monday wrote:\n> Old text")
	f.append("Sent", "same", "Answer\n\nOn Monday wrote:\n> Old text")
	f.append("INBOX/Child", "same", "Different answer")
	res, items, contents, _ := f.scan("task", config(), nil, true, false, true)
	if res.Error != nil || !res.Complete || len(items) != 2 {
		t.Fatalf("first scan: %v %d", res.Error, len(items))
	}
	var id string
	for key, content := range contents {
		if strings.Contains(content.Text, "Answer") {
			id = key
			if strings.Contains(content.Text, "Old text") || !strings.Contains(content.Text, "report.pdf") {
				t.Fatal(content.Text)
			}
		}
	}
	calls := f.bodyCalls
	res, again, _, _ := f.scan("task", config(), res.Cursor, true, false, false)
	if res.Error != nil || len(again) != 2 || f.bodyCalls != calls {
		t.Fatalf("known UID bodies re-fetched: %v %d", res.Error, f.bodyCalls-calls)
	}
	f.append("Sent", "same", "Answer\n\nOn Monday wrote:\n> Old text")
	incremental, copy, _, _ := f.scan("task", config(), res.Cursor, false, false, false)
	if incremental.Error != nil || incremental.Complete || len(copy) != 1 || copy[id].ID != id || f.bodyCalls != calls+1 {
		t.Fatalf("new UID copy was not body-verified: %+v %+v", incremental, copy)
	}
	if err := f.user.Delete("INBOX"); err != nil {
		t.Fatal(err)
	}
	if err := f.user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	moved, remaining, _, _ := f.scan("task", config(), incremental.Cursor, false, false, false)
	if moved.Error != nil || !moved.Complete || remaining[id].ID != id {
		t.Fatalf("move/UIDVALIDITY: %+v", moved)
	}
	cfg := config()
	cfg["stripQuotedText"] = false
	changed, unchanged, all, _ := f.scan("task", cfg, moved.Cursor, true, false, true)
	if changed.Error != nil || unchanged[id].ID != id || unchanged[id].Revision == items[id].Revision || !strings.Contains(all[id].Text, "Old text") {
		t.Fatal("processing change changed identity or did not reload body", changed.Error)
	}
}

func TestBatchFetchCommitPagesAndPreview(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 205; i++ {
		f.append("INBOX", strconv.Itoa(i), "Short body")
	}
	res, items, _, _ := f.scan("task", config(), nil, true, false, false)
	if res.Error != nil || len(items) != 205 || f.bodyCalls != 5 || f.headerCalls != 3 {
		t.Fatalf("batching: %v items=%d body=%d headers=%d", res.Error, len(items), f.bodyCalls, f.headerCalls)
	}
	for _, model := range []any{&models.EmailIdentity{}, &models.EmailOccurrence{}} {
		var count int64
		f.connector.DB.Model(model).Count(&count)
		if count != 205 {
			t.Fatal("composite-key commit lost a page", count)
		}
	}
	var before int64
	f.connector.DB.Model(&models.TaskRun{}).Count(&before)
	f.append("INBOX", "new", "New body")
	preview, _, _, runID := f.scan("task", config(), nil, true, true, true)
	if preview.Error != nil {
		t.Fatal(preview.Error)
	}
	var after, staged, occurrences int64
	f.connector.DB.Model(&models.TaskRun{}).Count(&after)
	f.connector.DB.Model(&models.EmailScanOccurrence{}).Where("run_id = ?", runID).Count(&staged)
	f.connector.DB.Model(&models.EmailOccurrence{}).Count(&occurrences)
	if before != after || staged != 0 || occurrences != 205 {
		t.Fatalf("preview wrote storage: %d %d %d", after, staged, occurrences)
	}
}

func TestUIDFallbackBodyLimitAndFailure(t *testing.T) {
	f := newFixture(t)
	f.append("INBOX", "", "No Message ID")
	f.append("Sent", "", "No Message ID")
	f.append("INBOX", "large", "Long body beyond limit")
	cfg := config()
	cfg["maxBodyBytes"] = 4
	res, items, contents, _ := f.scan("task", cfg, nil, true, false, true)
	if res.Error != nil || len(items) != 3 {
		t.Fatal(res.Error, len(items))
	}
	for _, content := range contents {
		if len(content.Warnings) == 0 || !strings.Contains(content.Text, "Body omitted") {
			t.Fatal(content)
		}
	}
	f.mu.Lock()
	f.failFetch = true
	f.mu.Unlock()
	failed, _, _, runID := f.scan("task", cfg, res.Cursor, true, false, true)
	if failed.Error == nil || failed.Complete {
		t.Fatal("FETCH failure authorized commit")
	}
	if err := f.connector.AbortScan(t.Context(), runID); err != nil {
		t.Fatal(err)
	}
	cfg["excludeFolders"] = []string{"INBOX", "Sent"}
	empty, items, _, _ := f.scan("task", cfg, nil, true, false, false)
	if empty.Error != nil || !empty.Complete || len(items) != 0 {
		t.Fatal("explicit empty scope failed", empty)
	}
	var count int64
	f.connector.DB.Model(&models.EmailOccurrence{}).Count(&count)
	if count != 0 {
		t.Fatal("empty full scan kept old occurrences")
	}
}

func TestFolderSelectionAndSpecialUse(t *testing.T) {
	list := []*imap.ListData{{Mailbox: "INBOX", Delim: '/'}, {Mailbox: "INBOX/Child", Delim: '/'}, {Mailbox: "INBOX/Child/Deep", Delim: '/'}, {Mailbox: "Container", Delim: '/', Attrs: []imap.MailboxAttr{imap.MailboxAttrNoSelect}}, {Mailbox: "Container/Sent", Delim: '/', Attrs: []imap.MailboxAttr{imap.MailboxAttrSent}}}
	browse := browseMailboxes(list, "")
	if len(browse.DefaultSelection) != 2 || browse.DefaultSelection[0] != "Container/Sent" {
		t.Fatal(browse)
	}
	cfg := map[string]any{"folders": []string{"INBOX"}, "excludeFolders": []string{"INBOX/Child"}}
	selected, err := selectedMailboxes(list, cfg)
	if err != nil || len(selected) != 1 {
		t.Fatal(selected, err)
	}
	cfg["folders"] = []string{"missing"}
	if _, err = selectedMailboxes(list, cfg); err == nil {
		t.Fatal("missing folder accepted")
	}
}

func TestQuoteSafetyAndCharset(t *testing.T) {
	for _, text := range []string{"Reply\n> question\nInline answer\n> context\nMore answer", "---------- Forwarded message ----------\n> forwarded", "From: a\nSent: b\nTo: c\nSubject: d\nThis is new content", "My reference:\n> Original quotation", "________________________________\nFrom: a\nSent: b\nTo: c\nSubject: d\nOld message\nInline answer"} {
		if stripReplyQuotes(text) != text {
			t.Fatal("new content removed", text)
		}
	}
	if stripped := stripReplyQuotes("Reply\n\nOn Monday Alice wrote:\n> Old message\n"); stripped != "Reply" {
		t.Fatal("recognized reply history was kept", stripped)
	}
	ambiguous, err := renderedBody(textPart{Type: "text/html"}, `<div class="gmail_quote"><blockquote>Original quotation</blockquote></div>`, true)
	if err != nil || !strings.Contains(ambiguous, "Original quotation") {
		t.Fatal("unattributed HTML quotation was removed", ambiguous, err)
	}
	forwarded, err := renderedBody(textPart{Type: "text/html"}, `<p>转发的邮件</p><div class="gmail_quote"><div class="gmail_attr">On Monday wrote:</div><blockquote>Forwarded content</blockquote></div>`, true)
	if err != nil || !strings.Contains(forwarded, "Forwarded content") {
		t.Fatal("forwarded HTML content was removed", forwarded, err)
	}
	decoded, err := decodePart(textPart{Type: "text/plain", Params: map[string]string{"charset": "iso-8859-1"}, Encoding: "quoted-printable"}, strings.NewReader("caf=E9"), 100)
	if err != nil || decoded.Omitted || decoded.Text != "café" || decoded.Bytes != 5 {
		t.Fatal(decoded, err)
	}
	for _, html := range []string{`<p>Reply</p><div class="gmail_quote"><div class="gmail_attr">On Monday wrote:</div><blockquote>Old content</blockquote></div>`, `<p>Forwarded message</p><div class="gmail_quote"><blockquote>Old content</blockquote></div>`, `<div class="gmail_quote"><blockquote>Old content</blockquote><p>Inline answer</p></div>`} {
		body, err := renderedBody(textPart{Type: "text/html"}, html, true)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(html, "Inline answer") && !strings.Contains(body, "Inline answer") {
			t.Fatal(body)
		}
		if strings.Contains(html, "Forwarded message") && !strings.Contains(body, "Old content") {
			t.Fatal(body)
		}
		if strings.Contains(html, "gmail_attr") && strings.Contains(body, "Old content") {
			t.Fatal(body)
		}
	}
}

func TestMultipartAttachmentDoesNotSelectItsTextChildren(t *testing.T) {
	plain := &imap.BodyStructureSinglePart{Type: "text", Subtype: "plain", Size: 20}
	attached := &imap.BodyStructureMultiPart{Subtype: "mixed", Children: []imap.BodyStructure{plain}, Extended: &imap.BodyStructureMultiPartExt{Disposition: &imap.BodyStructureDisposition{Value: "attachment", Params: map[string]string{"filename": "attached-message.mime"}}}}
	root := &imap.BodyStructureMultiPart{Subtype: "mixed", Children: []imap.BodyStructure{plain, attached}}
	parts, attachments := bodyParts(root, nil)
	if len(parts) != 1 || len(parts[0].Path) != 1 || parts[0].Path[0] != 1 || len(attachments) != 1 || attachments[0].Name != "attached-message.mime" {
		t.Fatalf("multipart attachment text was selected: parts=%v attachments=%v", parts, attachments)
	}
}

func TestStrictSTARTTLSAndAccountReservation(t *testing.T) {
	f := newFixture(t)
	host, port, _ := net.SplitHostPort(f.address)
	n, _ := strconv.Atoi(port)
	cred := connectors.Credential{Config: map[string]any{"host": host, "port": n, "username": "user", "security": "starttls", "connectionTimeout": 1, "commandTimeout": 1}, Secrets: map[string]string{"password": "password"}, Proxy: proxy.Config{Type: "none"}}
	if _, err := openSession(t.Context(), cred); err == nil {
		t.Fatal("plaintext server accepted without STARTTLS")
	}
	first, err := f.connector.reserve(t.Context(), cred, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := f.connector.reserve(ctx, cred, 2); err == nil {
		t.Fatal("partial reservations can deadlock")
	}
	spare, err := f.connector.reserve(t.Context(), cred, 1)
	if err != nil {
		t.Fatal(err)
	}
	spare()
	old := AccountKey(cred)
	cred.Secrets["password"] = "rotated"
	if AccountKey(cred) != old {
		t.Fatal("password changed account identity")
	}
	cred.Config["username"] = "other"
	if AccountKey(cred) == old {
		t.Fatal("account change reused identity")
	}
}

func TestTLSRejectsUntrustedCertificate(t *testing.T) {
	server := httptest.NewTLSServer(nil)
	defer server.Close()
	host, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	n, _ := strconv.Atoi(port)
	cred := connectors.Credential{Config: map[string]any{"host": host, "port": n, "username": "user", "security": "tls", "connectionTimeout": 2, "commandTimeout": 2}, Secrets: map[string]string{"password": "password"}, Proxy: proxy.Config{Type: "none"}}
	if _, err := openSession(t.Context(), cred); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("untrusted TLS certificate was not rejected: %v", err)
	}
}

func TestInvalidEmptyInventoryBlocksCommit(t *testing.T) {
	f := newFixture(t)
	f.append("INBOX", "mail", "Body")
	f.mu.Lock()
	f.invalidInventory = true
	f.mu.Unlock()
	res, _, _, _ := f.scan("task", config(), nil, true, false, false)
	if res.Error == nil || res.Complete {
		t.Fatal("nonempty mailbox with invalid UIDNEXT authorized an empty inventory")
	}
}

func BenchmarkEmailInventory(b *testing.B) {
	for range b.N {
		b.StopTimer()
		f := newFixture(b)
		for i := 0; i < 10000; i++ {
			f.append("INBOX", strconv.Itoa(i), "A short message body")
		}
		b.StartTimer()
		res, items, _, _ := f.scan("task", config(), nil, true, false, false)
		if res.Error != nil || len(items) != 10000 || f.headerCalls != 100 || f.bodyCalls != 200 {
			b.Fatalf("large inventory: %v items=%d headers=%d body=%d", res.Error, len(items), f.headerCalls, f.bodyCalls)
		}
	}
}
