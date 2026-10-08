package hindsight

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
)

type Tag struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

type TagPage struct {
	Items  []Tag `json:"items"`
	Total  int   `json:"total"`
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
}

func (c *Client) Tags(ctx context.Context, bankID, query string, limit, offset int) (TagPage, error) {
	page := TagPage{Items: []Tag{}}
	q := url.Values{"limit": {fmt.Sprint(limit)}, "offset": {fmt.Sprint(offset)}}
	if query != "" {
		q.Set("q", query)
	}
	err := c.http.JSON(ctx, http.MethodGet, c.bankURL(bankID, "tags")+"?"+q.Encode(), nil, &page)
	return page, err
}

func (c *Client) TaskDocuments(ctx context.Context, bankID, taskID string) ([]string, error) {
	tag := "ingestion_task:" + taskID
	ids := []string{}
	seen := map[string]bool{}
	for offset := 0; ; {
		var page struct {
			Items []struct {
				ID   string   `json:"id"`
				Tags []string `json:"tags"`
			} `json:"items"`
			Total int `json:"total"`
		}
		q := url.Values{"tags": {tag}, "tags_match": {"all_strict"}, "limit": {"100"}, "offset": {fmt.Sprint(offset)}}
		if err := c.http.JSON(ctx, http.MethodGet, c.bankURL(bankID, "documents")+"?"+q.Encode(), nil, &page); err != nil {
			return nil, err
		}
		for _, doc := range page.Items {
			// Verify ownership even if an older server ignores the filter.
			if slices.Contains(doc.Tags, tag) && doc.ID != "" && !seen[doc.ID] {
				ids = append(ids, doc.ID)
				seen[doc.ID] = true
			}
		}
		offset += len(page.Items)
		if len(page.Items) == 0 || offset >= page.Total {
			return ids, nil
		}
	}
}
