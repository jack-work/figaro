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
	return s, nil
}

// Snapshot returns the current board. Snapshots are immutable values,
// so the caller holds a stable view no matter what happens next.
func (s *State) Snapshot() Snapshot {
	return s.snapshot
}

// Apply advances the state by the patch and returns the new board.
//
// A patch that changes nothing leaves the state (and the dirty flag)
// alone: the tree returns a pointer-identical root for a semantically
// equal write, so there is nothing to persist.
func (s *State) Apply(p Patch) Snapshot {
	if p.IsEmpty() {
		return s.snapshot
	}
	next := s.snapshot.Apply(p)
	if next.root == s.snapshot.root {
		return s.snapshot
	}
	s.snapshot = next
	s.dirty = true
	return s.snapshot
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
