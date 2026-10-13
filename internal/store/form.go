package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/actor"
	figOtel "github.com/jack-work/figaro/internal/otel"
)

// Form is an aria's state, and the only writer of the channel that holds it.
type Form struct {
	log   FormLog
	q     *actor.Lazy[*formWrite]
	state atomic.Pointer[formState]

	// tick is the broadcast, closed and replaced after every batch. Completion
	// is per submission (formWrite.done), never a watermark: tickets are handed
	// out before the queue is entered, so a high ticket can land in an earlier
	// batch than a low one.
	tick atomic.Pointer[chan struct{}]

	// sinks is an immutable slice behind a pointer, so emitting takes no lock.
	sinks atomic.Pointer[[]func(uint64, message.Patch)]
	// sealed is rebuilt from the published state at open, so a dead form stays
	// dead across a restart.
	sealed atomic.Bool
	subs   atomic.Pointer[[]*Subscription]
	closed atomic.Bool
}

// formState is one published version. patches are kept because the projection
// renders each at the record it landed on, which a folded snapshot cannot say.
type formState struct {
	snap    form.Snapshot
	version uint64
	patches []VersionedPatch
	// trimmed is the highest version dropped from patches, 0 if none. It is
	// explicit because an identity patch appends no record, so a gap below the
	// first kept patch is not a trim.
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

// Intent says what a removal means: the one place a command and its event may
// disagree about legality.
type Intent uint8

const (
	// Ensure: the caller wants the key absent, whether or not it was there.
	Ensure Intent = iota
	// Assert: removing a key that is not there is a refusal; the caller's model
	// of the world is wrong and telling it so is more useful than agreeing.
	Assert
)

type formResult struct {
	version uint64
	// applied is what the writer committed after reducing against the
	// published state: the keys that really changed.
	applied message.Patch
	err     error
}

// FormLog is the durable half: append-only patch payloads indexed from 1.
type FormLog interface {
	// AppendPatch returns the index the payload landed at.
	AppendPatch(payload []byte) (uint64, error)
	// SyncThrough runs between a batch's appends and its publish: one sync
	// covers the batch, durable before visible.
	SyncThrough(index uint64) error
	// RangePatches visits records in index order from `from` (1 or 0 is the
	// beginning) through `upTo` (0 is the end), starting at the range rather
	// than at record 1.
	RangePatches(from, upTo uint64, fn func(index uint64, payload []byte) error) error
}

// FormBaseLog has a floor and a base to start from below it: Floor says where
// answering stops, Base what to start from instead, so a reader below the
// floor is reseeded from memory rather than sent over the wire.
type FormBaseLog interface {
	FormLog
	Base() (form.Snapshot, uint64)
	Floor() uint64
}

// OpenForm replays the log (the cold cost); afterwards every read is an atomic load.
func OpenForm(log FormLog) (*Form, error) {
	f := &Form{log: log}
	st := &formState{}
	// A paged log is opened from its base, not record 1, which may no longer
	// exist; trimmed records that a read under the base is told no rather than
	// answered short.
	if bl, ok := log.(FormBaseLog); ok {
		st.snap, st.version = bl.Base()
		st.trimmed = st.version
	}
	if err := log.RangePatches(0, 0, func(index uint64, payload []byte) error {
		var p message.Patch
		if err := json.Unmarshal(payload, &p); err != nil {
			return err
		}
		st.snap = st.snap.Apply(p)
		st.version = index
		if !p.IsIdentity() {
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
var (
	patchWindow atomic.Int64
	patchSlack  = 256
)

func init() { patchWindow.Store(2048) }

// SetPatchWindow: zero or negative retains everything.
func SetPatchWindow(n int) { patchWindow.Store(int64(n)) }

// formLinger is package level so the daemon sets it once before any form
// opens; already-open forms keep the old value.
var formLinger atomic.Int64

func init() { formLinger.Store(int64(2 * time.Second)) }

// SetFormLinger: call before opening forms.
func SetFormLinger(d time.Duration) {
	if d < 0 {
		d = 0
	}
	formLinger.Store(int64(d))
}

// Snapshot is lock-free.
func (f *Form) Snapshot() (form.Snapshot, uint64) {
	st := f.state.Load()
	return st.snap, st.version
}

func (f *Form) Read() FormAt {
	st := f.state.Load()
	return FormAt{Snapshot: st.snap, Version: st.version}
}

type FormAt struct {
	Snapshot form.Snapshot
	Version  uint64
}

// PatchesBetween returns (after, upTo] as a view on the published array: no copy.
func (f *Form) PatchesBetween(after, upTo uint64) []VersionedPatch {
	st := f.state.Load()
	ps := st.patches
	// Only a trim sends a read to disk.
	if upTo > after && st.trimmed > 0 && after < st.trimmed {
		if fromLog, ok := f.patchesFromLog(after, upTo); ok {
			figOtel.RecordFormPatchRead(context.Background(), len(fromLog), len(ps))
			return fromLog
		}
		// The log could not answer, so neither can this: a short prefix from the
		// resident array reads as a complete answer at the call site and
		// surfaces later as a phantom gap. PatchFloor tells the caller why.
		return nil
	}
	out := patchRange(ps, after, upTo)
	figOtel.RecordFormPatchRead(context.Background(), len(out), len(ps))
	return out
}

// PatchFloor: a read at or under it is answered nil by PatchesBetween and the
// caller must reseed from Snapshot(). Zero means the whole history is
// available, which is every durable form.
func (f *Form) PatchFloor() uint64 {
	if bl, ok := f.log.(FormBaseLog); ok {
		return bl.Floor()
	}
	return 0
}

// Base is the oldest state this form can be reseeded from without the wire;
// for a durable form, the zero snapshot at version 0.
func (f *Form) Base() (form.Snapshot, uint64) {
	if bl, ok := f.log.(FormBaseLog); ok {
		return bl.Base()
	}
	return form.Snapshot{}, 0
}

// The cold path; allocates.
func (f *Form) patchesFromLog(after, upTo uint64) ([]VersionedPatch, bool) {
	var out []VersionedPatch
	err := f.log.RangePatches(after+1, upTo, func(index uint64, payload []byte) error {
		var p message.Patch
		if err := json.Unmarshal(payload, &p); err != nil {
			return err
		}
		if !p.IsIdentity() {
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

func (f *Form) Apply(patch message.Patch, ifVersion uint64) (uint64, error) {
	version, _, err := f.ApplyEffect(patch, ifVersion)
	return version, err
}

// ApplyEffect also returns what actually landed after the writer reduced the
// patch against the published state.
func (f *Form) ApplyEffect(patch message.Patch, ifVersion uint64) (uint64, message.Patch, error) {
	return f.ApplyEffectIntent(patch, ifVersion, Ensure)
}

// ApplyEffectIntent: under Assert a removal of a missing key is refused rather
// than reduced away.
func (f *Form) ApplyEffectIntent(patch message.Patch, ifVersion uint64, intent Intent) (uint64, message.Patch, error) {
	return f.applyEffect(patch, ifVersion, intent, false)
}

// ApplyEffectPrivileged may touch system-managed keys. There is no wire field
// for this and there must never be one: privilege is a property of the call site.
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

// Ticket: Await it or drop it, the write lands either way.
type Ticket struct{ w *formWrite }

// Submit queues without waiting: a caller that does not need the version
// never parks a goroutine.
func (f *Form) Submit(patch message.Patch, ifVersion uint64, intent Intent) (Ticket, error) {
	return f.submit(&formWrite{patch: patch, ifVersion: ifVersion, intent: intent})
}

// submit: privilege never reaches the public Submit; a caller that may write
// harness keys says so by call site, not by argument.
func (f *Form) submit(w *formWrite) (Ticket, error) {
	if err := f.q.Submit(w); err != nil {
		return Ticket{}, ErrFormClosed
	}
	return Ticket{w: w}, nil
}

// Await parks the caller's own goroutine; one broadcast serves every waiter,
// no goroutine or channel per call.
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

// OnCommit sinks run after the append and the publish, never before, so an
// observer never sees state that would not survive a restart.
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

// Close refuses further writes rather than dropping them. Idempotent:
// eviction and a delete can both reach the same form.
func (f *Form) Close() {
	f.closed.Store(true)
	f.q.Close()
}

// runBatch: reduce each submission against a running state, append, one sync
// for the batch, publish, then answer and emit.
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
		// Records, not writes: a batch in which one patch changed anything
		// writes one record, and the instrument must count what the sync covered.
		figOtel.RecordSync(context.Background(), time.Since(started), len(events))
		if err != nil {
			// Without durability the writes did not happen: every write in the
			// batch is rejected and the state stands.
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
	// Emit before answering: a caller returning from Apply may assume the delta
	// reached the sinks. A sink must hand off and return.
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
		for _, ent := range w.patch.Entries() {
			if !ent.IsRemoval() {
				continue
			}
			k := ent.Key
			if !st.snap.Has(k) {
				return nil, formResult{version: st.version,
					err: fmt.Errorf("remove %q: no such key", k)}
			}
		}
	}
	// Reduce first, purely: a patch is only an event if it changes something,
	// and a failure has nothing to roll back.
	applied := effectivePatch(st.snap, w.patch)
	if applied.IsIdentity() {
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
	// Append to the shared backing array: safe with exactly one drainer, since
	// a published state holds its own slice length and a later append either
	// writes past it or reallocates.
	next.patches, next.trimmed = trimPatches(append(st.patches, VersionedPatch{Version: version, Patch: applied}), st.trimmed)
	return next, formResult{version: version, applied: applied}
}

func (f *Form) answer(batch []*formWrite) {
	for _, w := range batch {
		w.done.Store(true)
	}
	next := make(chan struct{})
	old := f.tick.Swap(&next)
	close(*old)
}

// emit runs after the publish, never before; a sink that panics must not take
// the drainer with it.
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

// effectivePatch: the keys a patch changes and the removals that remove
// something. Every place that dresses a board must agree about what "already
// wearing it" means.
func effectivePatch(snap form.Snapshot, p message.Patch) message.Patch {
	// Apply and re-diff keeps exactly what changed; a removal of something
	// absent falls out on its own.
	return snap.Apply(p).Diff(snap)
}

// memFormPage is the floor in raw records, one tier under the decoded window
// patchWindow bounds; read the two together.
var memFormPage atomic.Int64

func init() { memFormPage.Store(512) }

// SetMemFormPage: zero or negative retains everything.
func SetMemFormPage(n int) { memFormPage.Store(int64(n)) }

// memPage answers for any version above its base without consulting anything else.
type memPage struct {
	base        form.Snapshot // the state as of baseVersion
	baseVersion uint64
	patches     [][]byte // absolute versions baseVersion+1 .. baseVersion+len
}

func (p *memPage) top() uint64 { return p.baseVersion + uint64(len(p.patches)) }

// memLogState is published as one immutable value so a reader takes no lock
// and never sees a half-rotated log.
type memLogState struct {
	// sealed is the predecessor, kept until live alone satisfies the floor.
	sealed *memPage
	live   *memPage
}

// floorVersion: a read at or below it is answered from a base snapshot instead.
func (s *memLogState) floorVersion() uint64 {
	if s.sealed != nil {
		return s.sealed.baseVersion
	}
	return s.live.baseVersion
}

// MemFormLog holds a form's records in memory and is paged: appends land on
// the live page; when it fills, the state is snapshot and a fresh page opens
// on it; the predecessor is held until the live page alone satisfies the
// floor. Resident history is always in [page, 2*page) records plus two
// snapshots, and a reader that falls off the end is reseeded from the base
// rather than sent over the wire.
//
// Single writer by construction (AppendPatch is reached only from runBatch,
// the actor's one drainer); readers are not.
type MemFormLog struct {
	state atomic.Pointer[memLogState]

	// writing detects a second concurrent writer: the lock removal rests on
	// the single-writer contract, so a break must be reported.
	writing atomic.Bool

	// testHold parks a writer inside the critical region so a test can overlap
	// two appends; nil in production.
	testHold func()
}

// ErrBelowFloor: a range starting under the floor cannot be served from
// patches, and a short answer would read as a legitimate gap. Base() is the cure.
var ErrBelowFloor = fmt.Errorf("form log: the requested range starts below the retained floor")

func (m *MemFormLog) load() *memLogState {
	if st := m.state.Load(); st != nil {
		return st
	}
	return &memLogState{live: &memPage{}}
}

// Base: a reader below Floor() starts here and replays forward.
func (m *MemFormLog) Base() (form.Snapshot, uint64) {
	st := m.load()
	if st.sealed != nil {
		return st.sealed.base, st.sealed.baseVersion
	}
	return st.live.base, st.live.baseVersion
}

// Floor is the oldest version a patch is still held for.
func (m *MemFormLog) Floor() uint64 { return m.load().floorVersion() }

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
	old := m.load()

	live := &memPage{
		base:        old.live.base,
		baseVersion: old.live.baseVersion,
		patches:     append(append(make([][]byte, 0, len(old.live.patches)+1), old.live.patches...), append([]byte(nil), payload...)),
	}
	next := &memLogState{sealed: old.sealed, live: live}

	// Rotate: the predecessor is dropped only when the page replacing it can
	// carry the floor on its own.
	if page := int(memFormPage.Load()); page > 0 && len(live.patches) >= page {
		snap, err := replayPage(live)
		if err != nil {
			return 0, err
		}
		next = &memLogState{
			sealed: live,
			live:   &memPage{base: snap, baseVersion: live.top()},
		}
	}
	m.state.Store(next)
	return live.top(), nil
}

// replayPage runs once per rotation: the amortized cost of the bound.
func replayPage(p *memPage) (form.Snapshot, error) {
	snap := p.base
	for _, payload := range p.patches {
		var patch message.Patch
		if err := json.Unmarshal(payload, &patch); err != nil {
			return form.Snapshot{}, fmt.Errorf("mem form log: replay page: %w", err)
		}
		snap = snap.Apply(patch)
	}
	return snap, nil
}

// SyncThrough is a no-op: memory is as durable as this log gets.
func (m *MemFormLog) SyncThrough(uint64) error { return nil }

// RangePatches takes no lock: it walks one published state, and a concurrent
// append publishes a successor this call does not see, a consistent older
// history rather than a torn newer one. Below the floor it returns
// ErrBelowFloor rather than answering short.
func (m *MemFormLog) RangePatches(from, upTo uint64, fn func(uint64, []byte) error) error {
	st := m.load()
	floor := st.floorVersion()
	if from < 1 {
		from = floor + 1
	}
	if from <= floor {
		return ErrBelowFloor
	}
	for _, p := range []*memPage{st.sealed, st.live} {
		if p == nil {
			continue
		}
		for i, payload := range p.patches {
			v := p.baseVersion + uint64(i) + 1
			if v < from {
				continue
			}
			if upTo > 0 && v > upTo {
				return nil
			}
			if err := fn(v, payload); err != nil {
				return err
			}
		}
	}
	return nil
}

func NewMemForm() *Form {
	f, err := OpenForm(&MemFormLog{})
	if err != nil {
		panic(err) // a memory log cannot fail to replay nothing
	}
	return f
}

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
	first, last, _ := xw.ChannelBounds(chanForm)
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

func FormLingerForTest() time.Duration { return time.Duration(formLinger.Load()) }

func PatchWindowForTest() int { return int(patchWindow.Load()) }
