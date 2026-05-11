package chalkboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// State is a per-aria chalkboard state handle. Single-owner (no
// concurrent access).
type State struct {
	snapshot Snapshot
	path     string
	dirty    bool
}

// Open reads the snapshot at path. Missing file = empty state.
func Open(path string) (*State, error) {
	s := &State{
		path:     path,
		snapshot: Snapshot{},
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("chalkboard.Open(%s): %w", path, err)
	}
	if len(data) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s.snapshot); err != nil {
		return nil, fmt.Errorf("chalkboard.Open: parse %s: %w", path, err)
	}
	if s.snapshot == nil {
		s.snapshot = Snapshot{}
	}
	return s, nil
}

// Snapshot returns a deep clone of the state.
func (s *State) Snapshot() Snapshot {
	return s.snapshot.Clone()
}

// Apply mutates the snapshot and marks dirty.
func (s *State) Apply(p Patch) Snapshot {
	if p.IsEmpty() {
		return s.Snapshot()
	}
	s.snapshot = s.snapshot.Apply(p)
	s.dirty = true
	return s.Snapshot()
}

// Save flushes to disk if dirty. Atomic via tmp+rename.
func (s *State) Save() error {
	if !s.dirty || s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("chalkboard.Save: mkdir: %w", err)
	}
	data, err := json.Marshal(s.snapshot)
	if err != nil {
		return fmt.Errorf("chalkboard.Save: marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("chalkboard.Save: write tmp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("chalkboard.Save: rename: %w", err)
	}
	s.dirty = false
	return nil
}

// Path returns the snapshot file path.
func (s *State) Path() string {
	return s.path
}

// Close flushes and releases the State.
func (s *State) Close() error {
	return s.Save()
}
