package chalkboard

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
)

// board is the unit of publication: the current snapshot plus whether it
// holds changes not yet on disk. The two travel together so a reader can
// never observe one without the other, and so publishing is a single
// atomic pointer store.
type board struct {
	snapshot Snapshot
	dirty    bool
}

// State is a per-aria chalkboard state handle.
//
// Concurrency contract: ONE WRITER, MANY READERS.
//
// The writer is the agent's inbox drain loop (Agent.act -> applyControlPatch
// -> State.Apply). Readers are everyone else — the figaro.chalkboard RPC
// handler, Agent.ApplyOutfit, Agent.chalkboardString/chalkboardInt via
// Agent.Info — and they run on RPC goroutines, concurrently with the writer.
//
// Because a Snapshot is an immutable persistent value, publishing one through
// an atomic.Pointer is all the synchronisation needed: readers are lock-free,
// each sees a complete board, and the happens-before edge that the plain field
// read used to lack is supplied by the atomic. Before this, State.Snapshot on
// an RPC goroutine raced State.Apply on the agent goroutine — a reader could
// range a map the writer was still filling, which is fatal-capable, not benign.
//
// A second writer would need CompareAndSwap on the update path. There is
// exactly one, so Apply stores unconditionally; Save, which runs only after the
// drain loop has exited (Agent.Kill waits on it), clears the dirty flag with a
// single non-looping CAS so that it cannot clobber a concurrent publication.
type State struct {
	published atomic.Pointer[board]
	path      string
}

// Open reads the snapshot at path. Missing file = empty state.
func Open(path string) (*State, error) {
	s := &State{path: path}
	s.publish(board{})
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
	var snap Snapshot
	// Direct, not through json.Unmarshal: see chalkboardReduce's comment —
	// encoding/json pre-scans the whole document before handing it to an
	// Unmarshaler, which doubles the cost for identical bytes.
	if err := snap.UnmarshalJSON(data); err != nil {
		return nil, fmt.Errorf("chalkboard.Open: parse %s: %w", path, err)
	}
	s.publish(board{snapshot: snap})
	return s, nil
}

// load reads the published board. The zero State reads as an empty board.
func (s *State) load() board {
	if b := s.published.Load(); b != nil {
		return *b
	}
	return board{}
}

func (s *State) publish(b board) { s.published.Store(&b) }

// Snapshot returns the current board. Lock-free and safe from any
// goroutine: snapshots are immutable, so the caller holds a stable,
// self-consistent view no matter what the writer does next.
func (s *State) Snapshot() Snapshot {
	return s.load().snapshot
}

// Apply advances the state by the patch and returns the new board.
// Writer-side only — see the concurrency contract on State.
//
// A patch that changes nothing publishes nothing and does not mark the
// state dirty: the tree returns a pointer-identical root for a
// semantically equal write, so there is no new state to persist.
func (s *State) Apply(p Patch) Snapshot {
	cur := s.load()
	if p.IsEmpty() {
		return cur.snapshot
	}
	next := cur.snapshot.Apply(p)
	if next.root == cur.snapshot.root {
		return cur.snapshot
	}
	s.publish(board{snapshot: next, dirty: true})
	return next
}

// Save flushes to disk if dirty. Atomic via tmp+rename.
func (s *State) Save() error {
	old := s.published.Load()
	if old == nil || !old.dirty || s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("chalkboard.Save: mkdir: %w", err)
	}
	data, err := old.snapshot.MarshalJSON() // direct; see Open
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
	// Clear dirty only if nothing was published while we were writing. A
	// failed swap means newer state exists and is legitimately still dirty.
	s.published.CompareAndSwap(old, &board{snapshot: old.snapshot})
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
