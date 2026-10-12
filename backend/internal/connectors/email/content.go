package email

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset"
	"golang.org/x/net/html"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

type textPart struct {
	Path     []int
	Type     string
	Params   map[string]string
	Encoding string
	Size     uint32
}
type attachment struct {
	Name string
	Type string
	Size uint32
}
type mailSnapshot struct {
	Subject     string
	From        string
	To          string
	Cc          string
	Date        time.Time
	MessageID   string
	InReplyTo   string
	References  []string
	Size        int64
	Parts       []textPart
	Attachments []attachment
	ContentHash string
	HeaderHash  string
	Omitted     bool
}
type contentLocation struct {
	RunID    string
	Folder   string
	Validity uint64
	UID      imap.UID
	Snapshot mailSnapshot
}
type scanSession struct {
	content *session
	folder  string
	cache   map[string]connectors.SourceContent
}

func digest(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(h, "%d:", len(part))
		_, _ = io.WriteString(h, part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func snapshotOf(fetched *imapclient.FetchMessageBuffer) (mailSnapshot, error) {
	section := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, Peek: true, Partial: &imap.SectionPartial{Size: 256 << 10}}
	raw := fetched.FindBodySection(section)
	if raw == nil || fetched.BodyStructure == nil {
		return mailSnapshot{}, fmt.Errorf("missing message header or MIME structure for UID %d", fetched.UID)
	}
	entity, err := message.Read(bytes.NewReader(raw))
	if err != nil {
		return mailSnapshot{}, err
	}
	header := entity.Header
	decode := func(key string) (string, error) { return header.Text(key) }
	snapshot := mailSnapshot{Size: fetched.RFC822Size, Date: fetched.InternalDate, Parts: []textPart{}, Attachments: []attachment{}}
	for key, target := range map[string]*string{"Subject": &snapshot.Subject, "From": &snapshot.From, "To": &snapshot.To, "Cc": &snapshot.Cc} {
		if *target, err = decode(key); err != nil {
			return mailSnapshot{}, err
		}
	}
	if date, parseErr := mail.ParseDate(header.Get("Date")); parseErr == nil {
		snapshot.Date = date
	}
	snapshot.MessageID = normalizedMessageID(header.Get("Message-ID"))
	snapshot.InReplyTo = normalizedMessageID(header.Get("In-Reply-To"))
	for _, ref := range messageIDs.FindAllString(header.Get("References"), -1) {
		if normalized := normalizedMessageID(ref); normalized != "" {
			snapshot.References = append(snapshot.References, normalized)
		}
	}
	snapshot.Parts, snapshot.Attachments = bodyParts(fetched.BodyStructure, nil)
	snapshot.HeaderHash = digest(snapshot.Subject, snapshot.From, snapshot.To, snapshot.Cc, header.Get("Date"), snapshot.InReplyTo, strings.Join(snapshot.References, " "))
	return snapshot, nil
}

var messageIDs = regexp.MustCompile(`<[^<>\s]+@[^<>\s]+>`)

func normalizedMessageID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw != "" && !strings.HasPrefix(raw, "<") {
		raw = "<" + raw + ">"
	}
	if messageIDs.FindString(raw) != raw {
		return ""
	}
	return raw
}

func bodyParts(structure imap.BodyStructure, path []int) ([]textPart, []attachment) {
	if single, ok := structure.(*imap.BodyStructureSinglePart); ok {
		if len(path) == 0 {
			path = []int{1}
		}
		disposition := single.Disposition()
		attached := disposition != nil && strings.EqualFold(disposition.Value, "attachment")
		if attached || single.Filename() != "" || (single.MediaType() != "text/plain" && single.MediaType() != "text/html") {
			return nil, []attachment{{Name: single.Filename(), Type: single.MediaType(), Size: single.Size}}
		}
		return []textPart{{Path: path, Type: single.MediaType(), Params: single.Params, Encoding: single.Encoding, Size: single.Size}}, nil
	}
	multi := structure.(*imap.BodyStructureMultiPart)
	if disposition := multi.Disposition(); disposition != nil && (strings.EqualFold(disposition.Value, "attachment") || disposition.Params["filename"] != "") {
		return nil, []attachment{{Name: disposition.Params["filename"], Type: multi.MediaType()}}
	}
	parts := []textPart{}
	attachments := []attachment{}
	for index, child := range multi.Children {
		childPath := append(append([]int(nil), path...), index+1)
		childParts, childAttachments := bodyParts(child, childPath)
		parts = append(parts, childParts...)
		attachments = append(attachments, childAttachments...)
	}
	if strings.EqualFold(multi.Subtype, "alternative") {
		for _, part := range parts {
			if part.Type == "text/plain" {
				return []textPart{part}, attachments
			}
		}
		if len(parts) > 0 {
			parts = parts[:1]
		}
	}
	return parts, attachments
}

type decodedText struct {
	Text    string
	Bytes   int64
	Omitted bool
}

func decodePart(part textPart, raw io.Reader, limit int64) (decodedText, error) {
	header := message.HeaderFromMap(nil)
	header.SetContentType(part.Type, part.Params)
	header.Set("Content-Transfer-Encoding", part.Encoding)
	entity, err := message.New(header, raw)
	if err != nil {
		return decodedText{}, err
	}
	body, err := io.ReadAll(io.LimitReader(entity.Body, limit+1))
	if err != nil {
		return decodedText{}, err
	}
	if int64(len(body)) > limit {
		return decodedText{Omitted: true}, nil
	}
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	if part.Type == "text/plain" && strings.EqualFold(part.Params["format"], "flowed") {
		text = unflow(text, strings.EqualFold(part.Params["delsp"], "yes"))
	}
	return decodedText{Text: text, Bytes: int64(len(body))}, nil
}

func unflow(text string, deleteSpace bool) string {
	lines := strings.Split(text, "\n")
	out := []string{}
	for index := 0; index < len(lines); index++ {
		line := strings.TrimPrefix(lines[index], " ")
		for index+1 < len(lines) && strings.HasSuffix(line, " ") && line != "-- " && !strings.HasPrefix(line, ">") && !strings.HasPrefix(lines[index+1], ">") {
			if deleteSpace {
				line = strings.TrimSuffix(line, " ")
			}
			index++
			line += strings.TrimPrefix(lines[index], " ")
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func renderedBody(part textPart, text string, strip bool) (string, error) {
	if part.Type == "text/plain" {
		if strip {
			text = stripReplyQuotes(text)
		}
		return text, nil
	}
	dom, err := html.Parse(strings.NewReader(text))
	if err != nil {
		return "", err
	}
	cleanHTML(dom, strip && !hasForwardMarker(dom))
	converted, err := htmltomarkdown.ConvertNode(dom)
	return string(converted), err
}

func cleanHTML(root *html.Node, strip bool) {
	for child := root.FirstChild; child != nil; {
		next := child.NextSibling
		remove := child.Type == html.ElementNode && (child.Data == "script" || child.Data == "style" || child.Data == "iframe" || child.Data == "object" || child.Data == "img")
		if strip {
			for _, attr := range child.Attr {
				if attr.Key == "class" && slicesContainsWord(attr.Val, "gmail_quote") && quoteOnlyHTML(child) {
					remove = true
				}
			}
		}
		if remove {
			root.RemoveChild(child)
		} else {
			cleanHTML(child, strip)
		}
		child = next
	}
}
func slicesContainsWord(list, word string) bool {
	for _, entry := range strings.Fields(list) {
		if entry == word {
			return true
		}
	}
	return false
}
func hasForwardMarker(root *html.Node) bool {
	if root.Type == html.TextNode && forwardMarker(root.Data) {
		return true
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if hasForwardMarker(child) {
			return true
		}
	}
	return false
}

// Preserve gmail_quote wrappers containing possible inline answers. Only the
// standard attribution plus blockquote shape is safe to discard automatically.
func quoteOnlyHTML(root *html.Node) bool {
	quoted := false
	attributed := false
	for node := root.FirstChild; node != nil; node = node.NextSibling {
		if node.Type == html.TextNode && strings.TrimSpace(node.Data) == "" {
			continue
		}
		if node.Type == html.ElementNode && node.Data == "blockquote" {
			quoted = true
			continue
		}
		attribution := false
		for _, attr := range node.Attr {
			if attr.Key == "class" && slicesContainsWord(attr.Val, "gmail_attr") {
				attribution = true
			}
		}
		if !attribution {
			return false
		}
		attributed = true
	}
	return quoted && attributed
}

var wroteLine = regexp.MustCompile(`(?i)^On .+wrote:\s*$`)

func forwardMarker(text string) bool {
	return strings.Contains(strings.ToLower(text), "forwarded message") || strings.Contains(text, "转发邮件") || strings.Contains(text, "转发的邮件")
}

func stripReplyQuotes(text string) string {
	if forwardMarker(text) {
		return text
	}
	lines := strings.Split(text, "\n")
	start := len(lines)
	quoted := false
	for start > 0 {
		line := strings.TrimSpace(lines[start-1])
		if line == "" || strings.HasPrefix(line, ">") {
			quoted = quoted || strings.HasPrefix(line, ">")
			start--
		} else {
			break
		}
	}
	if quoted && start > 0 && wroteLine.MatchString(strings.TrimSpace(lines[start-1])) {
		return strings.TrimRight(strings.Join(lines[:start-1], "\n"), "\n")
	}
	return text
}

func mailContent(snapshot mailSnapshot, body string) connectors.SourceContent {
	var text strings.Builder
	fmt.Fprintf(&text, "# %s\n\nFrom: %s\nTo: %s\nCc: %s\nDate: %s\nMessage-ID: %s\nIn-Reply-To: %s\n\n## Message\n\n", snapshot.Subject, snapshot.From, snapshot.To, snapshot.Cc, snapshot.Date.Format(time.RFC3339), snapshot.MessageID, snapshot.InReplyTo)
	content := connectors.SourceContent{Timestamp: snapshot.Date}
	if snapshot.Omitted {
		body = "Body omitted: exceeds the configured decoded body size limit."
		content.Warnings = []string{body}
	} else if strings.TrimSpace(body) == "" {
		body = "No new message body."
	}
	text.WriteString(body)
	if len(snapshot.Attachments) > 0 {
		text.WriteString("\n\n## Attachments (content not imported)\n\n")
		for _, attached := range snapshot.Attachments {
			if attached.Size > 0 {
				fmt.Fprintf(&text, "- %s (%s, %d encoded bytes)\n", attached.Name, attached.Type, attached.Size)
			} else {
				fmt.Fprintf(&text, "- %s (%s)\n", attached.Name, attached.Type)
			}
		}
	}
	content.Text = text.String()
	return content
}

func sourceMail(snapshot mailSnapshot, id string, cfg map[string]any) connectors.SourceItem {
	limit, _ := bodyLimit(cfg)
	revision := digest("email-body-v1", snapshot.ContentHash, strconv.FormatBool(snapshot.Omitted), strconv.FormatBool(connectors.BoolDefault(cfg, "stripQuotedText", true)), strconv.FormatInt(limit, 10))
	metadata := map[string]string{"email_message_id": snapshot.MessageID, "email_in_reply_to": snapshot.InReplyTo, "email_from": snapshot.From, "email_to": snapshot.To, "email_cc": snapshot.Cc, "email_subject": snapshot.Subject, "email_has_attachments": strconv.FormatBool(len(snapshot.Attachments) > 0)}
	metadata["email_identity_kind"] = "message_id"
	if snapshot.MessageID == "" || snapshot.ContentHash == "" {
		metadata["email_identity_kind"] = "uid_fallback"
	}
	if len(snapshot.References) > 0 {
		metadata["email_thread_root"] = snapshot.References[0]
		encoded, _ := json.Marshal(snapshot.References)
		metadata["email_references"] = string(encoded)
	}
	return connectors.SourceItem{ID: id, Name: snapshot.Subject, Kind: connectors.KindDocument, Revision: revision, ModifiedAt: snapshot.Date, MIMEType: "text/markdown", Size: snapshot.Size, Metadata: metadata, Attributes: map[string]any{"subject": snapshot.Subject, "from": snapshot.From, "to": snapshot.To, "cc": snapshot.Cc, "date": snapshot.Date, "size": float64(snapshot.Size), "hasAttachments": len(snapshot.Attachments) > 0}}
}

func (c *Connector) OpenContent(ctx context.Context, req connectors.ContentRequest) (connectors.SourceContent, error) {
	var location contentLocation
	if err := json.Unmarshal(req.Item.Opaque, &location); err != nil {
		return connectors.SourceContent{}, err
	}
	active, ok := c.sessions.Load(location.RunID)
	if !ok {
		return connectors.SourceContent{}, fmt.Errorf("mail scan session is no longer active")
	}
	scan := active.(*scanSession)
	if content, ok := scan.cache[req.Item.ID]; ok {
		delete(scan.cache, req.Item.ID)
		return content, nil
	}
	if err := ctx.Err(); err != nil {
		return connectors.SourceContent{}, err
	}
	if scan.folder != location.Folder {
		status, err := examine(scan.content, location.Folder)
		if err != nil {
			return connectors.SourceContent{}, redactError(err, req.Credential)
		}
		if uint64(status.UIDValidity) != location.Validity {
			return connectors.SourceContent{}, fmt.Errorf("mailbox UIDVALIDITY changed while reading content")
		}
		scan.folder = location.Folder
	}
	texts, omitted, err := fetchBody(scan.content, location.UID, location.Snapshot, req.Config)
	if err != nil {
		return connectors.SourceContent{}, redactError(err, req.Credential)
	}
	snapshot := location.Snapshot
	snapshot.Omitted = omitted
	return mailContent(snapshot, texts), nil
}
