package disk

import (
	"errors"
	"path/filepath"
	"sync"
)

// Store is an in-memory cache of opened Logs keyed by their canonical
// absolute directory path. It exists to deduplicate parent references
// when many forks share a common ancestor: opening a child fork via
// the store reuses the already-open parent instead of re-reading its
// segments into memory.
//
// A Store is owned by the caller and has an explicit lifetime; Close
// shuts down everything it holds. Plain log.Open still works for
// callers that do not need sharing.
type Store struct {
	mu     sync.Mutex
	logs   map[string]*Log
	closed bool
}

func NewStore() *Store {
	return &Store{logs: make(map[string]*Log)}
}

// Open returns the cached Log for dir if present, otherwise opens it
// (resolving its parent transitively through the same Store) and
// caches the result. Concurrent callers receive the same *Log.
func (s *Store) Open(dir string, opts Options) (*Log, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	abs = filepath.Clean(abs)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("log store is closed")
	}
	if l, ok := s.logs[abs]; ok {
		s.mu.Unlock()
		return l, nil
	}
	s.mu.Unlock()

	// Resolve parent through the store if the dir is a fork and no
	// explicit Parent was injected.
	if opts.Parent == nil {
		base, err := readForkMarker(abs)
		if err != nil {
			return nil, err
		}
		if base > 0 {
			parentDir := filepath.Dir(abs)
			parentOpts := opts
			parentOpts.Parent = nil
			p, err := s.Open(parentDir, parentOpts)
			if err != nil {
				return nil, err
			}
			opts.Parent = p
		}
	}

	l, err := Open(abs, opts)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	// Re-check under the lock in case a concurrent Open landed first.
	if existing, ok := s.logs[abs]; ok {
		s.mu.Unlock()
		// Close our duplicate. The shared instance wins.
		_ = l.Close()
		return existing, nil
	}
	s.logs[abs] = l
	s.mu.Unlock()
	return l, nil
}

// Close closes every cached log and marks the Store unusable. Logs
// returned from this Store should not be closed individually; the
// Store owns their lifetime.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var errs []error
	for _, l := range s.logs {
		if err := l.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	s.logs = nil
	return errors.Join(errs...)
}
