package log

import (
	"errors"
	"path/filepath"
	"sync"

	"github.com/jack-work/figaro/internal/store/disk"
)

// Store owns shared log handles for one physical directory tree. It wraps a
// disk.Store and creates exactly one immutable cache snapshot per physical
// log. Fork children point at the same parent snapshot rather than copying
// their inherited history.
type Store struct {
	mu      sync.Mutex
	disk    *disk.Store
	logs    map[string]*Log
	opening map[string]*storeOpen
	closed  bool
}

type storeOpen struct {
	ready chan struct{}
	log   *Log
	err   error
}

// NewStore creates an empty shared log store.
func NewStore() *Store {
	return &Store{
		disk:    disk.NewStore(),
		logs:    make(map[string]*Log),
		opening: make(map[string]*storeOpen),
	}
}

// Open returns the single shared cached Log for dir. Concurrent opens of the
// same log wait for one cache construction; sibling opens share the same
// parent snapshot pointer. An explicit opts.Parent is honoured only for the
// first open of dir; the cache keys on dir alone.
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
	if l := s.logs[abs]; l != nil {
		s.mu.Unlock()
		return l, nil
	}
	if pending := s.opening[abs]; pending != nil {
		s.mu.Unlock()
		<-pending.ready
		return pending.log, pending.err
	}
	pending := &storeOpen{ready: make(chan struct{})}
	s.opening[abs] = pending
	s.mu.Unlock()

	l, openErr := s.open(abs, opts)

	s.mu.Lock()
	if s.closed && openErr == nil {
		openErr = errors.New("log store is closed")
		l = nil
	}
	if openErr == nil {
		s.logs[abs] = l
	}
	pending.log, pending.err = l, openErr
	delete(s.opening, abs)
	close(pending.ready)
	s.mu.Unlock()
	return l, openErr
}

func (s *Store) open(abs string, opts Options) (*Log, error) {
	inner, err := s.disk.Open(abs, opts)
	if err != nil {
		return nil, err
	}
	return &Log{inner: inner, shared: true, maxLag: maxLagFor(opts)}, nil
}

// Evict syncs and drops the cached Log for dir, releasing the payload
// blocks its segments hold; the next Open rebuilds nothing but the index.
// The underlying disk log stays cached in the disk store for cheap reload.
// On sync failure the log stays cached, so no pending entry is orphaned.
func (s *Store) Evict(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	abs = filepath.Clean(abs)
	s.mu.Lock()
	l := s.logs[abs]
	s.mu.Unlock()
	if l == nil {
		return nil
	}
	if err := l.Sync(); err != nil {
		return err
	}
	l.inner.DropPayloadCache()
	s.mu.Lock()
	if s.logs[abs] == l {
		delete(s.logs, abs)
	}
	s.mu.Unlock()
	return nil
}

// Close syncs queued entries and closes every disk log after all
// in-flight cache constructions finish. The Store cannot be used again.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	for len(s.opening) > 0 {
		var ready <-chan struct{}
		for _, pending := range s.opening {
			ready = pending.ready
			break
		}
		s.mu.Unlock()
		<-ready
		s.mu.Lock()
	}
	logs := make([]*Log, 0, len(s.logs))
	for _, l := range s.logs {
		logs = append(logs, l)
	}
	d := s.disk
	s.logs = nil
	s.mu.Unlock()
	var errs []error
	for _, l := range logs {
		if err := l.Sync(); err != nil {
			errs = append(errs, err)
		}
	}
	if err := d.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
