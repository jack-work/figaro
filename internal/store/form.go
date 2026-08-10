package store

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/jack-work/figaro/internal/actor"
	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
)

// Form is an aria's state, and the only writer of the channel that holds it.
//
// One goroutine owns the append. Readers never touch it: a published state is
// swapped in atomically, so Snapshot is one load and cannot block, cannot wake
// a dormant aria, and cannot be serialized behind a turn.
//
// DURABILITY PRECEDES VISIBILITY, structurally. The writer appends, and only
// then publishes. The reverse is not a lost write but a hallucinated one: the
// patch is projected to the model as a reminder on the next tic, so the agent
// would act on state that will not survive a restart.
//
// THE WRITER DOES I/O AND NOTHING ELSE. It never calls back into an agent,
// never renders, never waits on a turn. That is what makes Apply safe to call
// from inside a turn — even from a tool call — where the older design, which
// routed storage through the queue that also owns turns, could close a wait
// cycle and hang. A Form is happy to exist with no aria attached at all.
type Form struct {
	log   FormLog
	write *actor.Queue[formWrite]
	state atomic.Pointer[formState]

	mu       sync.Mutex
	onCommit []func(version uint64, patch message.Patch)
}

// formState is one published version: the tree, the durable index it stands
// at, and the patches that built it (the projection renders each at the record
// it landed on, which a folded snapshot cannot say).
type formState struct {
	snap    form.Snapshot
	version uint64
	patches []VersionedPatch
}

type formWrite struct {
	patch     message.Patch
	ifVersion uint64
	reply     chan formResult
}

type formResult struct {
	version uint64
	err     error
}

// FormLog is the durable half: an append-only sequence of patch payloads,
// indexed from 1. The xwal-backed implementation is the real one; a memory
// implementation is enough to hold a form on its own.
type FormLog interface {
	// AppendPatch returns the index the payload landed at.
	AppendPatch(payload []byte) (uint64, error)
	// RangePatches visits every record in index order.
	RangePatches(fn func(index uint64, payload []byte) error) error
}

// OpenForm replays the log and starts the writer. The replay is the cold cost;
// afterwards every read is an atomic load.
func OpenForm(log FormLog) (*Form, error) {
	f := &Form{log: log}
	st := &formState{}
	if err := log.RangePatches(func(index uint64, payload []byte) error {
		var p message.Patch
		if err := json.Unmarshal(payload, &p); err != nil {
			return err
		}
		st.snap = st.snap.Apply(p)
		st.version = index
		if !p.IsEmpty() {
			st.patches = append(st.patches, VersionedPatch{Version: index, Patch: p})
		}
		return nil
	}); err != nil {
		return nil, err
	}
	f.state.Store(st)
	// The runtime is internal/actor, the same one the aria's inbox runs on: one
	// goroutine, FIFO, close refuses. No Coalescer — two form patches could be
	// merged, but each carries its own version and its own reply, so folding
	// them would have to invent an answer for a caller that asked about one.
	f.write = actor.Start[formWrite](nil, func(w formWrite) { w.reply <- f.commit(w) }, nil)
	return f, nil
}

// Snapshot is the published state and the version it stands at. Lock-free.
func (f *Form) Snapshot() (form.Snapshot, uint64) {
	st := f.state.Load()
	return st.snap, st.version
}

// Version is the durable index of the last patch applied.
func (f *Form) Version() uint64 { return f.state.Load().version }

// Patches returns the patches that built the published state, in order.
func (f *Form) Patches() []VersionedPatch {
	st := f.state.Load()
	return append([]VersionedPatch(nil), st.patches...)
}

// Apply appends a patch and publishes it, returning its durable version.
//
// ifVersion, when non-zero, refuses unless the form still stands at that
// version. The comparison happens in the writer, where it is atomic with the
// append — which is what makes a read-modify-write (an array element, a nested
// field) safe against a second shell doing the same thing.
func (f *Form) Apply(patch message.Patch, ifVersion uint64) (uint64, error) {
	reply := make(chan formResult, 1)
	if !f.write.Send(formWrite{patch: patch, ifVersion: ifVersion, reply: reply}) {
		return 0, fmt.Errorf("form is closed")
	}
	res := <-reply
	return res.version, res.err
}

// OnCommit registers a sink for committed patches, called AFTER the append and
// the publish — never before, so an observer can never see state that would not
// survive a restart.
//
// It runs ON THE WRITER, so it obeys the writer's law: hand the delta off and
// return. A sink that blocks on anything which might be waiting on this form
// stops every write to it. The routing layer's own queue is the right place to
// put the work.
func (f *Form) OnCommit(fn func(version uint64, patch message.Patch)) {
	f.mu.Lock()
	f.onCommit = append(f.onCommit, fn)
	f.mu.Unlock()
}

// Close stops the writer. Further writes are refused rather than dropped.
func (f *Form) Close() { f.write.Close() }

func (f *Form) commit(w formWrite) formResult {
	st := f.state.Load()
	if w.ifVersion != 0 && st.version != w.ifVersion {
		return formResult{err: fmt.Errorf(
			"form moved: at version %d, not %d — re-read and retry", st.version, w.ifVersion)}
	}
	payload, err := json.Marshal(w.patch)
	if err != nil {
		return formResult{err: err}
	}
	version, err := f.log.AppendPatch(payload)
	if err != nil {
		return formResult{err: err}
	}
	next := &formState{snap: st.snap.Apply(w.patch), version: version, patches: st.patches}
	if !w.patch.IsEmpty() {
		// Append to the SHARED backing array rather than copying the history.
		// Safe because there is exactly one writer: a published state holds a
		// slice header with its own length, so a later append either writes
		// past that length (which no reader reads) or reallocates. Copying
		// instead made every write O(history) — 14µs to 40µs on an aria with a
		// few hundred patches, and worse the longer it lived.
		next.patches = append(st.patches, VersionedPatch{Version: version, Patch: w.patch})
	}
	f.state.Store(next)
	f.mu.Lock()
	sinks := f.onCommit
	f.mu.Unlock()
	for _, fn := range sinks {
		fn(version, w.patch)
	}
	return formResult{version: version}
}

// MemFormLog holds a form's records in memory. It is what "a form without an
// aria" means in practice: the algebra and the MVCC state with no store under
// them, for a test, a tool, or a form that never needed to be durable.
type MemFormLog struct {
	mu      sync.Mutex
	records [][]byte
}

func (m *MemFormLog) AppendPatch(payload []byte) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, append([]byte(nil), payload...))
	return uint64(len(m.records)), nil
}

func (m *MemFormLog) RangePatches(fn func(uint64, []byte) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, rec := range m.records {
		if err := fn(uint64(i+1), rec); err != nil {
			return err
		}
	}
	return nil
}

// NewMemForm is a standalone form: no store, no aria, no daemon.
func NewMemForm() *Form {
	f, err := OpenForm(&MemFormLog{})
	if err != nil {
		panic(err) // a memory log cannot fail to replay nothing
	}
	return f
}

// xwalFormLog is the durable half of a backed Form: the aria's form channel.
//
// The append goes through Trunks.Append, not the raw channel, so the poison
// gate and the dirty/touch bookkeeping see form writes. It does not read the
// timeline: the channel is unkeyed, so a patch is written with no reference to
// the turn in flight, which is what lets a set land mid-turn.
type xwalFormLog struct {
	backend *XwalBackend
	ariaID  string
}

func (l *xwalFormLog) AppendPatch(payload []byte) (uint64, error) {
	return l.backend.store.trunks.Append(l.ariaID, chanForm, 0, payload, nil)
}

func (l *xwalFormLog) RangePatches(fn func(uint64, []byte) error) error {
	xw, err := l.backend.store.OpenNode(l.ariaID)
	if err != nil {
		return err
	}
	defer xw.Close()
	var first, last uint64
	for _, ch := range xw.Channels() {
		if ch.Name == chanForm {
			first, last = ch.First, ch.Last
			break
		}
	}
	if first == 0 && last > 0 {
		first = 1
	}
	for lt := first; lt >= 1 && lt <= last; lt++ {
		rec, err := xw.ReadAt(chanForm, lt)
		if err != nil {
			return err
		}
		if err := fn(lt, rec.Payload); err != nil {
			return err
		}
	}
	return nil
}
