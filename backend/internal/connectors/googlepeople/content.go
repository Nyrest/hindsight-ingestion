package googlepeople

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func normalize(p person, id string) (contactSnapshot, error) {
	snapshot := contactSnapshot{ID: id, Deleted: p.Metadata.Deleted}
	if snapshot.Deleted {
		return snapshot, nil
	}
	snapshot.Names = fieldValues(p.Names, contactName)
	snapshot.Emails = fieldValues(p.EmailAddresses, fieldValue)
	snapshot.Phones = fieldValues(p.PhoneNumbers, fieldValue)
	snapshot.Organizations = fieldValues(p.Organizations, func(f personField) string { return f.Name })
	snapshot.Name = displayName(snapshot.Names, snapshot.Emails, snapshot.Phones)
	for _, source := range p.Metadata.Sources {
		if source.Type == "CONTACT" && source.UpdateTime.After(snapshot.ModifiedAt) {
			snapshot.ModifiedAt = source.UpdateTime
		}
	}
	text, err := render(p, snapshot.Name)
	if err != nil {
		return snapshot, err
	}
	snapshot.Text = text
	return snapshot, nil
}

func displayName(groups ...[]string) string {
	for _, candidates := range groups {
		if name := firstNonempty(candidates...); name != "" {
			return name
		}
	}
	return "Unnamed contact"
}

func contactName(f personField) string {
	return firstNonempty(f.DisplayName, strings.TrimSpace(f.GivenName+" "+f.FamilyName))
}

func fieldValue(f personField) string { return f.Value }

func firstNonempty(candidates ...string) string {
	for _, candidate := range candidates {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func fieldValues(fields []personField, format func(personField) string) []string {
	ordered := append([]personField(nil), fields...)
	slices.SortFunc(ordered, func(a, b personField) int {
		if a.Metadata.Primary != b.Metadata.Primary {
			if a.Metadata.Primary {
				return -1
			}
			return 1
		}
		return strings.Compare(format(a), format(b))
	})
	values := []string{}
	for _, field := range ordered {
		if formatted := strings.TrimSpace(format(field)); formatted != "" && !slices.Contains(values, formatted) {
			values = append(values, formatted)
		}
	}
	return values
}

func labeledField(f personField) string {
	label := strings.TrimSpace(f.Type)
	if f.Metadata.Primary {
		label = strings.TrimSpace(label + " primary")
	}
	if label == "" || strings.TrimSpace(f.Value) == "" {
		return f.Value
	}
	return f.Value + " (" + label + ")"
}

func organization(f personField) string {
	parts := []string{}
	for _, part := range []string{f.Name, f.Title, f.Department, f.JobDescription} {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	f.Value = strings.Join(parts, " — ")
	return labeledField(f)
}

func address(f personField) string {
	parts := []string{}
	for _, part := range []string{f.StreetAddress, f.City, f.Region, f.PostalCode, f.Country} {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	f.Value = firstNonempty(f.FormattedValue, strings.Join(parts, ", "))
	return labeledField(f)
}

func dateField(f personField) string {
	if f.Date.Month != 0 || f.Date.Day != 0 || f.Date.Year != 0 {
		f.Value = fmt.Sprintf("%04d-%02d-%02d", f.Date.Year, f.Date.Month, f.Date.Day)
		if f.Date.Year == 0 {
			f.Value = fmt.Sprintf("--%02d-%02d", f.Date.Month, f.Date.Day)
		}
	} else {
		f.Value = f.Text
	}
	return labeledField(f)
}

func render(p person, name string) (string, error) {
	var markdown strings.Builder
	markdown.WriteString("# " + strings.ReplaceAll(name, "\n", " ") + "\n")
	sections := []struct {
		name   string
		fields []personField
		format func(personField) string
	}{
		{"Names", p.Names, func(f personField) string { f.Value = contactName(f); return labeledField(f) }},
		{"Nicknames", p.Nicknames, labeledField}, {"Email addresses", p.EmailAddresses, labeledField},
		{"Phone numbers", p.PhoneNumbers, labeledField}, {"Organizations", p.Organizations, organization},
		{"Addresses", p.Addresses, address}, {"Birthdays", p.Birthdays, dateField}, {"Events", p.Events, dateField},
		{"Relations", p.Relations, func(f personField) string { f.Value = f.Person; return labeledField(f) }},
		{"Websites", p.URLs, labeledField},
	}
	for _, section := range sections {
		writeSection(&markdown, section.name, fieldValues(section.fields, section.format))
	}
	groups := []string{}
	for _, membership := range p.Memberships {
		if group := membership.ContactGroupMembership.ResourceName; group != "" {
			groups = append(groups, group)
		}
	}
	slices.Sort(groups)
	writeSection(&markdown, "Contact group IDs", slices.Compact(groups))
	notes := []string{}
	for _, biography := range p.Biographies {
		note := biography.Value
		if biography.ContentType == "TEXT_HTML" {
			converted, err := htmltomarkdown.ConvertString(note)
			if err != nil {
				return "", fmt.Errorf("convert contact notes: %w", err)
			}
			note = converted
		}
		if note = strings.TrimSpace(note); note != "" {
			notes = append(notes, note)
		}
	}
	slices.Sort(notes)
	if len(notes) > 0 {
		markdown.WriteString("\n## Notes\n\n" + strings.Join(slices.Compact(notes), "\n\n") + "\n")
	}
	return markdown.String(), nil
}

func writeSection(markdown *strings.Builder, heading string, values []string) {
	if len(values) == 0 {
		return
	}
	markdown.WriteString("\n## " + heading + "\n")
	for _, value := range values {
		markdown.WriteString("- " + strings.ReplaceAll(value, "\n", "\n  ") + "\n")
	}
}

func (snapshot contactSnapshot) sourceItem() connectors.SourceItem {
	digest := sha256.Sum256([]byte("google-people-markdown-v1\x00" + snapshot.Text))
	payload, _ := json.Marshal(snapshot.Text)
	return connectors.SourceItem{
		ID: snapshot.ID, Name: snapshot.Name, Path: snapshot.Name, Kind: connectors.KindDocument,
		Revision: hex.EncodeToString(digest[:]), ModifiedAt: snapshot.ModifiedAt,
		MIMEType: "text/markdown", Size: int64(len(snapshot.Text)), Deleted: snapshot.Deleted, Opaque: payload,
		Attributes: map[string]any{"name": optionalFilterValues(snapshot.Names), "email": optionalFilterValues(snapshot.Emails),
			"phone": optionalFilterValues(snapshot.Phones), "organization": optionalFilterValues(snapshot.Organizations), "modifiedAt": snapshot.ModifiedAt},
	}
}

func optionalFilterValues(values []string) any {
	if len(values) == 0 {
		// A slice inside an interface is non-nil; filters require nil for absence.
		return nil
	}
	return values
}

func (*Connector) OpenContent(_ context.Context, req connectors.ContentRequest) (connectors.SourceContent, error) {
	var text string
	if err := json.Unmarshal(req.Item.Opaque, &text); err != nil {
		return connectors.SourceContent{}, err
	}
	return connectors.SourceContent{Text: text, TimestampUnset: true, Context: "Google Contacts: " + req.Item.Name}, nil
}
