package disk

import (
	"errors"
	"path/filepath"
	"sync"
)

// Store deduplicates parent references when many forks share an ancestor:
// opening a child through it reuses the already-open parent.
type Store struct {
	mu     sync.Mutex
	logs   map[string]*Log
	closed bool
}

func NewStore() *Store {
	return &Store{logs: make(map[string]*Log)}
}

// Open resolves the parent transitively through the same Store; concurrent
// callers receive the same *Log.
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

	// Resolve the parent through the store unless one was injected.
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

// Close closes every cached log; the Store owns their lifetime.
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
