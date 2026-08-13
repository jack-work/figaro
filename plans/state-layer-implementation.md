# The state layer: implementation

Companion to [durable-forms.md](durable-forms.md), which says what and why.
This says **how**, at the level of types and function bodies, and lays out
the work.

Read the design doc first. Open questions live in
`~/notes/figaro/form-work/`, numbered, and are referenced inline as
`[q01]`.

---

# Part I: how the work is laid out

## Branches and worktrees

**One worktree, one feature branch**: `/home/gluck/dev/figaro-qua/incant`,
`feat/state-layer`, off main. Everything lands there and Gluck validates it
end to end before merge.

**Three exceptions, each genuinely divisible:**

1. **figwal is a different repository.** `SyncChannelThrough`, the compacting
   channel option, and any crash-test additions land in
   `/home/gluck/dev/figwal`, get released, and are pinned by version. That is
   a stack whether or not we want one.
2. **The instruments** (`[q04]`): mutex/block profiling, an fsync counter, a
   patch-latency histogram. An hour of work that makes every later claim
   checkable, so it merges to main first and the refactor rebases on it.
3. **`feat/incantations`** is finished and validated and should land before
   this starts (`[q05]`), or we are validating two things at once.

Phases below are commits on the one branch, not branches. A phase is a
commit when it builds, passes, and leaves the tree honest.

## The phases

**STATUS as of 2026-08-12** (see `progress.md` for commits): phases 0, 1, 2
and 5 are DONE. Phase 3 is partial (intent landed end to end; command,
event, ack, session and seq not started). Phase 4 not started. Phases 6
to 10 not started.

| # | phase | red | green | independently useful |
|---|---|---|---|---|
| 0 | figwal: `SyncChannelThrough` | 0 | ~60 | yes |
| 1 | `internal/actor`: the lazy queue | ~40 | ~260 | yes |
| 2 | `store.Form` on the actor, group commit, fsync | ~120 | ~220 | **yes, the headline** |
| 3 | command / event / ack, intent, session+seq | ~90 | ~300 | yes |
| 4 | schema validation in the writer | ~10 | ~220 | yes |
| 5 | `SubscribeFrom`, the event stream, `OnCommit` dies | ~130 | ~240 | yes |
| 6 | tombstones and leases | ~40 | ~320 | no (needs 5) |
| 7 | figwal: compacting channel + figaro plumbing | ~20 | ~180 | no |
| 8 | the topology form; `internal/trunk` dies | ~420 | ~260 | no |
| 9 | derived forms, the libretto, `study` as alias | ~600 | ~450 | no |
| 10 | the API refactor and `angelus.hello` | ~350 | ~400 | yes |

Rough, and the red columns are the point: phases 8 and 9 delete more than
they add, which is the shape of the whole changeset.

**Phases 0 to 5 are worth having even if 6 to 10 never happen.** That is the
property to protect when the estimate is wrong.

---

# Part II: the code

## 1. `internal/actor`: the lazy queue

The existing `actor.Start` spawns a goroutine that lives forever. The
replacement spawns on demand and dies when idle. Everything else in this
plan is built on it.

```go
// Package actor: a single-writer queue whose goroutine exists only while
// there is work.
//
// The eager version cost one parked goroutine per open object, and objects
// open on sight: a listing reads a form per row, so the daemon held one
// goroutine per aria anyone had LOOKED at. 415 goroutines for 40 arias.
//
// This is not a worker pool. There is exactly one drainer per queue, which
// is what makes the queue a serialization point rather than a buffer.
package actor

type state uint32

const (
	idle state = iota
	running
	lingering
)

// Queue serializes work of type T through one drainer at a time.
type Queue[T any] struct {
	// pending is the inbox. The mutex guards the SLICE, never the work: it
	// is held for an append or a slice header swap and nothing else. An
	// actor that held its inbox lock across the work would be a mutex with
	// extra steps, which is the thing this replaces.
	pending struct {
		sync.Mutex
		items  []T
		closed bool
	}

	st     atomic.Uint32
	wake   chan struct{} // 1-buffered: a signal, never a queue
	linger time.Duration
	batch  int
	drain  func([]T) // runs on the drainer; must not block on a submitter
}

func New[T any](batch int, linger time.Duration, drain func([]T)) *Queue[T] {
	return &Queue[T]{wake: make(chan struct{}, 1), linger: linger, batch: batch, drain: drain}
}

// Submit enqueues and ensures a drainer exists. It never waits for the work.
func (q *Queue[T]) Submit(v T) error {
	q.pending.Lock()
	if q.pending.closed {
		q.pending.Unlock()
		return ErrClosed
	}
	q.pending.items = append(q.pending.items, v)
	q.pending.Unlock()
	q.ensureRunning()
	return nil
}

func (q *Queue[T]) ensureRunning() {
	for {
		switch state(q.st.Load()) {
		case running:
			return // the drainer has not left; it will see the item
		case lingering:
			if q.st.CompareAndSwap(uint32(lingering), uint32(running)) {
				select {
				case q.wake <- struct{}{}:
				default: // a signal is already pending; one is enough
				}
				return
			}
		case idle:
			if q.st.CompareAndSwap(uint32(idle), uint32(running)) {
				go q.work()
				return
			}
		}
	}
}

func (q *Queue[T]) take(n int) []T {
	q.pending.Lock()
	defer q.pending.Unlock()
	if len(q.pending.items) == 0 {
		return nil
	}
	if n <= 0 || n > len(q.pending.items) {
		n = len(q.pending.items)
	}
	out := q.pending.items[:n:n]
	q.pending.items = q.pending.items[n:]
	return out
}

func (q *Queue[T]) work() {
	timer := time.NewTimer(q.linger)
	defer timer.Stop()

	for {
		for {
			batch := q.take(q.batch)
			if len(batch) == 0 {
				break
			}
			q.drain(batch)
		}

		// Drained. Offer to leave.
		if !q.st.CompareAndSwap(uint32(running), uint32(lingering)) {
			continue // somebody moved us; look again
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(q.linger)

		select {
		case <-q.wake:
			continue // Submit already set running

		case <-timer.C:
			if !q.st.CompareAndSwap(uint32(lingering), uint32(idle)) {
				continue // a Submit beat us back to running
			}
			// THE EXIT RACE, and the only subtle thing in this file.
			//
			// A Submit can land between take() returning empty and the CAS
			// above. That Submit saw `lingering` or `running`, so it did NOT
			// spawn, and we are about to return. Re-check AFTER declaring
			// ourselves idle: anything enqueued before our CAS is visible
			// now, and anything enqueued after it will spawn its own
			// drainer. Miss this and a patch sits in the inbox with nobody
			// to run it, which presents as a hung `fig set` that unhangs on
			// the next unrelated write.
			if q.hasWork() && q.st.CompareAndSwap(uint32(idle), uint32(running)) {
				continue
			}
			return
		}
	}
}

func (q *Queue[T]) hasWork() bool {
	q.pending.Lock()
	defer q.pending.Unlock()
	return len(q.pending.items) > 0
}
```

**Why the pending mutex is justified**: it guards the slice, never the work.
Held for an append or a header swap, nanoseconds, never across I/O. The rule
from Part III applies: this is a registry of items, not the state of an
object.

**Tests that must exist before this is used** (§ Part IV):
`TestExitRaceLosesNothing` (hammer submit against a 0-linger queue),
`TestOneDrainerAtATime` (a drain function that panics if re-entered),
`FuzzQueueInterleavings`, and a `-race` soak.

## 2. `store.Form` on the queue

### 2.1 Types

```go
// Intent says what a removal MEANS, which is the only place a command and
// its event can disagree about legality.
type Intent uint8

const (
	// Assert: the caller believes the key is there. Removing one that is
	// not is a refusal, because the caller's model of the world is wrong
	// and it should be told.
	Assert Intent = iota
	// Ensure: the caller wants the key absent and does not care whether it
	// was. Birth dressing (-D) means this: "do not inherit that", about a
	// parent closure that may or may not hold it.
	Ensure
)

type Outcome uint8

const (
	Applied   Outcome = iota // a record was written
	Unchanged                // legal, and it changed nothing
	Refused                  // ifVersion, schema, intent, or a sealed form
)

// Command is what a caller asks for. It may be refused.
type Command struct {
	Patch     message.Patch
	IfVersion uint64 // 0: unconditional
	Intent    Intent
	Session   string // per connection; "" for in-process callers
	Seq       uint64 // monotonic within a session; 0 when Session is ""
	Priv      bool   // set ONLY by in-process callers; never decoded off a wire

	ticket Ticket
	result Result // filled by the drainer before the ticket advances
}

// Result is what happened. Written once, read after the ticket advances.
type Result struct {
	Outcome Outcome
	Version uint64        // the record index; unchanged for Unchanged/Refused
	Applied message.Patch // the EVENT: what actually landed
	Err     error
}

// Event is what subscribers see. Only Applied commands produce one.
type Event struct {
	Version uint64
	Applied message.Patch
	Session string // echoed so an optimistic client can retire its pending
	Seq     uint64
}
```

`Priv` is a struct field and not a wire field on purpose: privilege is a
property of the call site, checkable by grep and by the compiler, and there
is no JSON tag for it. `[q09]`

### 2.2 The form

```go
type Form struct {
	log FormLog
	q   *actor.Queue[*op]

	// state is the published value. One load to read; only the drainer
	// stores. No mutex: publication is a pointer swap and readers hold no
	// lock at all.
	state atomic.Pointer[formState]

	// ticket advances once per COMMAND, including no-ops and refusals.
	// version advances once per RECORD. They are different counters and
	// conflating them hangs Await on a no-op. [q02]
	ticket atomic.Uint64
	tick   atomic.Pointer[chan struct{}] // broadcast: closed and replaced

	// subs is drainer-owned. Registration is an op, so nothing else ever
	// touches this and it needs no lock.
	subs []*subscription

	validate Validator
	sealed   bool // drainer-owned; set by a tombstone
}
```

Every field is either atomic, or owned by the drainer. **There is no mutex on
Form.**

### 2.3 The op, because subscribe is also a command

```go
type opKind uint8

const (
	opPatch opKind = iota
	opSubscribe
	opUnsubscribe
)

type op struct {
	kind opKind
	cmd  *Command
	sub  *subscribeOp
}

// subscribeOp asks for a snapshot AND a stream from the same point.
// It goes through the queue rather than being a direct call because
// registration takes a durable lease, and a durable registration is a write,
// and a write needs the writer. [design §6]
type subscribeOp struct {
	after  uint64
	holder Lease
	buffer int

	ready chan struct{} // closed by the drainer when the fields below are set
	snap  form.Snapshot
	at    uint64
	ch    chan Event
	err   error
}
```

### 2.4 The drain

```go
// runBatch is the whole protocol: reduce each in turn against a RUNNING
// state, append, one fsync for the batch, publish, then answer and emit.
//
// Batching is for DURABILITY, never for semantics. Each command is reduced
// against the state as of its own position, or two writers' IfVersion
// guards stop meaning anything: the first must win and the second must see
// the moved version and be refused, and a merged patch cannot express that.
func (f *Form) runBatch(ops []*op) {
	published := f.state.Load()
	var lastRecord uint64
	events := make([]Event, 0, len(ops))

	for _, o := range ops {
		switch o.kind {
		case opSubscribe:
			f.serviceSubscribe(o.sub, published)
			continue
		case opUnsubscribe:
			f.serviceUnsubscribe(o.sub)
			continue
		}

		c := o.cmd
		next, ev, res := f.reduceOne(published, c)
		c.result = res
		if next != nil {
			published = next
			lastRecord = next.version
			events = append(events, ev)
		}
	}

	if lastRecord != 0 {
		if err := f.log.SyncThrough(lastRecord); err != nil {
			// NOTHING has been published. The records may or may not reach
			// disk later, so the honest answer is that this form's disk is
			// gone: seal it, fail the batch, and let the next OpenForm
			// recover from whatever is durable. [q01]
			f.poison(ops, err)
			return
		}
		f.state.Store(published)
	}

	f.emit(events)      // subscribers, after publish, never before
	f.answer(ops)       // tickets advance, waiters wake
}

// reduceOne is pure with respect to everything except the log.
func (f *Form) reduceOne(st *formState, c *Command) (*formState, Event, Result) {
	if f.sealed {
		return nil, Event{}, Result{Outcome: Refused, Err: ErrSealed}
	}
	if c.IfVersion != 0 && st.version != c.IfVersion {
		return nil, Event{}, Result{Outcome: Refused, Version: st.version,
			Err: fmt.Errorf("form moved: at version %d, not %d", st.version, c.IfVersion)}
	}
	// VALIDATION BEFORE REDUCTION. The reducer answers "does this change
	// anything"; it must never answer "is this allowed". Schema, reserved
	// keys and Assert-removals are refusals and belong here, in the one
	// place a check is atomic with the append.
	if err := f.validate.Check(st.snap, c); err != nil {
		return nil, Event{}, Result{Outcome: Refused, Version: st.version, Err: err}
	}

	applied := effectivePatch(st.snap, c.Patch)
	if applied.IsEmpty() {
		// Legal, and it changed nothing. No record, no version, no event,
		// and an ACK all the same: silence is not a legal answer to a
		// command. [design §4.1]
		return nil, Event{}, Result{Outcome: Unchanged, Version: st.version, Applied: applied}
	}

	payload, err := json.Marshal(applied)
	if err != nil {
		return nil, Event{}, Result{Outcome: Refused, Err: err}
	}
	version, err := f.log.AppendPatch(payload)
	if err != nil {
		return nil, Event{}, Result{Outcome: Refused, Err: err}
	}

	next := &formState{
		snap:    st.snap.Apply(applied),
		version: version,
		patches: append(st.patches, VersionedPatch{Version: version, Patch: applied}),
	}
	ev := Event{Version: version, Applied: applied, Session: c.Session, Seq: c.Seq}
	return next, ev, Result{Outcome: Applied, Version: version, Applied: applied}
}
```

Note what did **not** change: the order is still reduce, append, apply,
publish. The fsync slots between append and apply, and because the reduce is
pure and nothing is applied before it, **a failure at any point leaves the
published state untouched and there is nothing to roll back**.

### 2.5 Ticket, await, broadcast

```go
func (f *Form) Submit(c *Command) (Ticket, error) {
	c.ticket = Ticket(f.nextTicket.Add(1))
	if err := f.q.Submit(&op{kind: opPatch, cmd: c}); err != nil {
		return 0, err
	}
	return c.ticket, nil
}

// Await blocks the CALLER'S OWN goroutine until the command has been
// answered. No goroutine per call and no channel per call: one broadcast
// serves every waiter.
func (f *Form) Await(ctx context.Context, c *Command) (Result, error) {
	for {
		tick := *f.tick.Load() // read the tick BEFORE the check, or miss a wakeup
		if f.ticket.Load() >= uint64(c.ticket) {
			return c.result, c.result.Err // safe: released by answer()
		}
		select {
		case <-tick:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
}

// answer publishes the results of a batch and wakes every waiter once.
func (f *Form) answer(ops []*op) {
	var high uint64
	for _, o := range ops {
		if o.kind == opPatch && uint64(o.cmd.ticket) > high {
			high = uint64(o.cmd.ticket)
		}
	}
	if high == 0 {
		return
	}
	f.ticket.Store(high) // release: c.result is visible to any acquiring reader
	next := make(chan struct{})
	old := f.tick.Swap(&next)
	close(*old)
}
```

### 2.6 Emission

```go
// emit hands each subscriber its events. The drainer must NEVER block on a
// consumer, so a full buffer drops and records the gap: a mirror that
// misses one patch is wrong forever unless it is told to resync.
func (f *Form) emit(events []Event) {
	if len(events) == 0 {
		return
	}
	for _, s := range f.subs {
		for _, ev := range events {
			select {
			case s.ch <- ev:
				s.at = ev.Version
			default:
				s.missed++
			}
		}
		if s.missed > 0 {
			select {
			case s.ch <- Event{Version: s.at, Applied: resyncMarker(s.missed)}:
				s.missed = 0
			default:
			}
		}
	}
}
```

`recover` goes around this loop, because since main moved sinks off the
actor goroutine a panicking consumer takes down whoever called `fig set`.
Two lines, and worth landing on its own before any of this.

## 3. The topology form

Same primitive, custom API, no patch views, compaction allowed.

```go
// Topology is the presentation hierarchy: what `fig ls` draws and what a
// delete takes. It is a form with three verbs instead of `patch`, because
// every change must be VALIDATED against the drawn tree and emitted as ONE
// record, and a caller holding `patch` could write half a promote.
type Topology struct {
	f *Form // never exposed; Patches() is not reachable from here
}

func (t *Topology) Promote(ctx context.Context, id string) error {
	return t.f.Do(ctx, func(st *formState) (message.Patch, error) {
		tree := treeOf(st.snap)
		up, ok := tree.Parent(id)
		if !ok {
			return message.Patch{}, fmt.Errorf("promote: %s is a root", id)
		}
		grand, ok := tree.Parent(up)
		if !ok || !isConversation(grand) {
			// Only conversations nest. An outfit stump or the genesis root
			// is structure, and hanging one under a conversation would put
			// every aria in the store inside one aria's subtree.
			return message.Patch{}, ErrAtStump
		}
		// ONE patch, two keys. Today this is one save() of a whole file and
		// the pair can half-land.
		return message.Patch{Set: map[string]json.RawMessage{
			"parent." + id: raw(grand),
			"parent." + up: raw(id),
		}}, nil
	})
}
```

`Form.Do` is the shape that makes validation atomic with the write:

```go
// Do runs a decision INSIDE the drainer and patches with whatever it
// returns. The decision sees the state the patch will be applied to, so
// there is no window between deciding and writing.
//
// This is what retires the TOCTOU in promote, which today reads the
// topology, decides, and writes overrides in three separate steps.
func (f *Form) Do(ctx context.Context, decide func(*formState) (message.Patch, error)) error
```

## 4. Derived forms

```go
// Derivation is a compound: two forms, two concurrency domains.
//
// The SPEC is user intent (which forms, which fields). The VALUES are
// machine output. Different writers, different lifecycles, different
// protection classes. One form would mean two writers on one node and the
// whole model collapses.
type Derivation struct {
	spec   *Form // {"@abc": {"brief": "this"}}
	values *Form // {"@abc": {"brief": "ship it"}}
	q      *actor.Queue[derivedOp]
	feeds  map[string]*feed // form id -> its subscription and cursor
}

// fold applies one source event to the derived values by TREE SURGERY, not
// by marshalling. form.Snapshot is an immutable AVL where Clone is the
// identity, and a presence-only derivation is literally a subtree
// selection, so the derived tree can share structure with the source.
//
// Writing this as marshal-and-unmarshal would triple the allocations and be
// very hard to undo later, so it is a stated goal from the first line.
func (d *Derivation) fold(src string, ev Event) message.Patch
```

`study` becomes:

```go
func Study(ctx context.Context, lib *Form, formID string) error {
	return lib.Apply(ctx, &Command{
		Patch:  message.Patch{Set: map[string]json.RawMessage{formID: rawThis}},
		Intent: Assert,
	})
}
```

and `drop` is the same with `Remove`, under `Assert`, so dropping something
you do not study is a refusal rather than a silent success. That deletes
`study.go`'s board list, `study_hub.go` entirely, and `SetObservedForms`.

---

# Part III: the lock audit

**The rule, stated so it can be applied without me:**

> A mutex is justified when it guards a **registry** (a map from id to
> object, read by many, mutated rarely). It is not justified when it guards
> **state** (the value of one object). State belongs to an actor.
>
> And no lock is ever held across I/O or across anything that can block.

Twenty-five mutexes in the packages this touches. By that rule:

### Dies

| lock | why |
|---|---|
| `store/form.go: write` | becomes the inbox. The headline. |
| `store/form.go: mu` (onCommit) | the subscriber list becomes drainer-owned |
| `store/xwal_store.go: observedMu` | the whole mirror dies with the libretto |
| `store/xwal_store.go: keepMu` | it guards ONE STRING with a documented lock-order hazard. `atomic.Pointer[string]` and the hazard is gone |
| `internal/trunk: mu` | dies with `trunks.json` |
| `store/form.go: MemFormLog.mu` | survives only because it is a test double; if it grows a second user it becomes an actor |

### Shrinks, with justification

| lock | after |
|---|---|
| `store/xwal_store.go: deleting` | the topology half moves into the topology actor; the FILESYSTEM half (detach, unlink) still needs serializing, because it is not a form write. Reduced scope, documented at the site. |
| `store/cached_log.go: mu` | should become `atomic.Pointer` to an immutable rows slice, the same publish-a-snapshot pattern `formState` uses. It is on the hot read path and it is read-mostly. Worth doing in phase 2, not required by it. |

### Justified as registries, kept

`xwal_store.mu` (the figwal handle and topology snapshot), `xwal_backend.mu`
(forms, logs, translations by aria), `angelus/hub.mu`, `hubs.mu`,
`registry.mu`, `protocol.configMu`. Each guards a map, not a value.

### Out of scope, flagged

`figaro/agent.go: mu` (RWMutex over turnCtx, subs, live state) is agent
state that should be actor-owned, and `protocol.restoreLocks` is a per-aria
mutex map that is an actor per aria wearing a disguise. Both are real, both
are large, and neither is required by this plan. Note them; do not open
them here.

---

# Part IV: validation

## What exists and is reusable

| harness | where | what it proves |
|---|---|---|
| form race repro | `internal/figaro/form_race_test.go` | unsynchronized publication, under `-race` |
| form fuzz | `internal/figaro/fuzz_form_test.go` | patch algebra |
| `formstress.sh` | `scripts/` | real daemon, real socket, disjoint writer ranges, no lost updates, **state equality across a restart**, `STRESS_RACE=1` builds the daemon with `-race` |
| `ariastress.sh` | `scripts/` (mine) | N arias, one daemon, PSS/`Pss_Anon`/swap, daemon census, pprof capture |
| twelve-aria recipe | `skills/tmux-testing.md` | concurrency through one daemon in real ptys |
| real-store probe | `internal/angelus/realaria_probe_test.go`, `internal/store/realform_probe_test.go` | real shape, real boards, opens a COPY |
| perf fixture | `internal/store/perf_fixture_test.go` | synthetic stores at scale |
| **figwal crashtest** | `figwal/crashtest/` | a child process killed at a random delay, with a model asserting `durable-lost`, `append-lost`, `checksum`, `prefix-mismatch` |

That last one is the important find: **figwal already has the crash harness
this plan needs**, and `durable-lost` is literally the invariant
sync-before-publish adds. The figaro-side test is that harness's shape
pointed at a form.

## What must be built

**Unit and property**

- `TestExitRaceLosesNothing`: 0-linger queue, N submitters, assert every
  submission is drained. The unique failure mode of the lazy actor.
- `TestOneDrainerAtATime`: a drain function that panics if re-entered.
- `FuzzQueueInterleavings`: submit/drain/linger/exit orders.
- `TestBatchPreservesIfVersion`: two CAS writers in ONE batch behave exactly
  as they do in two batches.
- `TestUnchangedIsAcknowledged`: a no-op advances the ticket and wakes
  `Await`. The bug this plan exists to prevent for optimistic clients.
- `TestAssertRemovalRefused` / `TestEnsureRemovalReduces`.
- **`TestColdEqualsWarmProjection`**: still missing since August
  (`form-projection-followups.md` §2), and now load-bearing twice over.
- `FuzzSchemaValidation`: arbitrary patches against the key table, assert no
  panic and that refusal never mutates.

**Crash**

- `crashtest`-shaped: a child writes patches and is killed at a random
  delay; the parent asserts **no acknowledged patch is missing** and **no
  unacknowledged patch is visible**. Sync-before-publish is exactly the
  claim this checks, and without it the claim is a comment.

**Replica conformance**

- A simulated optimistic client: apply locally, receive events and refusals,
  assert it converges to the server's state for every interleaving. Runs in
  memory, no daemon, and it is the executable form of §4.3.

**Stress, in the nix devshell**

- `formstress.sh` unchanged, plus `STRESS_RACE=1`, before and after every
  phase touching the writer.
- `ariastress.sh --study --study-patches 5000`, 12 arias, PSS and swap.
- A new `topostress.sh`: N concurrent promotes and deletes against one
  store, asserting the drawn tree is a tree (no cycles, no orphans) after
  every operation.

**UI, in tmux ptys**

Only phases 8 and 10 touch what a human sees (`fig ls` while attending, the
verb surface). The twelve-aria recipe plus targeted pty runs of `fig ls`,
`fig form listen` (which phase 5 changes the semantics of), and `fig
promote`.

## The profiling stack

**Have**: pprof over a unix socket (`FIGARO_PPROF=1`, 0600), `doctor mem -j`
(`HeapAlloc`, `HeapInuse`, `Goroutines`, `ResidentArias`, `ResidentIRBytes`),
benchstat with a do-nothing control in every table, PSS/`Pss_Anon`/swap
discipline, a daemon census with a peer check before reaping, otel traces
and one metric.

**Missing, and needed for this specifically** `[q04]`:

1. **Mutex and block profiling are compiled in but never enabled.** Nothing
   calls `runtime.SetMutexProfileFraction` or `SetBlockProfileRate`, so
   `/debug/pprof/mutex` and `/block` return nothing. For a change whose
   thesis is contention, that is the missing instrument.
2. **An fsync counter.** The number that proves group commit works
   (fsyncs per patch) does not exist.
3. **A patch latency histogram.** Benchmarks are a lab; nothing reports what
   `fig set` costs under real load.

With those three, the claim "contended throughput improved and solo latency
regressed by X" is checkable rather than argued.

---

# Part V: documentation

**Updated at the phase that changes them**, not at the end:

| phase | doc |
|---|---|
| 2 | `contributing/forms-design.md` §3 (the single writer), the `store.Form` type comment |
| 3 | new `reference/wire.md`: command, event, ack, intent, session/seq |
| 4 | `reference/forms.md` (validation), `known_keys.go` prose |
| 5 | `reference/forms.md` (`form listen` semantics), `forms-design.md` §8 |
| 6 | `forms-design.md` (deletion, leases), `cli.md` (`node.delete`) |
| 8 | `reference/trunks.md`, `contributing/trunk-singleton-form.md` marked BUILT, `trunks-substrate.md` |
| 9 | `reference/forms.md` (study is an alias), `roles-design.md`, `forms-design.md` §8b |
| 10 | `reference/wire.md` completed, `cli.md` generated |

**Intermediate notes are left in place** where a phase changes something
without finishing it: a `NOTE (phase N):` block in the doc that says what is
true now and what is coming. The rule is that no doc may describe a design
the code does not have, and a doc describing a design the code has only
partly must say which part.

`plans/durable-forms.md` and this file are the record of intent and are
updated as rulings land, not rewritten per phase.
