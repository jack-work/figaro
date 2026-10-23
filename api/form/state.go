package form

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
)

// edition is the unit of publication: snapshot and dirty travel together under
// one atomic pointer store.
type edition struct {
	snapshot Snapshot
	dirty    bool
}

type State struct {
	published atomic.Pointer[edition]
	path      string
}

// Open reads the snapshot at path; a missing file is an empty state.
func Open(path string) (*State, error) {
	s := &State{path: path}
	s.publish(edition{})
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("form.Open(%s): %w", path, err)
	}
	if len(data) == 0 {
		return s, nil
	}
	var snap Snapshot
	// Direct: json.Unmarshal pre-scans the whole document before handing it
	// to an Unmarshaler.
	if err := snap.UnmarshalJSON(data); err != nil {
		return nil, fmt.Errorf("form.Open: parse %s: %w", path, err)
	}
	s.publish(edition{snapshot: snap})
	return s, nil
}

func (s *State) load() edition {
	if b := s.published.Load(); b != nil {
		return *b
	}
	return edition{}
}

func (s *State) publish(b edition) { s.published.Store(&b) }

// Snapshot is lock-free and safe from any goroutine.
func (s *State) Snapshot() Snapshot {
	return s.load().snapshot
}

func (s *State) Apply(p Patch) Snapshot {
	if p.IsIdentity() {
		return s.load().snapshot
	}
	for {
		old := s.published.Load()
		var cur edition
		if old != nil {
			cur = *old
		}
		next := cur.snapshot.Apply(p)
		if next.Root().Equal(cur.snapshot.Root()) {
			return cur.snapshot
		}
		if s.published.CompareAndSwap(old, &edition{snapshot: next, dirty: true}) {
			return next
		}
	}
}

// Save flushes if dirty, via tmp+rename.
func (s *State) Save() error {
	old := s.published.Load()
	if old == nil || !old.dirty || s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("form.Save: mkdir: %w", err)
	}
	data, err := old.snapshot.MarshalJSON()
	if err != nil {
		return fmt.Errorf("form.Save: marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("form.Save: write tmp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("form.Save: rename: %w", err)
	}
	// A failed swap means newer state exists and is still dirty.
	s.published.CompareAndSwap(old, &edition{snapshot: old.snapshot})
	return nil
}

func (s *State) Path() string {
	return s.path
}

func (s *State) Close() error {
	return s.Save()
}
