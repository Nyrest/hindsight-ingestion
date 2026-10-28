package sync

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
	"github.com/Nyrest/hindsight-ingestion/internal/postprocess"
	"github.com/Nyrest/hindsight-ingestion/internal/proxy"
)

// postProcessor runs a task's post-processing script. Its VM is acquired on
// the first document that needs processing and reused for the whole run.
type postProcessor struct {
	key, script       string
	http              *http.Client
	vm                *postprocess.VM
	binaryPassThrough bool
}

func newPostProcessor(taskID, raw string, proxyConfig proxy.Config) *postProcessor {
	cfg := postprocess.Parse(raw)
	script := cfg.Script()
	binaryPassThrough := cfg.Mode == postprocess.ModeUI
	active := false
	for _, step := range cfg.Steps {
		if !step.Enabled {
			continue
		}
		active = true
		if step.Type == postprocess.StepHTTP || step.Type == postprocess.StepSkip {
			binaryPassThrough = false
		}
	}
	if cfg.Mode == postprocess.ModeUI && !active {
		return nil
	}
	if script == "" {
		return nil
	}
	return &postProcessor{
		key: postprocess.Key(taskID, script), script: script, binaryPassThrough: binaryPassThrough,
		http: &http.Client{Transport: httpx.Transport(proxyConfig)},
	}
}

func (p *postProcessor) release() {
	if p != nil && p.vm != nil {
		postprocess.Shared.Release(p.vm)
		p.vm = nil
	}
}

// Inline media blocks are represented in the script input by numbered
// object-replacement markers so documents can be processed as a whole.
const mediaMarker = "￼"

var mediaMarkerRe = regexp.MustCompile(`\x{FFFC}(\d+)\x{FFFC}`)

func flattenBlocks(blocks []connectors.ContentBlock) (string, []connectors.ContentBlock) {
	var b strings.Builder
	var media []connectors.ContentBlock
	for _, block := range blocks {
		if block.Type == "text" {
			b.WriteString(block.Text)
			continue
		}
		b.WriteString(mediaMarker + strconv.Itoa(len(media)) + mediaMarker)
		media = append(media, block)
	}
	return b.String(), media
}

// restoreBlocks rebuilds blocks from processed text; media whose marker was
// removed are dropped.
func restoreBlocks(text string, media []connectors.ContentBlock) (string, []connectors.ContentBlock) {
	var blocks []connectors.ContentBlock
	var plain strings.Builder
	position, kept := 0, 0
	for _, m := range mediaMarkerRe.FindAllStringSubmatchIndex(text, -1) {
		i, err := strconv.Atoi(text[m[2]:m[3]])
		if err != nil || i >= len(media) {
			continue
		}
		if segment := text[position:m[0]]; segment != "" {
			blocks = append(blocks, connectors.TextBlock(segment))
			plain.WriteString(segment)
		}
		blocks = append(blocks, media[i])
		position = m[1]
		kept++
	}
	if segment := text[position:]; segment != "" {
		blocks = append(blocks, connectors.TextBlock(segment))
		plain.WriteString(segment)
	}
	if kept == 0 {
		return plain.String(), nil
	}
	return plain.String(), blocks
}

// postProcess transforms opened content with the task's script. A script
// returning no content skips the item.
func (r *runner) postProcess(item connectors.SourceItem, content connectors.SourceContent) (connectors.SourceContent, error) {
	if r.post == nil || (content.Body != nil && r.post.binaryPassThrough) {
		return content, nil
	}
	doc := postprocess.Document{Text: content.Text, FileName: orEmpty(content.FileName, item.Name),
		MIMEType: orEmpty(content.MIMEType, item.MIMEType), Context: content.Context}
	var media []connectors.ContentBlock
	switch {
	case content.Body != nil:
		limit := int64(postprocess.MaxDocumentBytes)
		if r.maxSz > 0 {
			limit = min(limit, r.maxSz)
		}
		if content.Size > limit {
			_ = content.Body.Close()
			content.Body = nil
			return content, fmt.Errorf("post-processing input exceeds %d MiB", limit>>20)
		}
		data, err := io.ReadAll(io.LimitReader(content.Body, limit+1))
		_ = content.Body.Close()
		content.Body = nil
		if err != nil {
			return content, err
		}
		if int64(len(data)) > limit {
			return content, fmt.Errorf("post-processing input exceeds %d MiB", limit>>20)
		}
		doc.Binary, doc.Data = true, data

	case len(content.Blocks) > 0:
		size := 0
		for _, block := range content.Blocks {
			size += len(block.Text)
			if block.Source != nil {
				size += len(block.Source.Data)
			}
			if size > postprocess.MaxDocumentBytes {
				return content, fmt.Errorf("post-processing input exceeds %d MiB", postprocess.MaxDocumentBytes>>20)
			}
		}
		doc.Text, media = flattenBlocks(content.Blocks)
	}
	if r.post.vm != nil && r.post.vm.Expired() {
		r.post.vm = nil
	}
	if r.post.vm == nil {
		vm, err := postprocess.Shared.Acquire(r.post.key, r.post.script)
		if err != nil {
			return content, fmt.Errorf("post-processing: %w", err)
		}
		r.post.vm = vm
	}
	res, err := r.post.vm.Process(r.ctx, r.postEnv(item), doc)
	if r.post.vm.Broken() {
		r.post.vm = nil
	}
	if err != nil {
		if r.ctx.Err() != nil {
			return content, r.ctx.Err()
		}
		return content, fmt.Errorf("post-processing: %w", err)
	}
	if res.Skip {
		return content, connectors.Skip("post-processing returned no content")
	}
	out := content
	out.Body, out.Blocks, out.Text, out.Size = nil, nil, "", 0
	if res.Context != "" {
		out.Context = res.Context
	}
	if res.FileName != "" {
		out.FileName = res.FileName
	}
	if res.MIMEType != "" {
		out.MIMEType = res.MIMEType
	}
	switch {
	case res.Binary:
		if r.maxSz > 0 && int64(len(res.Data)) > r.maxSz {
			return content, fmt.Errorf("post-processing: file exceeds the configured maximum size")
		}
		if out.FileName == "" {
			out.FileName = item.Name
		}
		mimeType := orEmpty(out.MIMEType, item.MIMEType)
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		// Attachment blocks use the recoverable, idempotent retain path.
		var encoded strings.Builder
		encoded.Grow(base64.StdEncoding.EncodedLen(len(res.Data)))
		encoder := base64.NewEncoder(base64.StdEncoding, &encoded)
		_, _ = encoder.Write(res.Data)
		_ = encoder.Close()
		block := connectors.ContentBlock{Type: "file", FileName: out.FileName,
			Source: &connectors.AttachmentSource{Type: "base64", MediaType: mimeType, Data: encoded.String()}}
		if strings.HasPrefix(mimeType, "image/") {
			block.Type, block.FileName = "image", ""
		}
		out.Blocks, out.Size = []connectors.ContentBlock{block}, int64(len(res.Data))
	case media != nil:
		out.Text, out.Blocks = restoreBlocks(res.Text, media)
	default:
		out.Text = res.Text
	}
	return out, nil
}

func (r *runner) postEnv(item connectors.SourceItem) postprocess.Env {
	run := map[string]any{"dryRun": r.run == nil, "full": r.full, "generation": r.gen}
	if r.run != nil {
		run["id"], run["mode"] = r.run.ID, r.run.SyncMode
	}
	modified := ""
	if !item.ModifiedAt.IsZero() {
		modified = item.ModifiedAt.UTC().Format(time.RFC3339Nano)
	}
	env := postprocess.Env{HTTP: r.post.http, Context: map[string]any{
		"task": map[string]any{"id": r.task.ID, "name": r.task.Name, "sourceType": r.task.SourceType, "bankId": r.task.DestinationBankID},
		"run":  run,
		"item": map[string]any{
			"id": item.ID, "name": item.Name, "path": item.Path, "kind": item.Kind, "revision": item.Revision,
			"modifiedAt": modified, "mimeType": item.MIMEType, "size": item.Size,
			"tags": plainJSON(item.Tags, []any{}), "metadata": plainJSON(item.Metadata, map[string]any{}),
			"attributes": plainJSON(attributes(item), map[string]any{}),
		},
	}}
	if r.run != nil {
		name := displayName(item)
		env.Log = func(level, msg string) { r.logf(level, "Post-processing %s: %s", name, msg) }
	}
	return env
}

// plainJSON converts v to plain JSON values so scripts see strings for times.
func plainJSON[T any](v any, empty T) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return empty
	}
	var out any
	if json.Unmarshal(raw, &out) != nil || out == nil {
		return empty
	}
	return out
}
