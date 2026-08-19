package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/internal/actor"
	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
)

// Form is an aria's state, and the only writer of the channel that holds it.
//
// ONE DRAINER owns the append, and it exists only while there is work: an
// actor.Lazy, spawned on submit, gone after an idle window. It was a mutex,
// and before that a permanently parked goroutine per form, which cost the
// daemon one goroutine for every aria anyone had merely LISTED. Readers
// never touch it: a published state is swapped in atomically, so Snapshot is
// one load and cannot block, cannot wake a dormant aria, and cannot be
// serialized behind a turn.
//
// THREE INVARIANTS. Everything here depends on them and nothing may quietly
// relax one:
//
//  1. DURABILITY PRECEDES VISIBILITY. Reduce purely, append, fsync, publish.
//     A failed sync rejects and publishes nothing, which is safe because the
//     reduce mutates nothing: there is never anything to roll back. The
//     reverse ordering is not a lost write but a hallucinated one, since the
//     patch reaches the model as a reminder on the next tic.
//  2. BATCHING IS FOR DURABILITY, NEVER SEMANTICS. One drain covers many
//     submissions with one fsync, and each is still reduced against the
//     state as of its own position: otherwise the first ifVersion writer
//     stops winning and the second stops seeing that the form moved.
//  3. PatchesBetween IS A VIEW. Its safety rests on the published array
//     being append-only and the returned slice being capped. Anything that
//     compacts it, rewrites it, or hands out an uncapped slice breaks it
//     silently. TestFormPatchesBetweenIsAViewNotACopy is the guard.
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
	sinks atomic.Pointer[[]func(uint64, message.Patch)]
	// sealed is set by a tombstone and rebuilt from the published state at
	// open, so a dead form stays dead across a restart without anyone
	// re-declaring it.
	sealed atomic.Bool
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
	// trimmed is the highest version dropped from patches, or 0 if nothing
	// was. It is NOT inferrable from patches[0].Version: a no-op patch
	// appends no record, so a form whose first records changed nothing
	// legitimately starts at a version above 1, and reading the gap as a
	// trim sent every cold read to the log.
	trimmed uint64
}

type formWrite struct {
	patch     message.Patch
	ifVersion uint64
	intent    Intent
	priv      bool
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
	// RangePatches visits records in index order, from `from` (1 or 0 is the
	// beginning) through `upTo` (0 is the end). The BOUNDS are the point: a
	// cold read of a range the resident window no longer holds used to start
	// at record 1 and read its way up to the range, so a retranslate of an
	// aria with a long board was O(records x history).
	RangePatches(from, upTo uint64, fn func(index uint64, payload []byte) error) error
}

// OpenForm replays the log and starts the writer. The replay is the cold cost;
// afterwards every read is an atomic load.
func OpenForm(log FormLog) (*Form, error) {
	f := &Form{log: log}
	st := &formState{}
	if err := log.RangePatches(0, 0, func(index uint64, payload []byte) error {
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
	if st.snap.Has(TombstoneKey) {
		f.sealed.Store(true)
	}
	tick := make(chan struct{})
	f.tick.Store(&tick)
	empty := []func(uint64, message.Patch){}
	f.sinks.Store(&empty)
	subsInit(&f.subs)
	f.q = actor.NewLazy(formBatch, time.Duration(formLinger.Load()), f.runBatch)
	return f, nil
}

// formBatch caps one drain so a burst on one form cannot hold figwal's
// per-lineage lock against every other node forked from the same root.
const formBatch = 64

// patchWindow bounds the DECODED patch history a form keeps resident, and
// patchSlack is how far past it the array may run before it is copied down.
//
// Slack, because trimming is a COPY. The published array is shared by
// construction (that is what makes PatchesBetween a view), so re-slicing the
// front off releases nothing: a header into the middle pins the whole array.
// Copying on every write would be the O(history) cost this design removed,
// so it happens once per slack allowance instead.
var (
	patchWindow atomic.Int64
	patchSlack  = 256
)

func init() { patchWindow.Store(2048) }

// SetPatchWindow bounds resident decoded patches per form. Zero or negative
// retains everything, which is what figaro did before this existed.
func SetPatchWindow(n int) { patchWindow.Store(int64(n)) }

// formLinger is how long a drained writer waits before leaving. Package
// level so the daemon can set it once from config before any form opens;
// changing it later would leave already-open forms on the old value, which
// is not worth the coordination.
var formLinger atomic.Int64

func init() { formLinger.Store(int64(2 * time.Second)) }

// SetFormLinger sets the writer's affinity window. Call before opening
// forms.
func SetFormLinger(d time.Duration) {
	if d < 0 {
		d = 0
	}
	formLinger.Store(int64(d))
}

// Snapshot is the published state and the version it stands at. Lock-free.
func (f *Form) Snapshot() (form.Snapshot, uint64) {
	st := f.state.Load()
	return st.snap, st.version
}

// Read is the published pair: the state and the version it was published AT,
// from one atomic load, as one value.
//
// It exists because the pair is the unit of correctness and two accessors
// were an invitation. Every optimistic read-modify-write in this codebase
// computes from a state and then quotes a version to a conditional apply; if
// those come from separate loads, a writer landing between them yields a pair
// that NEVER EXISTED, the guard passes, and the write silently overwrites a
// change it never saw. That is unrecoverable for a board -- the board is what
// the reconciliation sweep recomputes FROM.
//
// So `Version()` is gone. A caller that wants the number takes the pair and
// reads its field, which costs nothing and cannot be split.
func (f *Form) Read() FormAt {
	st := f.state.Load()
	return FormAt{Snapshot: st.snap, Version: st.version}
}

// FormAt is a form's state together with the version it was published at.
// The zero value is an empty form at version 0.
type FormAt struct {
	Snapshot form.Snapshot
	Version  uint64
}

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
	st := f.state.Load()
	ps := st.patches
	// Only a TRIM sends a read to disk. A cold retranslate of history the
	// window no longer holds walks the log; everything else is the view.
	if upTo > after && st.trimmed > 0 && after < st.trimmed {
		if fromLog, ok := f.patchesFromLog(after, upTo); ok {
			figOtel.RecordFormPatchRead(context.Background(), len(fromLog), len(ps))
			return fromLog
		}
	}
	out := patchRange(ps, after, upTo)
	// The pair the old API could not report: what this read answered with,
	// and how long the history behind it was. Free here (both are in hand),
	// and it is the only place that knows both.
	figOtel.RecordFormPatchRead(context.Background(), len(out), len(ps))
	return out
}

// patchesFromLog re-reads a range the resident window no longer covers.
// Allocates, and is meant to: it is the cold path.
func (f *Form) patchesFromLog(after, upTo uint64) ([]VersionedPatch, bool) {
	var out []VersionedPatch
	err := f.log.RangePatches(after+1, upTo, func(index uint64, payload []byte) error {
		var p message.Patch
		if err := json.Unmarshal(payload, &p); err != nil {
			return err
		}
		if !p.IsEmpty() {
			out = append(out, VersionedPatch{Version: index, Patch: p})
		}
		return nil
	})
	if err != nil {
		slog.Warn("form patches from log", "after", after, "upTo", upTo, "err", err)
		return nil, false
	}
	return out, true
}

// patchRange is the range itself: binary search on both ends of a
// version-ordered, immutable array.
//
// The capped slice (ps[lo:hi:hi]) is not decoration. Without the cap, a caller
// that appends to the returned slice writes into the writer's backing array,
// past the length every published state can see but inside the capacity the
// next commit will append into: a lost write with no crash and no test that
// finds it. The cap turns that into a reallocation.
// trimPatches copies the tail down once the array has run a slack allowance
// past the window. Copying is the point: re-slicing would release nothing.
func trimPatches(ps []VersionedPatch, trimmed uint64) ([]VersionedPatch, uint64) {
	w := int(patchWindow.Load())
	if w <= 0 || len(ps) <= w+patchSlack {
		return ps, trimmed
	}
	cut := len(ps) - w
	kept := make([]VersionedPatch, w)
	copy(kept, ps[cut:])
	return kept, ps[cut-1].Version
}

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
	return f.applyEffect(patch, ifVersion, intent, false)
}

// ApplyEffectPrivileged is the harness's own write: it may touch keys the
// catalog marks system-managed. There is no wire field for this and there
// must never be one. Privilege is a property of the CALL SITE, checkable by
// grep and by the compiler.
func (f *Form) ApplyEffectPrivileged(patch message.Patch, ifVersion uint64) (uint64, message.Patch, error) {
	return f.applyEffect(patch, ifVersion, Ensure, true)
}

func (f *Form) applyEffect(patch message.Patch, ifVersion uint64, intent Intent, priv bool) (uint64, message.Patch, error) {
	t, err := f.submit(&formWrite{patch: patch, ifVersion: ifVersion, intent: intent, priv: priv})
	if err != nil {
		return 0, message.Patch{}, err
	}
	return f.Await(context.Background(), t)
}

// Ticket is a submitted write. Hold it to Await the verdict, or drop it and
// never wait: the write lands either way.
type Ticket struct{ w *formWrite }

// Submit queues a write and returns without waiting for it. The caller that
// does not need the version (a cursor, a derived fold) never waits at all,
// which is the only thing that removes a parked goroutine per writer.
func (f *Form) Submit(patch message.Patch, ifVersion uint64, intent Intent) (Ticket, error) {
	return f.submit(&formWrite{patch: patch, ifVersion: ifVersion, intent: intent})
}

// submit is the one place a write enters the queue. Privilege never reaches
// the public Submit: a caller that may write harness keys says so by using
// the privileged entry point, which is a call site rather than an argument.
func (f *Form) submit(w *formWrite) (Ticket, error) {
	if err := f.q.Submit(w); err != nil {
		return Ticket{}, ErrFormClosed
	}
	return Ticket{w: w}, nil
}

// Await parks the CALLER'S own goroutine until the write has been answered.
// One broadcast serves every waiter: no goroutine and no channel per call.
func (f *Form) Await(ctx context.Context, t Ticket) (uint64, message.Patch, error) {
	if t.w == nil {
		return 0, message.Patch{}, fmt.Errorf("await: no ticket")
	}
	for {
		tick := *f.tick.Load()
		if t.w.done.Load() {
			return t.w.result.version, t.w.result.applied, t.w.result.err
		}
		select {
		case <-tick:
		case <-ctx.Done():
			return 0, message.Patch{}, ctx.Err()
		}
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
		// RECORDS, not writes. A batch of 64 in which one patch changed
		// anything writes one record, and reporting the batch size there
		// would say group commit is working when it is not. The alarm this
		// instrument exists to raise is the reverse case, so it must count
		// what the sync actually covered.
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
		return nil, formResult{err: ErrFormClosed}
	}
	// A tombstone is final. The one write allowed past it is the tombstone
	// itself, which Tombstone makes idempotent rather than repeatable.
	if f.sealed.Load() {
		return nil, formResult{version: st.version, err: errSealed}
	}
	if w.ifVersion != 0 && st.version != w.ifVersion {
		return nil, formResult{err: fmt.Errorf("%w: at version %d, not %d: re-read and retry",
			ErrFormMoved, st.version, w.ifVersion)}
	}
	if err := form.CheckWritable(w.patch, w.priv); err != nil {
		return nil, formResult{version: st.version, err: err}
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
	next.patches, next.trimmed = trimPatches(append(st.patches, VersionedPatch{Version: version, Patch: applied}), st.trimmed)
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
// MemFormLog's writer is SINGLE BY CONSTRUCTION and its readers are not.
// AppendPatch is reached only from reduceOne, which is reached only from
// runBatch, which is the actor's ONE DRAINER. RangePatches is reached from
// patchesFromLog on whatever goroutine asks for a range.
//
// SO THE MUTEX WAS TWO DIFFERENT EXCLUSIONS WEARING ONE NAME. Writer-versus-
// writer was dead weight -- there is only ever one writer. Reader-versus-writer
// was REAL and may not simply be deleted. The cure is to publish an immutable
// value: readers load a slice header and walk it with no lock at all, the
// writer publishes a successor, and the concurrency survives the lock's
// removal.
//
// THE APPEND DOES NOT COPY THE HISTORY. append may write into the old array's
// spare capacity at an index PAST THE PUBLISHED LENGTH, where no reader can
// see it; the store is what publishes it. Same idiom, and same reason, as
// cachedLog's logView. Copy-on-write would have made N appends O(N^2), which
// is a complexity change nobody asked for.
type MemFormLog struct {
	records atomic.Pointer[[][]byte]

	// writing detects a SECOND CONCURRENT WRITER. The lock removal rests on
	// the single-writer contract, so the contract breaking must be REPORTED
	// rather than silently tolerated -- otherwise the removal is an assumption
	// instead of a design.
	writing atomic.Bool

	// testHold parks a writer inside the critical region so a test can
	// overlap two appends. nil in production.
	testHold func()
}

func (m *MemFormLog) AppendPatch(payload []byte) (uint64, error) {
	if !m.writing.CompareAndSwap(false, true) {
		return 0, fmt.Errorf("mem form log: a second concurrent AppendPatch. This log " +
			"is lock-free on the strength of a SINGLE-WRITER contract -- appends come " +
			"from Form.runBatch, the actor's one drainer -- and that contract has been " +
			"broken by the caller")
	}
	defer m.writing.Store(false)
	if m.testHold != nil {
		m.testHold()
	}
	old := m.records.Load()
	var cur [][]byte
	if old != nil {
		cur = *old
	}
	next := append(cur, append([]byte(nil), payload...))
	m.records.Store(&next)
	return uint64(len(next)), nil
}

// SyncThrough is a no-op: memory is as durable as this log gets.
func (m *MemFormLog) SyncThrough(uint64) error { return nil }

// RangePatches TAKES NO LOCK. It loads one published slice header and walks
// it; a writer appending concurrently publishes a successor that this call
// simply does not see, which is a consistent older history rather than a torn
// newer one.
func (m *MemFormLog) RangePatches(from, upTo uint64, fn func(uint64, []byte) error) error {
	if from < 1 {
		from = 1
	}
	var records [][]byte
	if p := m.records.Load(); p != nil {
		records = *p
	}
	for i := from; i <= uint64(len(records)); i++ {
		if upTo > 0 && i > upTo {
			return nil
		}
		if err := fn(i, records[i-1]); err != nil {
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

func (l *xwalFormLog) RangePatches(from, upTo uint64, fn func(uint64, []byte) error) error {
	xw, err := l.backend.store.openNode(l.ariaID)
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
	if from > first {
		first = from // start AT the range, not at the beginning of history
	}
	if upTo > 0 && upTo < last {
		last = upTo
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

// FormLingerForTest and PatchWindowForTest: see HandleIdleForTest.
func FormLingerForTest() time.Duration { return time.Duration(formLinger.Load()) }

// PatchWindowForTest is the resident patch bound the writer trims against.
func PatchWindowForTest() int { return int(patchWindow.Load()) }
