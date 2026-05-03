package chalkboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// State is a per-aria chalkboard state handle. Owns the in-memory
// snapshot and its on-disk cache file (typically
// arias/{id}/chalkboard.json). Lifetime is bound to one Agent.
//
// Single-owner: safe for use by the agent's drain-loop goroutine
// without locking — the actor model serializes access. Concurrent
// access from multiple goroutines is not supported.
//
// TODO(parallelism): Snapshot() returns a defensive clone today.
// If multi-goroutine access ever lands, this should become a proper
// immutable / lock-guarded data structure or a copy-on-write
// persistent map. The current single-owner contract is documented
// here so future contributors don't bolt on a sync.Mutex without
// thinking about whether immutable-snapshot semantics are wanted.
type State struct {
	snapshot Snapshot
	path     string
	dirty    bool
}

// Open reads the snapshot at path into memory. If path does not
// exist, returns an empty State. The directory containing path is
// created lazily on first Save.
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

// Snapshot returns a deep clone of the current in-memory snapshot.
// Callers may retain the returned value safely; mutations do not
// affect the State.
func (s *State) Snapshot() Snapshot {
	return s.snapshot.Clone()
}

// Apply mutates the in-memory snapshot. Marks dirty. Returns the
// post-apply snapshot (a clone, per Snapshot()) for caller convenience.
func (s *State) Apply(p Patch) Snapshot {
	if p.IsEmpty() {
		return s.Snapshot()
	}
	s.snapshot = s.snapshot.Apply(p)
	s.dirty = true
	return s.Snapshot()
}

// Save flushes the snapshot to disk if dirty. Atomic via
// rewrite-tmp-rename (write to sibling .tmp file, os.Rename).
// Idempotent: a second Save() with no intervening Apply() is a no-op.
// In-memory state (Open with empty path) skips persistence entirely.
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
