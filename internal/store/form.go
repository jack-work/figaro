package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
)

// Form is an aria's state, and the only writer of the channel that holds it.
//
// ONE LOCK owns the append, not one goroutine. Serialization is all the
// writer ever needed, and a parked goroutine per open form cost the daemon
// one goroutine for every aria anyone had listed. Readers never touch it: a
// published state is swapped in atomically, so Snapshot is one load and
// cannot block, cannot wake a dormant aria, and cannot be serialized behind
// a turn.
//
// DURABILITY PRECEDES VISIBILITY, structurally. The writer appends, and only
// then publishes. The reverse is not a lost write but a hallucinated one: the
// patch is projected to the model as a reminder on the next tic, so the agent
// would act on state that will not survive a restart.
//
// THE WRITER DOES I/O AND NOTHING ELSE. It never calls back into an agent,
// never renders, never waits on a turn. That is what makes Apply safe to call
// from inside a turn: even from a tool call: where the older design, which
// routed storage through the queue that also owns turns, could close a wait
// cycle and hang. A Form is happy to exist with no aria attached at all.
type Form struct {
	log   FormLog
	state atomic.Pointer[formState]

	// write serializes commits: the single writer, held across the append
	// and the publish so durability precedes visibility.
	write  sync.Mutex
	closed bool

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
}

type formResult struct {
	version uint64
	// applied is what the writer actually committed after reducing the
	// caller's patch against the published state: the keys that really
	// changed, and the removals that really removed something.
	applied message.Patch
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
	return f, nil
}

// Snapshot is the published state and the version it stands at. Lock-free.
func (f *Form) Snapshot() (form.Snapshot, uint64) {
	st := f.state.Load()
	return st.snap, st.version
}

// Version is the durable index of the last patch applied.
func (f *Form) Version() uint64 { return f.state.Load().version }

// PatchesBetween returns the published patches in the absolute range
// (after, upTo], as a VIEW on the published state's array: no copy.
//
// The view is safe for the same reason commit's shared-array append is safe,
// and it is the read half of that decision. A published formState is
// immutable: its slice header carries its own length, and the single writer
// (one at a time, under f.write) only ever appends PAST that length or
// reallocates, so bytes a reader can see never change under it. The returned
// slice is capped (ps[lo:hi:hi]) so a caller that appends to it reallocates
// instead of scribbling into the writer's array.
//
// The writer became a mutex rather than a goroutine in the trunk-presentation
// work, which changes nothing here: what the view needs is that commits
// SERIALIZE and that state is published atomically, and both still hold.
//
// It replaces Patches(), which copied the entire history on every call: once
// per studied form per provider Send, to answer a question whose answer is
// almost always one patch or none. That copy was defending against a mutation
// this type structurally cannot perform.
//
// Absolute rather than cursor-relative because the projection warm-starts
// mid-log: see the note on the caller side about what a relative cursor did.
// Binary search on both ends, so a pass costs O(log n) per call and holds no
// position between calls, which is what lets one accessor be shared and long
// lived instead of rebuilt per Send.
func (f *Form) PatchesBetween(after, upTo uint64) []VersionedPatch {
	ps := f.state.Load().patches
	out := patchRange(ps, after, upTo)
	// The pair the old API could not report: what this read answered with,
	// and how long the history behind it was. Free here (both are in hand),
	// and it is the only place that knows both.
	figOtel.RecordFormPatchRead(context.Background(), len(out), len(ps))
	return out
}

// patchRange is the range itself: binary search on both ends of a
// version-ordered, immutable array.
//
// The capped slice (ps[lo:hi:hi]) is not decoration. Without the cap, a caller
// that appends to the returned slice writes into the writer's backing array,
// past the length every published state can see but inside the capacity the
// next commit will append into: a lost write with no crash and no test that
// finds it. The cap turns that into a reallocation.
func patchRange(ps []VersionedPatch, after, upTo uint64) []VersionedPatch {
	if upTo <= after || len(ps) == 0 {
		return nil
	}
	lo := sort.Search(len(ps), func(i int) bool { return ps[i].Version > after })
	hi := sort.Search(len(ps), func(i int) bool { return ps[i].Version > upTo })
	if lo >= hi {
		return nil
	}
	return ps[lo:hi:hi]
}

// Apply appends a patch and publishes it, returning its durable version.
//
// ifVersion, when non-zero, refuses unless the form still stands at that
// version. The comparison happens in the writer, where it is atomic with the
// append: which is what makes a read-modify-write (an array element, a nested
// field) safe against a second shell doing the same thing.
func (f *Form) Apply(patch message.Patch, ifVersion uint64) (uint64, error) {
	version, _, err := f.ApplyEffect(patch, ifVersion)
	return version, err
}

// ApplyEffect is Apply, and also says what actually landed: the patch after
// the writer reduced it against the published state. A caller that reports to
// a human ("set 3 keys") or fans a delta out to listeners wants THAT, not what
// it asked for.
func (f *Form) ApplyEffect(patch message.Patch, ifVersion uint64) (uint64, message.Patch, error) {
	f.write.Lock()
	defer f.write.Unlock()
	if f.closed {
		return 0, message.Patch{}, fmt.Errorf("form is closed")
	}
	res := f.commit(formWrite{patch: patch, ifVersion: ifVersion})
	return res.version, res.applied, res.err
}

// OnCommit registers a sink for committed patches, called AFTER the append and
// the publish: never before, so an observer can never see state that would not
// survive a restart.
//
// It runs UNDER THE WRITE LOCK, so it obeys the writer's law: hand the delta
// off and return. A sink that blocks on anything which might be waiting on
// this form stops every write to it. The routing layer's own queue is the right place to
// put the work.
func (f *Form) OnCommit(fn func(version uint64, patch message.Patch)) {
	f.mu.Lock()
	f.onCommit = append(f.onCommit, fn)
	f.mu.Unlock()
}

// Close stops the writer. Further writes are refused rather than dropped.
// Idempotent: eviction and a delete can both reach the same form.
func (f *Form) Close() {
	f.write.Lock()
	f.closed = true
	f.write.Unlock()
}

func (f *Form) commit(w formWrite) formResult {
	st := f.state.Load()
	if w.ifVersion != 0 && st.version != w.ifVersion {
		return formResult{err: fmt.Errorf(
			"form moved: at version %d, not %d: re-read and retry", st.version, w.ifVersion)}
	}
	// REDUCE FIRST. A patch is only an event if it changes something: keys
	// already holding the value asked for are dropped, removals of keys that
	// are not there are dropped, and a patch that survives none of that is a
	// no-op: no record, no version, no delta.
	//
	// It happens HERE, in the writer, and not in either caller, for two
	// reasons. It is the only place where the diff is atomic with the append,
	// so a filter cannot lose a write to a racing one (read-then-filter in a
	// handler can: two shells, one setting a=2 and one setting a=1 against a
	// board holding a=1, and the second silently drops the write that would
	// have won). And it is the only place BOTH write paths pass through, so
	// the agent's board and an agentless form obey one rule instead of two -
	// which matters most for observation, where a no-op patch on a role would
	// otherwise move its version and make an observing aria derive a
	// transition that announces nothing.
	applied := effectivePatch(st.snap, w.patch)
	if applied.IsEmpty() {
		return formResult{version: st.version, applied: applied}
	}
	payload, err := json.Marshal(applied)
	if err != nil {
		return formResult{err: err}
	}
	version, err := f.log.AppendPatch(payload)
	if err != nil {
		return formResult{err: err}
	}
	next := &formState{snap: st.snap.Apply(applied), version: version, patches: st.patches}
	if !applied.IsEmpty() {
		// Append to the SHARED backing array rather than copying the history.
		// Safe because there is exactly one writer: a published state holds a
		// slice header with its own length, so a later append either writes
		// past that length (which no reader reads) or reallocates. Copying
		// instead made every write O(history): 14µs to 40µs on an aria with a
		// few hundred patches, and worse the longer it lived.
		next.patches = append(st.patches, VersionedPatch{Version: version, Patch: applied})
	}
	f.state.Store(next)
	f.mu.Lock()
	sinks := f.onCommit
	f.mu.Unlock()
	for _, fn := range sinks {
		fn(version, applied)
	}
	return formResult{version: version, applied: applied}
}

// effectivePatch is what a patch actually does to a state: the keys it
// changes, and the removals that remove something. form.Additive is the one
// implementation of the first half: every place that dresses a board must
// agree about what "already wearing it" means, and the second half is the
// same question asked of Remove.
func effectivePatch(snap form.Snapshot, p message.Patch) message.Patch {
	out := form.Additive(snap, message.Patch{Set: p.Set})
	for _, k := range p.Remove {
		if snap.Has(k) {
			out.Remove = append(out.Remove, k)
		}
	}
	return out
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
