package email

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/emersion/go-imap/v2"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func mailboxes(s *session) ([]*imap.ListData, error) {
	return command(s, func() ([]*imap.ListData, error) {
		options := &imap.ListOptions{}
		if s.client.Caps().Has(imap.CapSpecialUse) {
			options.ReturnSpecialUse = true
		}
		return s.client.List("", "*", options).Collect()
	})
}

func descendant(name, parent string, delim rune) bool {
	return delim != 0 && strings.HasPrefix(name, parent+string(delim))
}

func selectedMailboxes(list []*imap.ListData, cfg map[string]any) ([]*imap.ListData, error) {
	roots, err := folderNames(cfg, "folders")
	if err != nil {
		return nil, err
	}
	excluded, err := folderNames(cfg, "excludeFolders")
	if err != nil {
		return nil, err
	}
	for _, root := range roots {
		if !slices.ContainsFunc(list, func(m *imap.ListData) bool {
			return m.Mailbox == root && !slices.Contains(m.Attrs, imap.MailboxAttrNoSelect)
		}) {
			return nil, fmt.Errorf("selected mailbox is missing or not selectable: %s", root)
		}
	}
	selected := []*imap.ListData{}
	for _, mailbox := range list {
		if slices.Contains(mailbox.Attrs, imap.MailboxAttrNoSelect) {
			continue
		}
		included := slices.Contains(roots, mailbox.Mailbox)
		if connectors.BoolDefault(cfg, "includeSubfolders", true) {
			included = included || slices.ContainsFunc(roots, func(root string) bool { return descendant(mailbox.Mailbox, root, mailbox.Delim) })
		}
		blocked := slices.ContainsFunc(excluded, func(root string) bool {
			return mailbox.Mailbox == root || descendant(mailbox.Mailbox, root, mailbox.Delim)
		})
		if included && !blocked {
			selected = append(selected, mailbox)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Mailbox < selected[j].Mailbox })
	return selected, nil
}

func (c *Connector) Browse(ctx context.Context, cred connectors.Credential, req connectors.BrowseRequest) (connectors.BrowseResult, error) {
	release, err := c.reserve(ctx, cred, 1)
	if err != nil {
		return connectors.BrowseResult{}, err
	}
	defer release()
	s, err := openSession(ctx, cred)
	if err != nil {
		return connectors.BrowseResult{}, redactError(err, cred)
	}
	defer s.close()
	list, err := mailboxes(s)
	if err != nil {
		return connectors.BrowseResult{}, redactError(err, cred)
	}
	return browseMailboxes(list, req.ParentID), nil
}

func browseMailboxes(list []*imap.ListData, parent string) connectors.BrowseResult {
	entries := map[string]connectors.BrowseItem{}
	for _, mailbox := range list {
		name := mailbox.Mailbox
		if parent != "" {
			if !descendant(name, parent, mailbox.Delim) {
				continue
			}
			name = strings.TrimPrefix(name, parent+string(mailbox.Delim))
		}
		leaf := name
		hasChildren := false
		if mailbox.Delim != 0 {
			if before, _, found := strings.Cut(name, string(mailbox.Delim)); found {
				leaf = before
				hasChildren = true
			}
		}
		id := leaf
		if parent != "" {
			id = parent + string(mailbox.Delim) + leaf
		}
		entry := entries[id]
		entry.ID, entry.Name, entry.Path, entry.Kind = id, leaf, id, connectors.KindFolder
		entry.HasChildren = entry.HasChildren || hasChildren
		if id == mailbox.Mailbox {
			entry.Selectable = !slices.Contains(mailbox.Attrs, imap.MailboxAttrNoSelect)
			for _, attr := range mailbox.Attrs {
				if slices.Contains([]imap.MailboxAttr{imap.MailboxAttrSent, imap.MailboxAttrDrafts, imap.MailboxAttrJunk, imap.MailboxAttrTrash}, attr) {
					entry.SpecialUse = append(entry.SpecialUse, string(attr))
				}
			}
		}
		entries[id] = entry
	}
	listing := connectors.BrowseResult{Items: []connectors.BrowseItem{}, ParentID: parent, Breadcrumbs: []connectors.Breadcrumb{{ID: "", Name: "Root"}}}
	if parent == "" {
		for _, mailbox := range list {
			if !slices.Contains(mailbox.Attrs, imap.MailboxAttrNoSelect) && (strings.EqualFold(mailbox.Mailbox, "INBOX") || slices.Contains(mailbox.Attrs, imap.MailboxAttrSent)) {
				listing.DefaultSelection = append(listing.DefaultSelection, mailbox.Mailbox)
			}
		}
		slices.Sort(listing.DefaultSelection)
	}
	if parent != "" {
		listing.Breadcrumbs = append(listing.Breadcrumbs, connectors.Breadcrumb{ID: parent, Name: parent})
	}
	for _, entry := range entries {
		listing.Items = append(listing.Items, entry)
	}
	sort.Slice(listing.Items, func(i, j int) bool { return listing.Items[i].ID < listing.Items[j].ID })
	return listing
}
