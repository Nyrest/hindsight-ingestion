package git

import (
	"errors"
	"os"
	"sync"

	"github.com/go-git/go-billy/v5"
)

var errCacheLimit = errors.New("Git cache size limit exceeded")

// Counting writes as well as existing bytes bounds transient pack and index files.
// Overwrites may exhaust the budget early; the next attempt uses a fresh cache.
type diskBudget struct {
	mu          sync.Mutex
	used, limit int64
	files       map[*quotaFile]struct{}
}

func (b *diskBudget) track(file billy.File) *quotaFile {
	wrapped := &quotaFile{File: file, budget: b}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.files == nil {
		b.files = map[*quotaFile]struct{}{}
	}
	b.files[wrapped] = struct{}{}
	return wrapped
}
func (b *diskBudget) closeFiles() error {
	b.mu.Lock()
	files := make([]*quotaFile, 0, len(b.files))
	for file := range b.files {
		files = append(files, file)
	}
	b.mu.Unlock()
	var closeErr error
	for _, file := range files {
		closeErr = errors.Join(closeErr, file.Close())
	}
	return closeErr
}

func (b *diskBudget) reserve(n int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n < 0 || n > b.limit-b.used {
		return errCacheLimit
	}
	b.used += n
	return nil
}

type quotaFS struct {
	billy.Filesystem
	budget *diskBudget
}

func (f *quotaFS) Create(name string) (billy.File, error) {
	return f.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
}
func (f *quotaFS) OpenFile(name string, flag int, perm os.FileMode) (billy.File, error) {
	file, err := f.Filesystem.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return f.budget.track(file), nil
}
func (f *quotaFS) TempFile(dir, prefix string) (billy.File, error) {
	file, err := f.Filesystem.TempFile(dir, prefix)
	if err != nil {
		return nil, err
	}
	return f.budget.track(file), nil
}
func (f *quotaFS) Open(name string) (billy.File, error) {
	file, err := f.Filesystem.Open(name)
	if err != nil {
		return nil, err
	}
	return f.budget.track(file), nil
}
func (f *quotaFS) Chroot(path string) (billy.Filesystem, error) {
	child, err := f.Filesystem.Chroot(path)
	if err != nil {
		return nil, err
	}
	return &quotaFS{Filesystem: child, budget: f.budget}, nil
}

type quotaFile struct {
	billy.File
	budget   *diskBudget
	once     sync.Once
	closeErr error
}

func (f *quotaFile) Close() error {
	f.once.Do(func() {
		f.closeErr = f.File.Close()
		f.budget.mu.Lock()
		delete(f.budget.files, f)
		f.budget.mu.Unlock()
	})
	return f.closeErr
}

func (f *quotaFile) Write(p []byte) (int, error) {
	if err := f.budget.reserve(int64(len(p))); err != nil {
		return 0, err
	}
	return f.File.Write(p)
}
func (f *quotaFile) Truncate(size int64) error {
	position, err := f.File.Seek(0, 1)
	if err != nil {
		return err
	}
	current, err := f.File.Seek(0, 2)
	if err != nil {
		return err
	}
	if _, err := f.File.Seek(position, 0); err != nil {
		return err
	}
	if size > current {
		if err := f.budget.reserve(size - current); err != nil {
			return err
		}
	}
	return f.File.Truncate(size)
}
