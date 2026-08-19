package form

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

// State is a per-aria form state handle.
//
// Concurrency contract: ONE WRITER, MANY READERS.
//
// The writer is the agent's inbox drain loop (Agent.act -> applyControlPatch
// -> State.Apply). Readers are everyone else: the figaro.form RPC
// handler, Agent.ApplyOutfit, Agent.formString/formInt via
// Agent.Info, and they run on RPC goroutines, concurrently with the writer.
//
// Because a Snapshot is an immutable persistent value, publishing one through
// an atomic.Pointer is all the synchronisation needed: readers are lock-free,
// each sees a complete board, and the happens-before edge that the plain field
// read used to lack is supplied by the atomic. Before this, State.Snapshot on
// an RPC goroutine raced State.Apply on the agent goroutine, a reader could
// range a map the writer was still filling, which is fatal-capable, not benign.
//
// THERE IS NO LONGER EXACTLY ONE WRITER, and this doc used to say what to do
// about that: "a second writer would need CompareAndSwap on the update path."
// Cast came off the agent's actor loop (the self-cast deadlock), so a cast
// publishes its study set from the CALLER's goroutine while the drain loop
// publishes a `set` from its own. Apply's old load-modify-publish then lost
// whichever finished second -- not a data race the detector can see (the
// pointer is atomic and snapshots are immutable), a LOST UPDATE, which is
// worse for being invisible.
//
// So Apply is a CAS retry loop now. It is cheap: the contended window is the
// tree apply, publication is one pointer swap, and a retry recomputes against
// a snapshot that is immutable by construction.
//
// A CAS makes each publication atomic; it does NOT make a stale whole-value
// write correct, and no lock can. A writer that computes a whole key from a
// read taken before someone else's write must order itself some other way --
// see Agent.publishStudies, which orders by the durable version its write
// landed at. Save, which runs only after the drain loop has exited
// (Agent.Kill waits on it), clears the dirty flag with a single non-looping
// CAS so that it cannot clobber a concurrent publication.
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
		return nil, fmt.Errorf("form.Open(%s): %w", path, err)
	}
	if len(data) == 0 {
		return s, nil
	}
	var snap Snapshot
	// Direct, not through json.Unmarshal: see formReduce's comment -
	// encoding/json pre-scans the whole document before handing it to an
	// Unmarshaler, which doubles the cost for identical bytes.
	if err := snap.UnmarshalJSON(data); err != nil {
		return nil, fmt.Errorf("form.Open: parse %s: %w", path, err)
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
// Writer-side only: see the concurrency contract on State.
//
// A patch that changes nothing publishes nothing and does not mark the
// state dirty: the tree returns a pointer-identical root for a
// semantically equal write, so there is no new state to persist.
func (s *State) Apply(p Patch) Snapshot {
	if p.IsEmpty() {
		return s.load().snapshot
	}
	for {
		old := s.published.Load()
		var cur board
		if old != nil {
			cur = *old
		}
		next := cur.snapshot.Apply(p)
		if next.root == cur.snapshot.root {
			return cur.snapshot
		}
		// Publish only against the board we computed from. A losing swap
		// means someone else published in between, and their state is the
		// one this patch must be applied to.
		if s.published.CompareAndSwap(old, &board{snapshot: next, dirty: true}) {
			return next
		}
	}
}

// Save flushes to disk if dirty. Atomic via tmp+rename.
func (s *State) Save() error {
	old := s.published.Load()
	if old == nil || !old.dirty || s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("form.Save: mkdir: %w", err)
	}
	data, err := old.snapshot.MarshalJSON() // direct; see Open
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
