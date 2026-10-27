package notion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

type pageVisit struct {
	item  connectors.SourceItem
	depth int
}

func (c *Connector) scanHierarchy(ctx context.Context, req connectors.ScanRequest, cfg config) (connectors.ScanResult, error) {
	cl := client(req.Credential)
	roots, err := c.hierarchyRoots(ctx, req, cfg, cl)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	queue := make([]pageVisit, 0, len(roots))
	seen := map[string]bool{}
	for _, root := range roots {
		if !seen[pageKey(root.ID)] {
			queue = append(queue, pageVisit{item: root})
			seen[pageKey(root.ID)] = true
		}
	}
	// All roots enter the queue first so overlapping selections use the shortest
	// page depth, independently of the order in which roots were selected.
	for index := 0; index < len(queue); index++ {
		visit := queue[index]
		if err := req.Emit(visit.item); err != nil {
			return connectors.ScanResult{}, err
		}
		if visit.item.Deleted || !cfg.includeSubpages || (cfg.maxDepth >= 0 && visit.depth >= cfg.maxDepth) {
			continue
		}
		children, err := childPageIDs(ctx, cl, apiBase(req.Credential), visit.item.ID)
		if err != nil {
			return connectors.ScanResult{}, err
		}
		for _, childID := range children {
			if seen[pageKey(childID)] {
				continue
			}
			child, err := retrievePage(ctx, cl, apiBase(req.Credential), childID)
			if err != nil {
				return connectors.ScanResult{}, err
			}
			if seen[pageKey(child.ID)] {
				continue
			}
			queue = append(queue, pageVisit{item: childItem(child, visit.item), depth: visit.depth + 1})
			seen[pageKey(child.ID)] = true
		}
	}
	cur, _ := json.Marshal(cursorState{HighWater: time.Now().UTC()})
	return connectors.ScanResult{Cursor: cur, Complete: true}, nil
}

func (c *Connector) hierarchyRoots(ctx context.Context, req connectors.ScanRequest, cfg config, cl *httpx.Client) ([]connectors.SourceItem, error) {
	var roots []connectors.SourceItem
	if cfg.mode == "database" {
		inventory := req
		inventory.Full, inventory.Cursor = true, nil
		inventory.Emit = func(root connectors.SourceItem) error {
			roots = append(roots, root)
			return nil
		}
		_, err := c.scanDatabase(ctx, inventory)
		return roots, err
	}
	for _, id := range cfg.pageIDs {
		root, err := retrievePage(ctx, cl, apiBase(req.Credential), id)
		if err != nil {
			return nil, err
		}
		rootItem := toItem(root, "", pageTitle(root))
		rootItem.Path = rootItem.Name
		rootItem.Metadata["notion_root_page_id"] = root.ID
		rootItem.Tags = []string{"notion_root_page_id:" + root.ID}
		var op opaque
		_ = json.Unmarshal(rootItem.Opaque, &op)
		op.RootPageID = root.ID
		rootItem.Opaque, _ = json.Marshal(op)
		roots = append(roots, rootItem)
	}
	return roots, nil
}

func retrievePage(ctx context.Context, cl *httpx.Client, base, id string) (page, error) {
	var p page
	if err := cl.JSON(ctx, http.MethodGet, base+"/pages/"+url.PathEscape(id), nil, &p); err != nil {
		return p, fmt.Errorf("retrieve page %s: %w", id, err)
	}
	if p.Object != "page" || p.ID == "" || p.LastEditedTime.IsZero() {
		return p, fmt.Errorf("notion returned incomplete metadata for page %s", id)
	}
	return p, nil
}

func childItem(p page, parent connectors.SourceItem) connectors.SourceItem {
	var parentOp opaque
	_ = json.Unmarshal(parent.Opaque, &parentOp)
	child := toItem(p, parent.Metadata["notion_data_source_id"], parentOp.Source)
	child.Path = parent.Path + " / " + child.Name
	if parentOp.RootPageID != "" {
		child.Metadata["notion_root_page_id"] = parentOp.RootPageID
		child.Tags = []string{"notion_root_page_id:" + parentOp.RootPageID}
		var op opaque
		_ = json.Unmarshal(child.Opaque, &op)
		op.RootPageID = parentOp.RootPageID
		child.Opaque, _ = json.Marshal(op)
	}
	return child
}

type block struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	HasChildren bool   `json:"has_children"`
	InTrash     bool   `json:"in_trash"`
	Archived    bool   `json:"archived"`
}

func childPageIDs(ctx context.Context, cl *httpx.Client, base, pageID string) ([]string, error) {
	var children []string
	containers := []string{pageID}
	seen := map[string]bool{pageKey(pageID): true}
	for index := 0; index < len(containers); index++ {
		blocks, err := listBlocks(ctx, cl, base, containers[index])
		if err != nil {
			return nil, err
		}
		for _, b := range blocks {
			if b.InTrash || b.Archived {
				continue
			}
			switch b.Type {
			case "child_page":
				children = append(children, b.ID)
			case "child_database", "link_to_page":
				continue
			default:
				if b.HasChildren && !seen[pageKey(b.ID)] {
					containers = append(containers, b.ID)
					seen[pageKey(b.ID)] = true
				}
			}
		}
	}
	return children, nil
}

func listBlocks(ctx context.Context, cl *httpx.Client, base, id string) ([]block, error) {
	var blocks []block
	cursor := ""
	seenCursors := map[string]bool{}
	for {
		query := url.Values{"page_size": {fmt.Sprint(pageSize)}}
		if cursor != "" {
			query.Set("start_cursor", cursor)
		}
		var out struct {
			Results    []block `json:"results"`
			HasMore    bool    `json:"has_more"`
			NextCursor string  `json:"next_cursor"`
		}
		if err := cl.JSON(ctx, http.MethodGet, base+"/blocks/"+url.PathEscape(id)+"/children?"+query.Encode(), nil, &out); err != nil {
			return nil, fmt.Errorf("list child blocks of %s: %w", id, err)
		}
		for _, b := range out.Results {
			if b.ID == "" || b.Type == "" {
				return nil, fmt.Errorf("notion returned incomplete child blocks for %s", id)
			}
		}
		blocks = append(blocks, out.Results...)
		if !out.HasMore {
			return blocks, nil
		}
		if out.NextCursor == "" || seenCursors[out.NextCursor] {
			return nil, errors.New("notion block listing returned an invalid pagination cursor")
		}
		seenCursors[out.NextCursor] = true
		cursor = out.NextCursor
	}
}
