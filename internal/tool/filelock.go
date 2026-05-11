package tool

import (
	"path/filepath"
	"sync"
)

// fileMutex wraps a sync.Mutex with a refcount.
type fileMutex struct {
	mu       sync.Mutex
	refCount int
}

var (
	fileLocksMu sync.Mutex
	fileLocks   = make(map[string]*fileMutex)
)

// fileLockKey resolves a path to a canonical key.
func fileLockKey(path string) string {
	abs := path
	if !filepath.IsAbs(abs) {
		if a, err := filepath.Abs(abs); err == nil {
			abs = a
		}
	}
	return filepath.Clean(abs)
}

func acquireFileMutex(key string) *fileMutex {
	fileLocksMu.Lock()
	m, ok := fileLocks[key]
	if !ok {
		m = &fileMutex{}
		fileLocks[key] = m
	}
	m.refCount++
	fileLocksMu.Unlock()
	m.mu.Lock()
	return m
}

func releaseFileMutex(key string, m *fileMutex) {
	m.mu.Unlock()
	fileLocksMu.Lock()
	m.refCount--
	if m.refCount == 0 {
		delete(fileLocks, key)
	}
	fileLocksMu.Unlock()
}

// WithFileMutex serializes fn per absolute path.
func WithFileMutex(path string, fn func() error) error {
	key := fileLockKey(path)
	m := acquireFileMutex(key)
	defer releaseFileMutex(key, m)
	return fn()
}
