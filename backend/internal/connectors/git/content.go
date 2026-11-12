package git

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func (s *Source) OpenContent(ctx context.Context, req connectors.ContentRequest) (connectors.SourceContent, error) {
	if err := ctx.Err(); err != nil {
		return connectors.SourceContent{}, err
	}
	cfg, err := parseConfig(req.Config)
	if err != nil {
		return connectors.SourceContent{}, err
	}
	if err := contentLimit(cfg, req); err != nil {
		return connectors.SourceContent{}, err
	}
	blob, err := s.blobForItem(req.Item)
	if err != nil {
		return connectors.SourceContent{}, err
	}
	body, err := blobStream(ctx, blob)
	if err != nil {
		return connectors.SourceContent{}, err
	}
	content := connectors.SourceContent{Body: body, FileName: req.Item.Name, MIMEType: req.Item.MIMEType, Size: blob.Size, Context: req.Item.Path}
	if connectors.FileGroup(req.Item.Name, req.Item.MIMEType) == connectors.GroupPlainText {
		return plainContent(content)
	}
	return content, nil
}

func contentLimit(cfg sourceConfig, req connectors.ContentRequest) error {
	if cfg.FileLimit > 0 && req.Item.Size > cfg.FileLimit {
		return connectors.Skip("file exceeds the task's Git ingestion size limit")
	}
	if req.MaxFileSize > 0 && req.Item.Size > req.MaxFileSize {
		return connectors.Skip("file exceeds the global ingestion size limit")
	}
	return nil
}

func (s *Source) blobForItem(item connectors.SourceItem) (*object.Blob, error) {
	var location blobLocation
	if err := json.Unmarshal(item.Opaque, &location); err != nil {
		return nil, err
	}
	cached, ok := s.sessions.Load(location.Session)
	if !ok {
		return nil, errors.New("Git snapshot session has ended")
	}
	return cached.(*gogit.Repository).BlobObject(plumbing.NewHash(location.Blob))
}

func blobStream(ctx context.Context, blob *object.Blob) (io.ReadCloser, error) {
	reader, err := blob.Reader()
	if err != nil {
		return nil, err
	}
	buffered := bufio.NewReader(reader)
	prefix, peekErr := buffered.Peek(min(1024, int(blob.Size)))
	if peekErr != nil && !errors.Is(peekErr, io.EOF) {
		_ = reader.Close()
		return nil, peekErr
	}
	if blob.Size <= 1024 && isLFSPointer(prefix) {
		_ = reader.Close()
		return nil, connectors.Skip("Git LFS objects are not enabled")
	}
	return &blobBody{Reader: buffered, closer: reader, ctx: ctx}, nil
}

func plainContent(content connectors.SourceContent) (connectors.SourceContent, error) {
	body, err := io.ReadAll(content.Body)
	closeErr := content.Body.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return connectors.SourceContent{}, err
	}
	if !utf8.Valid(body) {
		return connectors.SourceContent{}, errors.New("Git text file is not valid UTF-8")
	}
	content.Body = nil
	content.Text = strings.TrimPrefix(string(body), "\ufeff")
	return content, nil
}

func isLFSPointer(prefix []byte) bool {
	const version = "version https://git-lfs.github.com/spec/v1"
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(prefix), "\r\n", "\n")), "\n")
	if len(lines) < 3 || lines[0] != version {
		return false
	}
	var oid, size bool
	for _, line := range lines[1:] {
		switch {
		case strings.HasPrefix(line, "oid sha256:"):
			decoded, err := hex.DecodeString(strings.TrimPrefix(line, "oid sha256:"))
			oid = err == nil && len(decoded) == 32
		case strings.HasPrefix(line, "size "):
			parsed, err := strconv.ParseInt(strings.TrimPrefix(line, "size "), 10, 64)
			size = err == nil && parsed >= 0
		case strings.HasPrefix(line, "ext-"):
		default:
			return false
		}
	}
	return oid && size
}

type blobBody struct {
	io.Reader
	closer io.Closer
	ctx    context.Context
}

func (b *blobBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	return b.Reader.Read(p)
}
func (b *blobBody) Close() error { return b.closer.Close() }
