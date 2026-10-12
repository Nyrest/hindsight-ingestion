package email

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func examine(s *session, folder string) (*imap.SelectData, error) {
	return command(s, func() (*imap.SelectData, error) {
		return s.client.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	})
}

func fetchHeaders(s *session, uids []imap.UID) (map[imap.UID]mailSnapshot, error) {
	return command(s, func() (map[imap.UID]mailSnapshot, error) {
		section := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, Peek: true, Partial: &imap.SectionPartial{Size: 256 << 10}}
		fetch := s.client.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true, InternalDate: true, RFC822Size: true, BodyStructure: &imap.FetchItemBodyStructure{Extended: true}, BodySection: []*imap.FetchItemBodySection{section}})
		success := false
		defer func() {
			if !success {
				_ = s.conn.Close()
			}
			_ = fetch.Close()
		}()
		out := map[imap.UID]mailSnapshot{}
		for msg := fetch.Next(); msg != nil; msg = fetch.Next() {
			buffer := &imapclient.FetchMessageBuffer{}
			for data := msg.Next(); data != nil; data = msg.Next() {
				switch data := data.(type) {
				case imapclient.FetchItemDataUID:
					buffer.UID = data.UID
				case imapclient.FetchItemDataInternalDate:
					buffer.InternalDate = data.Time
				case imapclient.FetchItemDataRFC822Size:
					buffer.RFC822Size = data.Size
				case imapclient.FetchItemDataBodyStructure:
					buffer.BodyStructure = data.BodyStructure
				case imapclient.FetchItemDataBodySection:
					if data.Literal == nil || data.Section.Specifier != imap.PartSpecifierHeader || data.Literal.Size() >= 256<<10 {
						return nil, fmt.Errorf("missing or oversized message header")
					}
					raw, err := io.ReadAll(io.LimitReader(data.Literal, 256<<10))
					if err != nil {
						return nil, err
					}
					buffer.BodySection = append(buffer.BodySection, imapclient.FetchBodySectionBuffer{Section: data.Section, Bytes: raw})
				}
			}
			if !slices.Contains(uids, buffer.UID) {
				return nil, fmt.Errorf("unexpected UID in header fetch")
			}
			if _, ok := out[buffer.UID]; ok {
				return nil, fmt.Errorf("duplicate UID in header fetch")
			}
			snapshot, err := snapshotOf(buffer)
			if err != nil {
				return nil, err
			}
			out[buffer.UID] = snapshot
		}
		if err := fetch.Close(); err != nil {
			return nil, err
		}
		if len(out) != len(uids) {
			return nil, fmt.Errorf("messages disappeared during header fetch")
		}
		success = true

		return out, nil
	})
}

func fetchBody(s *session, uid imap.UID, snapshot mailSnapshot, cfg map[string]any) (string, bool, error) {
	body, _, omitted, err := readBody(s, uid, snapshot, cfg)
	return body, omitted, err
}

func readBody(s *session, uid imap.UID, snapshot mailSnapshot, cfg map[string]any) (body, hash string, omitted bool, err error) {
	err = readBodies(s, []imap.UID{uid}, map[imap.UID]mailSnapshot{uid: snapshot}, cfg, func(_ imap.UID, b, h string, o bool) error { body, hash, omitted = b, h, o; return nil })
	return
}

// Group only identical selected text structures. A union of arbitrary MIME part
// paths could accidentally request an attachment from another message.
func readBodies(s *session, uids []imap.UID, snapshots map[imap.UID]mailSnapshot, cfg map[string]any, consume func(imap.UID, string, string, bool) error) error {
	groups := map[string][]imap.UID{}
	keys := []string{}
	for _, uid := range uids {
		parts := append([]textPart(nil), snapshots[uid].Parts...)
		for i := range parts {
			parts[i].Size = 0
		}
		encoded, _ := json.Marshal(parts)
		key := string(encoded)
		if _, ok := groups[key]; !ok {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], uid)
	}
	for _, key := range keys {
		group := groups[key]
		for len(group) > 0 {
			n := min(len(group), 50)
			if err := fetchBodies(s, group[:n], snapshots, cfg, consume); err != nil {
				return err
			}
			group = group[n:]
		}
	}
	return nil
}

// Literals are decoded one message at a time and consumed synchronously; no
// whole-batch body cache exists. PEEK requests never include attachment parts.
func fetchBodies(s *session, uids []imap.UID, snapshots map[imap.UID]mailSnapshot, cfg map[string]any, consume func(imap.UID, string, string, bool) error) error {
	limit, err := bodyLimit(cfg)
	if err != nil {
		return err
	}
	wireLimit := limit*4 + 4096
	parts := snapshots[uids[0]].Parts
	sections := make([]*imap.FetchItemBodySection, 0, len(parts))
	for _, part := range parts {
		sections = append(sections, &imap.FetchItemBodySection{Part: part.Path, Peek: true, Partial: &imap.SectionPartial{Size: wireLimit}})
	}
	if len(parts) == 0 {
		for _, uid := range uids {
			manifest, _ := json.Marshal(snapshots[uid].Attachments)
			if err := consume(uid, "", digest(string(manifest)), false); err != nil {
				return err
			}
		}
		return nil
	}
	timer := time.AfterFunc(s.timeout, func() { _ = s.conn.Close() })
	defer timer.Stop()
	fetch := s.client.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true, BodySection: sections})
	success := false
	defer func() {
		if !success {
			_ = s.conn.Close()
		}
		_ = fetch.Close()
	}()
	observed := map[imap.UID]bool{}
	for msg := fetch.Next(); msg != nil; msg = fetch.Next() {
		uid := imap.UID(0)
		rawTexts := make([]string, len(parts))
		sizes := make([]int64, len(parts))
		seen := make([]bool, len(parts))
		remaining := limit
		omitted := false
		for data := msg.Next(); data != nil; data = msg.Next() {
			switch data := data.(type) {
			case imapclient.FetchItemDataUID:
				uid = data.UID
			case imapclient.FetchItemDataBodySection:
				index := -1
				for i, section := range sections {
					if slices.Equal(data.Section.Part, section.Part) && data.Section.Specifier == section.Specifier {
						index = i
						break
					}
				}
				if index < 0 || data.Literal == nil {
					return fmt.Errorf("unexpected or missing text literal")
				}
				if seen[index] {
					return fmt.Errorf("duplicate text part")
				}
				seen[index] = true
				sizes[index] = data.Literal.Size()
				decoded, decodeErr := decodePart(parts[index], data.Literal, remaining)
				_, drainErr := io.Copy(io.Discard, data.Literal)
				if decodeErr != nil {
					return decodeErr
				}
				if drainErr != nil {
					return drainErr
				}
				if decoded.Omitted {
					omitted = true
				} else {
					rawTexts[index] = decoded.Text
					remaining -= decoded.Bytes
				}
			}
		}
		snapshot, ok := snapshots[uid]
		if !ok || observed[uid] {
			return fmt.Errorf("unexpected or duplicate message UID")
		}
		observed[uid] = true
		for i, part := range snapshot.Parts {
			if !seen[i] || sizes[i] != min(int64(part.Size), wireLimit) {
				return fmt.Errorf("missing or changed body literal")
			}
			if int64(part.Size) > wireLimit {
				omitted = true
			}
		}
		body, hash := "", ""
		if !omitted {
			rendered := make([]string, len(parts))
			for i, text := range rawTexts {
				converted, err := renderedBody(parts[i], text, connectors.BoolDefault(cfg, "stripQuotedText", true))
				if err != nil {
					return err
				}
				rendered[i] = converted
			}
			body = strings.Join(rendered, "\n\n")
			manifest, _ := json.Marshal(snapshot.Attachments)
			hash = digest(append(rawTexts, string(manifest))...)
			if len(body) > 32<<20 {
				return fmt.Errorf("rendered body exceeds the 32 MiB content cache limit")
			}
		}
		// Destination calls can take longer than an IMAP command. Suspend the network
		// timeout while the consumer submits this message, then resume it for FETCH.
		timer.Stop()
		err := consume(uid, body, hash, omitted)
		timer.Reset(s.timeout)
		if err != nil {
			return err
		}
	}
	if err := fetch.Close(); err != nil {
		return err
	}
	if len(observed) != len(uids) {
		return fmt.Errorf("messages disappeared during body fetch")
	}
	success = true
	return nil
}
