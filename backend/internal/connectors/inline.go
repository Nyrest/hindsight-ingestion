package connectors

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

type AttachmentSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type ContentBlock struct {
	Type   string            `json:"type"`
	Text   string            `json:"text,omitempty"`
	Source *AttachmentSource `json:"source,omitempty"`
}

func TextBlock(text string) ContentBlock { return ContentBlock{Type: "text", Text: text} }

func (r ContentRequest) IncludesImages() bool {
	return r.InlineMultimodalEnabled && r.FilePolicy.Images
}

// FetchImage returns raw bytes; authentication is supplied only for source-owned assets.
func FetchImage(ctx context.Context, rawURL string, credential *Credential) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("image URL must be an http(s) URL without user information")
	}
	cl := httpx.New(nil, nil)
	if credential != nil {
		cl.CustomHeaders = credential.Headers
		cl.AuthHeaders = map[string]string{"Authorization": "Token " + credential.String("token")}
	}
	cl.HTTP = &http.Client{Transport: httpx.Shared, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many image redirects")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("unsupported image redirect")
		}
		// Custom headers can also hold secrets. Rebuild headers on every redirected request.
		req.Header = make(http.Header)
		if credential != nil && req.URL.Scheme == u.Scheme && req.URL.Host == u.Host {
			cl.ApplyHeaders(req)
		}
		return nil
	}}
	return cl.Do(ctx, httpx.Request{Method: http.MethodGet, URL: rawURL})
}

type ImageFetcher func(context.Context, string) (*http.Response, error)

type inlineImageResolver struct {
	fetch                    ImageFetcher
	cache                    map[string]ContentBlock
	limit, totalLimit, total int64
	count                    int
}

func (r *inlineImageResolver) takeImage(ctx context.Context, ref imageReference) (ContentBlock, error) {
	if r.count >= 50 || r.total >= r.totalLimit {
		return ContentBlock{}, fmt.Errorf("document image count or total size limit exceeded")
	}
	block, cached := r.cache[ref.url]
	var err error
	if !cached {
		block, err = inlineImage(ctx, ref, min(r.limit, r.totalLimit-r.total), r.fetch)
	}
	if err != nil {
		return ContentBlock{}, err
	}
	size := imageByteSize(block)
	if r.total+size > r.totalLimit {
		return ContentBlock{}, fmt.Errorf("document image total size limit exceeded")
	}
	r.cache[ref.url] = block
	r.total += size
	r.count++
	return block, nil
}

func InlineImages(ctx context.Context, markdown string, maxSize int64, fetch ImageFetcher) (SourceContent, error) {
	content := SourceContent{Text: markdown}
	refs := markdownImages([]byte(markdown))
	limit := int64(20 << 20)
	totalLimit := int64(100 << 20)
	if maxSize > 0 {
		limit = min(limit, maxSize)
		totalLimit = min(totalLimit, maxSize)
	}
	resolver := inlineImageResolver{fetch: fetch, cache: map[string]ContentBlock{}, limit: limit, totalLimit: totalLimit}
	position := 0
	for i, ref := range refs {
		if err := ctx.Err(); err != nil {
			return SourceContent{}, err
		}
		block, err := resolver.takeImage(ctx, ref)
		if err != nil {
			if ctx.Err() != nil {
				return SourceContent{}, ctx.Err()
			}
			content.Warnings = append(content.Warnings, fmt.Sprintf("Image %d: %s", i+1, imageError(err)))
			continue
		}
		if text := markdown[position:ref.start]; text != "" {
			content.Blocks = append(content.Blocks, TextBlock(text))
		}
		if ref.caption != "" {
			content.Blocks = append(content.Blocks, TextBlock("\n"+ref.caption+"\n"))
		}
		content.Blocks = append(content.Blocks, block)
		position = ref.end
	}
	if len(content.Blocks) > 0 && position < len(markdown) {
		content.Blocks = append(content.Blocks, TextBlock(markdown[position:]))
	}
	return content, nil
}

func inlineImage(ctx context.Context, ref imageReference, limit int64, fetch ImageFetcher) (ContentBlock, error) {
	resp, err := fetch(ctx, ref.url)
	if err != nil {
		return ContentBlock{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ContentBlock{}, fmt.Errorf("image download returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return ContentBlock{}, err
	}
	if int64(len(data)) > limit {
		return ContentBlock{}, fmt.Errorf("image exceeds the maximum size")
	}
	mediaType := http.DetectContentType(data)
	switch mediaType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return ContentBlock{}, fmt.Errorf("unsupported image format %s", mediaType)
	}
	block := ContentBlock{Type: "image", Source: &AttachmentSource{Type: "base64", MediaType: mediaType, Data: base64.StdEncoding.EncodeToString(data)}}
	return block, nil
}

func imageByteSize(block ContentBlock) int64 {
	data := block.Source.Data
	return int64(base64.StdEncoding.DecodedLen(len(data)) - (len(data) - len(strings.TrimRight(data, "="))))
}

func imageError(err error) string {
	// net/http errors may contain the complete signed URL or an upstream response body.
	var downloadError *url.Error
	if errors.As(err, &downloadError) {
		return "image download failed"
	}
	var status *httpx.HTTPError
	if errors.As(err, &status) {
		return fmt.Sprintf("image download returned HTTP %d", status.Status)
	}
	return strings.TrimSpace(err.Error())
}
