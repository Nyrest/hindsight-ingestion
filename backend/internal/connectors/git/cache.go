package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/go-git/go-billy/v5/osfs"
	gogit "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

func taskKey(taskID string) string {
	sum := sha256.Sum256([]byte(taskID))
	return hex.EncodeToString(sum[:])
}

func (s *Source) taskPath(taskID string) (string, error) {
	root, err := s.cacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, taskKey(taskID)), nil
}
func (s *Source) lock(ctx context.Context, taskID string) (func(), error) {
	stored, _ := s.locks.LoadOrStore(taskKey(taskID), make(chan struct{}, 1))
	lock := stored.(chan struct{})
	select {
	case lock <- struct{}{}:
		return func() { <-lock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// removeCache only removes immediate children owned by this connector.
func (s *Source) removeCache(dir string) error {
	root, err := s.cacheRoot()
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, dir)
	if err != nil || relative == "." || filepath.Base(relative) != relative || len(relative) != 64 {
		return errors.New("invalid Git cache cleanup path")
	}
	if decoded, err := hex.DecodeString(relative); err != nil || len(decoded) != 32 {
		return errors.New("invalid Git cache key")
	}
	return os.RemoveAll(dir)
}
func (s *Source) CleanupTask(ctx context.Context, taskID string) error {
	unlock, err := s.lock(ctx, taskID)
	if err != nil {
		return err
	}
	defer unlock()
	dir, err := s.taskPath(taskID)
	if err != nil {
		return err
	}
	return s.removeCache(dir)
}

// RecoverCaches removes interrupted temporary runs and caches of deleted tasks.
func (s *Source) RecoverCaches(ctx context.Context, taskIDs []string) error {
	root, err := s.cacheRoot()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	live := map[string]bool{}
	for _, id := range taskIDs {
		live[taskKey(id)] = true
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.recoverCacheEntry(root, entry, live); err != nil {
			return err
		}
	}
	return nil
}

func (s *Source) recoverCacheEntry(root string, entry fs.DirEntry, live map[string]bool) error {
	if !entry.IsDir() || len(entry.Name()) != 64 {
		return nil
	}
	if _, err := hex.DecodeString(entry.Name()); err != nil {
		return nil
	}
	dir := filepath.Join(root, entry.Name())
	_, markerErr := os.Stat(filepath.Join(dir, "ephemeral"))
	if live[entry.Name()] && markerErr != nil {
		return nil
	}
	return s.removeCache(dir)
}

type repositoryCache struct {
	repo    *gogit.Repository
	storage *filesystem.Storage
	dir     string
	budget  *diskBudget
}

func (c *repositoryCache) Close() error { return errors.Join(c.storage.Close(), c.budget.closeFiles()) }

func cacheBytes(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) && path == dir {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("unexpected symlink in Git cache")
		}
		if !entry.IsDir() {
			stat, err := entry.Info()
			if err != nil {
				return err
			}
			total += stat.Size()
		}
		return nil
	})
	return total, err
}

func openRepository(dir string, cfg sourceConfig) (*repositoryCache, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	size, err := cacheBytes(dir)
	if err != nil {
		return nil, err
	}
	if size >= cfg.CacheLimit {
		return nil, errCacheLimit
	}
	budget := &diskBudget{used: size, limit: cfg.CacheLimit}
	store := filesystem.NewStorageWithOptions(&quotaFS{Filesystem: osfs.New(dir), budget: budget}, cache.NewObjectLRU(8<<20), filesystem.Options{LargeObjectThreshold: 1 << 20})
	repo, err := gogit.Open(store, nil)
	if errors.Is(err, gogit.ErrRepositoryNotExists) {
		repo, err = gogit.Init(store, nil)
		if err == nil {
			_, err = repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{cfg.URL}})
		}
	}
	if err != nil {
		_ = store.Close()
		_ = budget.closeFiles()
		return nil, err
	}
	remote, err := repo.Remote("origin")
	if err != nil || len(remote.Config().URLs) != 1 || remote.Config().URLs[0] != cfg.URL {
		_ = store.Close()
		_ = budget.closeFiles()
		return nil, errors.New("Git cache repository changed")
	}
	return &repositoryCache{repo: repo, storage: store, dir: dir, budget: budget}, nil
}
