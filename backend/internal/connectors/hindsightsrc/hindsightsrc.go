// Package hindsightsrc implements Hindsight as a source (Hindsight → Hindsight).
package hindsightsrc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/hindsight"
)

func init() {
	connectors.RegisterCredentialType(hindsight.CredentialSpec)
	connectors.RegisterSource(&Connector{})
}

// Connector reads documents from a source bank.
type Connector struct{}

// Info describes the connector.
func (c *Connector) Info() connectors.SourceInfo {
	return connectors.SourceInfo{
		Type:           "hindsight",
		Name:           "Hindsight",
		CredentialType: hindsight.CredentialType,
		Capabilities: connectors.Capabilities{
			IncrementalMode: connectors.IncrementalHighWater,
			DeletionMode:    connectors.DeletionFullReconcile,
		},
		BrowseKinds: []string{connectors.KindBank},
		Fields: []connectors.FieldSpec{
			{Key: "bankId", Label: "Source bank", Type: connectors.FieldString, Required: true, Browse: true,
				BrowseKind: connectors.KindBank, Effect: connectors.EffectRebaseline},
			{Key: "tags", Label: "Only documents with tags", Type: connectors.FieldString, Placeholder: "team:a, project:x",
				Help: "Comma-separated. Leave empty to copy every document.", Effect: connectors.EffectReconcile},
			{Key: "tagsMatch", Label: "Tag match", Type: connectors.FieldSelect, Default: "any_strict",
				Options: []connectors.Option{{Value: "any_strict", Label: "Any tag"}, {Value: "all_strict", Label: "All tags"}},
				Effect:  connectors.EffectReconcile},
			{Key: "skipIngested", Label: "Skip documents created by this service", Type: connectors.FieldBoolean, Default: true,
				Help: "Avoid re-copying documents that another ingestion task wrote into the source bank.", Effect: connectors.EffectReconcile},
		},
		FilterFields: []connectors.FilterFieldSpec{
			{Key: "name", Label: "Document ID", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "tags", Label: "Tags", Type: connectors.FilterString, Operators: []string{connectors.OpContains, connectors.OpEquals, connectors.OpNotEquals, connectors.OpIn, connectors.OpNotIn}},
			{Key: "modifiedAt", Label: "Updated", Type: connectors.FilterDatetime, Operators: connectors.DatetimeOps},
			{Key: "size", Label: "Text length", Type: connectors.FilterNumber, Operators: connectors.NumberOps},
		},
	}
}

// ValidateCredential checks connectivity.
func (c *Connector) ValidateCredential(ctx context.Context, cred connectors.Credential) (string, error) {
	cl, err := hindsight.NewClient(cred)
	if err != nil {
		return "", err
	}
	banks, err := cl.ListBanks(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Connected (%d banks visible)", len(banks)), nil
}

// ValidateConfig checks configuration.
func (c *Connector) ValidateConfig(cfg map[string]any, f connectors.Filter) error {
	if err := connectors.Require(cfg, "bankId"); err != nil {
		return err
	}
	return connectors.ValidateFilter(f, c.Info().FilterFields, false)
}

// Browse lists banks.
func (c *Connector) Browse(ctx context.Context, cred connectors.Credential, _ connectors.BrowseRequest) (connectors.BrowseResult, error) {
	cl, err := hindsight.NewClient(cred)
	if err != nil {
		return connectors.BrowseResult{}, err
	}
	banks, err := cl.ListBanks(ctx)
	if err != nil {
		return connectors.BrowseResult{}, err
	}
	res := connectors.BrowseResult{Breadcrumbs: []connectors.Breadcrumb{{Name: "Banks"}}}
	for _, b := range banks {
		res.Items = append(res.Items, connectors.BrowseItem{ID: b.BankID, Name: b.BankID, Kind: connectors.KindBank, Path: b.BankID, Selectable: true})
	}
	return res, nil
}

type cursorState struct {
	HighWater time.Time `json:"highWater"`
}

type opaque struct {
	BankID string `json:"bankId"`
}

const overlap = 2 * time.Minute

// Scan lists documents; incremental scans filter by updated_at.
func (c *Connector) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	cl, err := hindsight.NewClient(req.Credential)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	bankID := connectors.AsString(req.Config["bankId"])
	var prev cursorState
	if len(req.Cursor) > 0 && !req.Full {
		_ = json.Unmarshal(req.Cursor, &prev)
	}
	opts := hindsight.ListDocumentsOptions{}
	for _, t := range strings.Split(connectors.AsString(req.Config["tags"]), ",") {
		if t = strings.TrimSpace(t); t != "" {
			opts.Tags = append(opts.Tags, t)
		}
	}
	if len(opts.Tags) > 0 {
		opts.TagsMatch = connectors.AsString(req.Config["tagsMatch"])
		if opts.TagsMatch == "" {
			opts.TagsMatch = "any_strict"
		}
	}
	incremental := !req.Full && !prev.HighWater.IsZero()
	if incremental {
		start := prev.HighWater.Add(-overlap)
		opts.StartDate = &start
	}
	skipIngested := connectors.BoolDefault(req.Config, "skipIngested", true)
	scanStart := time.Now().UTC()
	high := prev.HighWater
	op, _ := json.Marshal(opaque{BankID: bankID})

	err = cl.ListDocuments(ctx, bankID, opts, func(docs []hindsight.Document) error {
		for _, d := range docs {
			updated, _ := time.Parse(time.RFC3339Nano, d.UpdatedAt)
			if updated.After(high) {
				high = updated
			}
			if skipIngested {
				if _, ok := d.Metadata["_ingestion_task_id"]; ok {
					continue
				}
			}
			rev := d.ContentHash
			if rev == "" {
				rev = d.UpdatedAt
			}
			md := map[string]string{"hindsight_source_bank_id": bankID, "hindsight_source_document_id": d.ID}
			for k, v := range d.Metadata {
				if s, ok := v.(string); ok && !strings.HasPrefix(k, "_ingestion_") {
					md[k] = s
				}
			}
			item := connectors.SourceItem{
				ID: d.ID, Name: d.ID, Path: bankID + "/" + d.ID, Kind: connectors.KindDocument,
				Revision: rev + "|" + d.UpdatedAt, ModifiedAt: updated, Size: int64(d.TextLength),
				Metadata:   md,
				Tags:       append([]string{"hindsight_bank_id:" + bankID}, d.Tags...),
				Attributes: map[string]any{"tags": d.Tags},
				Opaque:     op,
			}
			if err := req.Emit(item); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return connectors.ScanResult{}, err
	}
	if high.After(scanStart) {
		high = scanStart
	}
	cur, _ := json.Marshal(cursorState{HighWater: high})
	return connectors.ScanResult{Cursor: cur, Complete: !incremental}, nil
}

// OpenContent fetches the document's original text.
func (c *Connector) OpenContent(ctx context.Context, req connectors.ContentRequest) (connectors.SourceContent, error) {
	cl, err := hindsight.NewClient(req.Credential)
	if err != nil {
		return connectors.SourceContent{}, err
	}
	bankID := connectors.AsString(req.Config["bankId"])
	doc, err := cl.GetDocument(ctx, bankID, req.Item.ID)
	if err != nil {
		return connectors.SourceContent{}, err
	}
	if doc.OriginalText == nil || strings.TrimSpace(*doc.OriginalText) == "" {
		return connectors.SourceContent{}, connectors.Skip("document has no original text")
	}
	return connectors.SourceContent{
		Text:      *doc.OriginalText,
		Context:   fmt.Sprintf("Hindsight document %q from bank %q", doc.ID, bankID),
		Timestamp: req.Item.ModifiedAt,
	}, nil
}
