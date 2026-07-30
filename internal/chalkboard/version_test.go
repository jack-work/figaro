package chalkboard

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func p(kv map[string]string) Patch {
	set := map[string]json.RawMessage{}
	for k, v := range kv {
		b, _ := json.Marshal(v)
		set[k] = b
	}
	return Patch{Set: set}
}

// The version a board carries is ASSIGNED by the store, never generated here.
// So ApplyAt must record exactly what it is handed, and must never move the
// version backwards — a rewind would hand a resuming subscriber a cursor
// pointing behind state it already holds.
func TestApplyAtRecordsAssignedVersion(t *testing.T) {
	s, _ := Open("")
	if got := s.Version(); got != 0 {
		t.Fatalf("fresh board version = %d, want 0", got)
	}
	s.ApplyAt(7, p(map[string]string{"a": "1"}))
	if got := s.Version(); got != 7 {
		t.Fatalf("version = %d, want 7", got)
	}
	// A stale (lower) version must not rewind the cursor.
	s.ApplyAt(3, p(map[string]string{"b": "2"}))
	if got := s.Version(); got != 7 {
		t.Fatalf("version rewound to %d, want 7", got)
	}
	// The value still landed — a stale version does not discard the patch.
	if v, ok := s.Snapshot().Get("b"); !ok || string(v) != `"2"` {
		t.Fatalf("b = %q ok=%v, want \"2\"", string(v), ok)
	}
}

// A patch that rewrites the value a key already holds leaves the tree
// pointer-identical, but it WAS still an append, so the durable cursor moved.
// Refusing to record that leaves Version() lagging the channel, and every
// client resuming from it re-reads patches it already had.
func TestApplyAtAdvancesVersionOnSemanticNoOp(t *testing.T) {
	s, _ := Open("")
	s.ApplyAt(1, p(map[string]string{"k": "same"}))
	before := s.Snapshot()
	s.ApplyAt(2, p(map[string]string{"k": "same"}))
	if got := s.Version(); got != 2 {
		t.Fatalf("no-op did not advance version: got %d, want 2", got)
	}
	if s.Snapshot().root != before.root {
		t.Fatal("semantic no-op changed the tree root")
	}
}

// Save rebuilds the board to clear the dirty flag. Every field it omits is a
// field it silently resets — and a reset version is a rewound cursor for every
// reconnecting subscriber.
func TestSavePreservesVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cb.json")
	s, _ := Open(path)
	s.ApplyAt(42, p(map[string]string{"k": "v"}))
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := s.Version(); got != 42 {
		t.Fatalf("Save reset the version to %d, want 42", got)
	}
}
