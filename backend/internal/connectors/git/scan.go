package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/uuid"
)

type snapshot struct {
	cache     *repositoryCache
	commit    *object.Commit
	ref       reference
	origin    string
	fileLimit int64
}
type blobLocation struct {
	Session string `json:"session"`
	Blob    string `json:"blob"`
	Path    string `json:"path"`
}

func (s *Source) prepareSnapshot(ctx context.Context, access *access, cfg sourceConfig, dir string) (snapshot, error) {
	ref, err := resolveRef(ctx, access, cfg)
	if err != nil {
		return snapshot{}, err
	}
	if !cfg.KeepCache {
		if err := s.removeCache(dir); err != nil {
			return snapshot{}, err
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		cache, err := openRepository(dir, cfg)
		if err == nil {
			prepared := snapshot{cache: cache, ref: ref, origin: cfg.URL, fileLimit: cfg.FileLimit}
			err = prepared.prepare(ctx, access, cfg)
			if err == nil {
				return prepared, nil
			}
			_ = cache.Close()
			if ctx.Err() != nil {
				return snapshot{}, ctx.Err()
			}
		}
		if err := s.removeCache(dir); err != nil {
			return snapshot{}, err
		}
		if attempt == 1 {
			return snapshot{}, err
		}
	}
	return snapshot{}, errors.New("Git snapshot could not be prepared")
}

func (s *snapshot) prepare(ctx context.Context, access *access, cfg sourceConfig) error {
	if !cfg.KeepCache {
		if err := os.WriteFile(filepath.Join(s.cache.dir, "ephemeral"), nil, 0600); err != nil {
			return err
		}
	}
	commit, err := fetchCommit(ctx, s.cache, access, s.ref)
	if err != nil {
		return err
	}
	if err := validateTree(ctx, commit); err != nil {
		return err
	}
	s.commit = commit
	return nil
}

func validateTree(ctx context.Context, commit *object.Commit) error {
	tree, err := commit.Tree()
	if err != nil {
		return err
	}
	files := tree.Files()
	defer files.Close()
	return files.ForEach(func(*object.File) error { return ctx.Err() })
}

func (s *Source) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	cfg, err := parseConfig(req.Config)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	access, err := newAccess(ctx, req.Credential, cfg.URL)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	unlock, err := s.lock(ctx, req.TaskID)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	defer unlock()
	dir, err := s.taskPath(req.TaskID)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	if !cfg.KeepCache {
		defer s.removeCache(dir)
	}
	snapshot, err := s.prepareSnapshot(ctx, access, cfg, dir)
	if err != nil {
		if emptyRemote(err, cfg) {
			return connectors.ScanResult{Complete: true, Cursor: json.RawMessage(`{"empty":true}`)}, nil
		}
		return connectors.ScanResult{}, err
	}
	defer snapshot.cache.Close()
	return s.scanSnapshot(ctx, req, snapshot)
}

func (s *Source) scanSnapshot(ctx context.Context, req connectors.ScanRequest, snapshot snapshot) (connectors.ScanResult, error) {
	session := uuid.NewString()
	s.sessions.Store(session, snapshot.cache.repo)
	defer s.sessions.Delete(session)
	if req.Log != nil {
		req.Log("info", fmt.Sprintf("Git snapshot %s (%s), shallow depth 1", snapshot.commit.Hash, snapshot.ref.name))
	}
	if err := s.scanTree(ctx, req, snapshot, session); err != nil {
		return connectors.ScanResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return connectors.ScanResult{}, err
	}
	cursor, _ := json.Marshal(map[string]string{"commit": snapshot.commit.Hash.String(), "ref": string(snapshot.ref.name)})
	return connectors.ScanResult{Complete: true, Cursor: cursor}, nil
}

func (s *Source) scanTree(ctx context.Context, req connectors.ScanRequest, snapshot snapshot, session string) error {
	tree, err := snapshot.commit.Tree()
	if err != nil {
		return err
	}
	iterator := tree.Files()
	defer iterator.Close()
	return iterator.ForEach(func(file *object.File) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if file.Mode != filemode.Regular && file.Mode != filemode.Executable && file.Mode != filemode.Deprecated {
			return nil
		}
		if snapshot.fileLimit > 0 && file.Size > snapshot.fileLimit {
			if req.Log != nil {
				req.Log("info", fmt.Sprintf("Skipped %s: exceeds the task's Git ingestion size limit", file.Name))
			}
			return nil
		}
		// Git paths stay byte-for-byte case-sensitive; fixed hashes also fit SQL keys.
		sum := sha256.Sum256([]byte(snapshot.origin + "\x00" + file.Name))
		opaque, _ := json.Marshal(blobLocation{Session: session, Blob: file.Hash.String(), Path: file.Name})
		item := connectors.SourceItem{ID: hex.EncodeToString(sum[:]), Name: path.Base(file.Name), Path: file.Name, Kind: connectors.KindFile, Revision: file.Hash.String(), Size: file.Size, MIMEType: mime.TypeByExtension(strings.ToLower(path.Ext(file.Name))), Opaque: opaque,
			Metadata: map[string]string{"git_repository": snapshot.origin, "git_path": file.Name, "git_blob": file.Hash.String()},
		}
		return req.Emit(item)
	})
}
