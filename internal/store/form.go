package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/internal/actor"
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
	q     *actor.Lazy[*formWrite]
	state atomic.Pointer[formState]

	// tick is the broadcast: closed and replaced after every batch, so a
	// waiter parks on it rather than on a channel of its own. Completion is
	// per submission (formWrite.done), never a watermark: tickets are handed
	// out before the queue is entered, so a high ticket can land in an
	// earlier batch than a low one and a watermark would release a waiter
	// whose result had not been written yet.
	tick atomic.Pointer[chan struct{}]

	// sinks and closed are drainer-owned except for the CAS in OnCommit:
	// an immutable slice behind a pointer, so emitting takes no lock.
	sinks  atomic.Pointer[[]func(uint64, message.Patch)]
	subs   atomic.Pointer[[]*Subscription]
	closed atomic.Bool
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
	intent    Intent
	result    formResult
	done      atomic.Bool
}

// Intent says what a REMOVAL means, which is the one place a command and its
// event are allowed to disagree about legality.
type Intent uint8

const (
	// Ensure: the caller wants the key absent and does not care whether it
	// was. Birth dressing means this, and `-D` may name a key the parent
	// closure never held.
	Ensure Intent = iota
	// Assert: the caller believes the key is there. Removing one that is not
	// is a refusal, because the caller's model of the world is wrong and
	// telling it so is more useful than silently agreeing.
	Assert
)

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
	// SyncThrough makes every record up to index durable. It runs between a
	// batch's appends and its publish, which is the whole of "durable before
	// visible": one sync covers the batch.
	SyncThrough(index uint64) error
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
	tick := make(chan struct{})
	f.tick.Store(&tick)
	empty := []func(uint64, message.Patch){}
	f.sinks.Store(&empty)
	subsInit(&f.subs)
	f.q = actor.NewLazy(formBatch, formLinger, f.runBatch)
	return f, nil
}

const (
	// formBatch caps one drain so a burst on one form cannot hold figwal's
	// per-lineage lock against every other node forked from the same root.
	formBatch = 64
	// formLinger keeps the drainer through a burst. Long enough that a tool
	// loop's writes share one goroutine, short enough that an idle form
	// holds nothing.
	formLinger = 2 * time.Second
)

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
	return f.ApplyEffectIntent(patch, ifVersion, Ensure)
}

// ApplyEffectIntent is ApplyEffect with the removal rule named. Under Assert
// a removal of a key that is not there is refused rather than reduced away.
func (f *Form) ApplyEffectIntent(patch message.Patch, ifVersion uint64, intent Intent) (uint64, message.Patch, error) {
	w := &formWrite{patch: patch, ifVersion: ifVersion, intent: intent}
	if err := f.q.Submit(w); err != nil {
		return 0, message.Patch{}, fmt.Errorf("form is closed")
	}
	f.await(w)
	return w.result.version, w.result.applied, w.result.err
}

// await parks the CALLER'S own goroutine until its submission has been
// answered. One broadcast serves every waiter: no goroutine and no channel
// per call.
func (f *Form) await(w *formWrite) {
	for {
		tick := *f.tick.Load()
		if w.done.Load() {
			return
		}
		<-tick
	}
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
	for {
		old := f.sinks.Load()
		next := make([]func(uint64, message.Patch), len(*old), len(*old)+1)
		copy(next, *old)
		next = append(next, fn)
		if f.sinks.CompareAndSwap(old, &next) {
			return
		}
	}
}

// Close stops the writer. Further writes are refused rather than dropped.
// Idempotent: eviction and a delete can both reach the same form.
func (f *Form) Close() {
	f.closed.Store(true)
	f.q.Close()
}

// runBatch is the whole protocol: reduce each submission in turn against a
// RUNNING state, append, ONE sync for the batch, publish, then answer and
// emit.
//
// Batching is for DURABILITY, never for semantics. Each write is reduced
// against the state as of its own position, or two writers' ifVersion guards
// stop meaning anything: the first must win and the second must see the
// moved version.
func (f *Form) runBatch(batch []*formWrite) {
	published := f.state.Load()
	var lastRecord uint64
	var events []versionedApplied

	for _, w := range batch {
		next, res := f.reduceOne(published, w)
		w.result = res
		if next != nil {
			published = next
			lastRecord = next.version
			events = append(events, versionedApplied{next.version, res.applied})
		}
	}

	if lastRecord != 0 {
		started := time.Now()
		err := f.log.SyncThrough(lastRecord)
		figOtel.RecordSync(context.Background(), time.Since(started), len(events))
		if err != nil {
			// Nothing was published. The records are on the caller side of
			// durability, so the honest answer is that they did not happen:
			// every write in this batch is rejected and the state stands
			// where it stood.
			for _, w := range batch {
				if w.result.err == nil {
					w.result = formResult{err: fmt.Errorf("form sync: %w", err)}
				}
			}
			f.answer(batch)
			return
		}
		f.state.Store(published)
	}
	// Emit BEFORE answering. A caller that returns from Apply has always been
	// able to assume the delta reached the sinks, and moving the fanout off
	// its goroutine would silently withdraw that. The exposure is the one the
	// write lock already had: a sink must hand off and return.
	f.emit(events)
	f.publish(events)
	f.answer(batch)
}

type versionedApplied struct {
	version uint64
	applied message.Patch
}

func (f *Form) reduceOne(st *formState, w *formWrite) (*formState, formResult) {
	if f.closed.Load() {
		return nil, formResult{err: fmt.Errorf("form is closed")}
	}
	if w.ifVersion != 0 && st.version != w.ifVersion {
		return nil, formResult{err: fmt.Errorf(
			"form moved: at version %d, not %d: re-read and retry", st.version, w.ifVersion)}
	}
	if w.intent == Assert {
		for _, k := range w.patch.Remove {
			if !st.snap.Has(k) {
				return nil, formResult{version: st.version,
					err: fmt.Errorf("remove %q: no such key", k)}
			}
		}
	}
	// REDUCE FIRST, and purely. A patch is only an event if it changes
	// something, and the reduce touches nothing, which is why a failure
	// anywhere below leaves the published state exactly as it was and there
	// is nothing to roll back.
	//
	// It happens HERE and not in either caller because this is the only
	// place the diff is atomic with the append: read-then-filter in a
	// handler loses a write to a racing one.
	applied := effectivePatch(st.snap, w.patch)
	if applied.IsEmpty() {
		return nil, formResult{version: st.version, applied: applied}
	}
	payload, err := json.Marshal(applied)
	if err != nil {
		return nil, formResult{err: err}
	}
	version, err := f.log.AppendPatch(payload)
	if err != nil {
		return nil, formResult{err: err}
	}
	next := &formState{snap: st.snap.Apply(applied), version: version}
	// Append to the SHARED backing array rather than copying the history.
	// Safe because there is exactly one drainer: a published state holds a
	// slice header with its own length, so a later append either writes past
	// that length (which no reader reads) or reallocates.
	next.patches = append(st.patches, VersionedPatch{Version: version, Patch: applied})
	return next, formResult{version: version, applied: applied}
}

// answer releases the batch's results and wakes every waiter once.
func (f *Form) answer(batch []*formWrite) {
	for _, w := range batch {
		w.done.Store(true)
	}
	next := make(chan struct{})
	old := f.tick.Swap(&next)
	close(*old)
}

// emit hands committed patches to the sinks, after the publish, never
// before. A sink that panics must not take the drainer with it.
func (f *Form) emit(events []versionedApplied) {
	if len(events) == 0 {
		return
	}
	sinks := *f.sinks.Load()
	for _, ev := range events {
		for _, fn := range sinks {
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("form commit sink panicked", "err", r)
					}
				}()
				fn(ev.version, ev.applied)
			}()
		}
	}
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

// SyncThrough is a no-op: memory is as durable as this log gets.
func (m *MemFormLog) SyncThrough(uint64) error { return nil }

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

func (l *xwalFormLog) SyncThrough(index uint64) error {
	return l.backend.store.trunks.SyncChannelThrough(l.ariaID, chanForm, index)
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
