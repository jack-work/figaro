package chalkboard

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
)

// board is the unit of publication: the current snapshot, whether it holds
// changes not yet on disk, and the durable VERSION the snapshot reflects. The
// three travel together so a reader can never observe one without the others,
// and so publishing is a single atomic pointer store.
//
// version is ASSIGNED, never generated here. It is the append index the store
// gave the patch (the chalkboard channel's own position, or the IR LT for an
// ephemeral aria). A counter incremented by State would be a second number
// claiming to be the version, and the two would drift the moment anything
// reached the channel outside State's knowledge — which is exactly what a fold
// at Open does.
type board struct {
	snapshot Snapshot
	dirty    bool
	version  uint64
}

// State is a per-aria chalkboard state handle.
//
// Concurrency contract: ONE WRITER, MANY READERS.
//
// The writer is the agent's inbox drain loop (Agent.act -> applyControlPatch
// -> State.Apply). Readers are everyone else — the figaro.chalkboard RPC
// handler, Agent.ApplyLoadout, Agent.chalkboardString/chalkboardInt via
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

// Apply advances the state by the patch without moving the version. It is the
// unversioned form, for callers with no durable index to quote — an ephemeral
// aria whose patch rides a message that has not been appended yet.
//
// A patch that changes nothing publishes nothing and does not mark the state
// dirty: the tree returns a pointer-identical root for a semantically equal
// write, so there is no new state to persist.
func (s *State) Apply(p Patch) Snapshot {
	return s.ApplyAt(0, p)
}

// ApplyAt advances the state by the patch and records that it now reflects the
// given durable version. Writer-side only — see the concurrency contract on
// State. Pass 0 when there is no durable index (see Apply).
//
// The version moves even when the patch is a semantic no-op. Re-writing the
// value a key already holds leaves the tree pointer-identical, but it was still
// an append, so the durable cursor moved; refusing to record that would leave
// State.Version() lagging the channel and every client resuming from it would
// re-read patches it already had.
func (s *State) ApplyAt(version uint64, p Patch) Snapshot {
	cur := s.load()
	if p.IsEmpty() && version <= cur.version {
		return cur.snapshot
	}
	next := cur.snapshot.Apply(p)
	changed := next.root != cur.snapshot.root
	if !changed && version <= cur.version {
		return cur.snapshot
	}
	v := cur.version
	if version > v {
		v = version
	}
	s.publish(board{snapshot: next, dirty: cur.dirty || changed, version: v})
	return next
}

// SnapshotAt returns the snapshot and the version it reflects from ONE atomic
// load. Snapshot() and Version() called separately are TWO loads and can
// straddle a publication, yielding a snapshot labelled with a version it does
// not actually contain — after which a client resuming from that version skips
// the patch that landed in between. Any caller that reports both to a client
// must use this.
func (s *State) SnapshotAt() (Snapshot, uint64) {
	b := s.load()
	return b.snapshot, b.version
}

// Version is the durable index this board reflects: the highest append position
// folded into it. Lock-free, safe from any goroutine. Zero means nothing
// versioned has been applied — a fresh ephemeral board.
//
// This is the number a subscriber resumes from: "here is the state, it is at
// version N, now stream me N+1 onward".
func (s *State) Version() uint64 { return s.load().version }

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
	//
	// The version MUST be carried across: this rebuilds the board from
	// scratch, so a field omitted here is a field Save silently resets. A
	// rewound version would hand every reconnecting subscriber a cursor
	// pointing behind the state it already holds.
	s.published.CompareAndSwap(old, &board{snapshot: old.snapshot, version: old.version})
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
