# The state layer: one primitive, one durability rule, one protocol

**Status: designed, not built.** This supersedes the first draft of this file
and folds in
[contributing/trunk-singleton-form.md](../skills/figaro/contributing/trunk-singleton-form.md),
whose document shape, reserved node and migration survive intact and whose
concurrency section this replaces.

## The narrative

Figaro has six durability stories where it should have one.

The form primitive (a reducible WAL channel with a single writer) is the good
one. Beside it: `trunks.json` with a hand-rolled atomic-rename `save()`; the
study list, a JSON array on a board, mirrored into an in-memory `observed`
map that nothing persists; the translator cache, durable but with its own
invalidation rules; the IR log, which is a log and should stay one; and an
outfit/default-form lifecycle with a bespoke dirty flag.

Every one of those wants the same four things the form already has: durable,
versioned, reducible, one writer. They differ only in their API and their
validation.

**So: collapse them onto the primitive.** One mechanism with one durability
rule, one subscription rule, one deletion rule and one protection rule. The
libretto, the topology hierarchy, derived state, optimistic clients and
eventual distribution all become *policies on top of it* rather than
mechanisms beside it.

That is the changeset. Everything below is detail.

## 1. Base principles

1. **One writer per form.** An inbox with exactly one drainer. Not a mutex,
   not a convention.
2. **Durable before visible.** A patch is synced to disk before it reaches
   the published state. The reverse is not a lost write but a hallucinated
   one: the model is shown state as a reminder, so a crash would leave it
   acting on something that never happened.
3. **Reads are lock-free.** The live state is an immutable AVL root behind an
   atomic pointer. A read is one load. It never blocks a writer, never wakes
   anything, never serializes behind a turn.
4. **Command and event are different things.** A command may be refused. An
   event is a fact and can only be observed. The reduction from one to the
   other is pure and happens in the writer.
5. **Every command receives an answer.** Silence is not a legal outcome, even
   when the log gets nothing.
6. **Absence is the truthful default.** State describing other state holds
   overrides, never a full picture, so a lost document degrades to the truth
   rather than to a lie.
7. **Forking never consults presentation.** A presentation edge must never
   decide where data comes from.
8. **Deletion is a record, not an event in memory.** A subscriber that was
   offline must be able to learn it; a replay must reproduce it.
9. **History is observable unless the form declares otherwise**, and a form
   that declares otherwise may not hand out views of it.

## 2. What a delete does to the log

The operation that normalizes the log's arrangement. Nothing else does.

**The arrangement.** A forked node does not own its whole history. Its
records `[1, base)` live in its ancestor's segment files: `.from` names the
parent, a per-channel `.fork` gives the base above which the node owns its
own. That is why a fork is cheap and why a family shares one rendered prefix.

**The repair, in order** (`XwalStore.RemoveLeaf`):

1. **Refuse first**, before touching anything, because the repair below
   rewrites surviving arias and cannot be taken back.
2. **Find the boundary**: survivors outside the delete set whose lineage runs
   through it (`topo.Boundary`).
3. **Detach each** (`Trunks.Detach`): copy `[1, base)` out of the ancestor
   chain into the node's own directory as **one segment based at 1**. One,
   because a reducible channel's segment header carries the folded state at
   its start and only a segment based at 1 can honestly carry the initial
   state.
4. **Publish by rename**, `.fork` per channel first, the node marker
   (`.from`) LAST. A crash between two channels' flips leaves each channel
   individually correct, because `.from` still names the parent: a flipped
   channel reads its absorbed copy, an unflipped one delegates, and the two
   are byte-identical.
5. **Only then unlink.**
6. **Repair presentation** with the store lock released: `Forget` every edge
   naming a doomed node, `Reparent` survivors to a home that outlives the
   delete. Without it a survivor falls back to topology and lands under the
   genesis root with no outfit, which is the fossil this path exists to stop
   making (109 of 503 conversations in the real store, before figwal
   v0.16.1).
7. **Collect the stump** only if nothing is left wearing it.

Crash-safe with no journal, by ordering alone: absorbed records are written
below the node's own fork base, where reads still delegate and cannot see
them; the rename is the only thing that publishes them.

**Two consequences.** Detach is **already a compaction** (fold a prefix into
one segment, publish by rename), so segment normalization is the same
operation with a different trigger. And reclamation **already defers** (stump
collection waits to see where survivors land), so lease-deferred reclamation
is not a new kind of debt.

## 3. The primitive

### 3.1 Lifecycle

One structure in `internal/actor`, reused by every customer. Three states on
one atomic word:

```
idle       no goroutine exists
running    a goroutine is draining
lingering  drained, waiting out the affinity window
```

- **Submit**: enqueue, then `idle -> running` (CAS wins: spawn),
  `lingering -> running` (CAS: signal, no spawn), or `running` (nothing).
- **Drain**: take up to N (cap ~64), process each in turn, one fsync,
  publish, advance the ticket, emit.
- **Linger**: `running -> lingering`, wait on a signal or a timer.
- **Dormant**: timer wins, `lingering -> idle`, exit, **then re-check the
  queue** and CAS back to `running` if it is non-empty. A submit landing
  between the emptiness check and the state change is the unique failure
  mode of this design, and it presents as a hung `fig set` that unhangs on
  the next write. Test it before writing it.

Going dormant also releases the xwal handle. See §9: three idle clocks
become one policy.

### 3.2 Blocking and non-blocking on one mechanism

No reply channel per call.

```go
type Ticket uint64
func (f *Form) Submit(cmd Command) (Ticket, error)   // never waits
func (f *Form) Await(t Ticket) (Result, error)       // caller's own goroutine
func (f *Form) Apply(cmd Command) (Result, error)    // Submit + Await
```

The worker keeps `patched atomic.Uint64` plus a broadcast tick (a
`chan struct{}` closed and replaced at each publish). `Await` reads the tick,
checks `patched >= mine`, waits, repeats. **No goroutine per call, no channel
allocated per call**, one broadcast serving every waiter. The result rides
the caller's own submitted struct, filled before the release-store.

Because the ticket advances only after the fsync, a blocking `Apply` returns
when the patch is **durable**, which is what makes `fig set -j` trustworthy
to a script.

### 3.3 The order inside a patch, and why nothing needs to be reversible

`Form.commit` is already ordered correctly and only the fsync is missing:

```go
applied := effectivePatch(st.snap, cmd.Patch)  // PURE. reads state, mutates nothing
payload := marshal(applied)
version := f.log.AppendPatch(payload)          // durable half
// fsync HERE  <- the only addition
next := &formState{snap: st.snap.Apply(applied), version: version, ...}
f.state.Store(next)                            // visible half
```

The reduce is a pure function of published state; nothing is applied until
after the append. **A failure anywhere before the `Store` leaves the
published state untouched, so there is nothing to roll back.** Reverse
patches are a whole subsystem (derive an inverse, hold it across the window,
apply under failure, test it) that the ordering makes unnecessary.

**What is missing is only the fsync.** figwal's `Log.Write` buffers and syncs
when pending bytes exceed `maxLag`, default **64 MiB**. A form patch is a few
hundred bytes, so nothing syncs. The comment in `store.Form` claiming
durability precedes visibility is true of ORDERING and false of DURABILITY.

figwal needs one method: `XWAL.SyncChannelThrough(channel string, idx uint64)`.
`Log.SyncThrough` exists; `XWAL` exposes only `Sync` and `SyncCoherent`, and
the latter syncs the main channel first and would couple a form's durability
to the IR's.

**Group commit is what makes it affordable.** An fsync is 50 to 200 µs. Per
patch at 256 concurrent writers that is tens of milliseconds serialized; per
batch, 256 patches cost one. Batch the **durability**, never the
**semantics**: each command is still reduced against the state as of its own
position in the queue, or two clients' `ifVersion` guards stop meaning
anything.

### 3.4 Where the version comes from

Nowhere in figaro. `AppendPatch` → `Trunks.Append` → `XWAL.Append` →
`next := ch.log.LastIndex() + 1`, assigned under figwal's per-lineage lock
and returned back up. **The form's version IS the record index on the form
channel**: dense, monotonic, meaningful across restarts, and the coordinate
`PatchesBetween(after, upTo]` slices on.

## 4. The protocol

### 4.1 Command, event, acknowledgement

```
Command  { session, seq, patch, if_version?, intent }   may be refused
Event    { version, applied, session?, seq? }           a fact; on the stream
Ack      { session, seq, outcome, version, applied? }   to the submitter, always
```

**Ids.** `session` is per client connection; `seq` is **monotonic within a
session**. That pair is the prior art from Raft's client sessions and Kafka's
idempotent producer: it gives ordering, duplicate suppression across a
reconnect, and a correlation key for the ack. A UUID would give none of
those. The server keeps a small per-session window of the last applied `seq`,
so a retry after reconnect is recognized rather than applied twice.

**Every command receives an ack.** This is a rule the current code breaks: a
command that reduces to nothing produces no record, no event, no delta and
`err == nil`, so a caller cannot distinguish "I changed something" from "I
changed nothing". Under optimistic replication that is fatal, because the
client's optimistic state waits forever for a correction that never comes.

The split:

- **The log** keeps its rule: a patch that changes nothing is not an event.
  No record, no version bump, nothing on the stream.
- **The protocol** gains its rule: the submitter is acked regardless, and
  `applied: {}` at version V is a fine answer.

### 4.2 Intent: what a removal means

**Ruling (Gluck, 2026-08-12): a delete of a key that is not there is
REJECTED.** It is a caller with a wrong model of the world, and it should be
told rather than silently satisfied. That
conflicts with one existing caller unless intent is explicit: `-D` on a birth
patch means "do not inherit this", and naming a key the parent closure does
not hold is reasonable there.

So a command carries an intent:

| intent | set | remove | who |
|---|---|---|---|
| `assert` (default) | reduce | **reject if absent** | `fig unset`, `form delete`, an agent |
| `ensure` | reduce | reduce if absent | birth dressing `-D`, machine writers |

Same event either way. The reducer stays pure; the refusal happens in
validation, before the reduce.

### 4.3 Optimistic replication

Client state is two things, never one:

```
authoritative   snapshot at V         only events touch it
pending         [command…] in order   only commands live here
display         = replay(pending, onto: authoritative)   derived, never stored
```

- **Submit**: assign `seq`, append to `pending`, recompute `display`, send.
- **Event at V+1**: apply to `authoritative`; if it echoes a `seq` you own,
  drop that command; recompute `display`.
- **Ack (refused)**: drop the command; recompute `display`. **The optimistic
  change vanishes with no undo logic**, because rollback is "do not replay
  it".
- **Reconnect**: `SubscribeFrom(V)` gives snapshot and stream from one
  serialization point, so there is no window; unacked commands are
  re-submitted under the same `seq` and deduplicated.

The prediction must run **the same reduction code** as the server
(`effectivePatch`, pure, shippable), or the client is a second implementation
that drifts. Never mutate and undo; always rebase. This is client-side
prediction with server reconciliation, the Quake 3 lineage, and it is the
right one because you have a single authority and a total order per form.
OT solves a harder problem you do not have; CRDTs remove the authority you
want.

## 5. Validation and protection

**Ruling (Gluck, 2026-08-12): the dot-prefixed reserved path is dropped.**
One mechanism, not two: protection lives entirely in the schema. This also
retires the `.id` rename and the migration it would have needed;
`aria_id` stays where it is, protected by its `KeyMode` instead of by its
spelling.

`form.WellKnownKeys` already carries a `KeyMode` per key
(`KeyUserSettable`, `KeySystemManaged`, `KeyEphemeralPerTurn`). Today it is
documentary. Make it enforceable:

- **`KeySystemManaged` refuses an external write.** Privilege is a property
  of the call site, not a field on a request: an unexported entry point that
  only in-process callers can reach, with the public one refusing.
- **A declared shape is checked**: kind, and later a validator.
- **An unknown key is not an error.** The schema is open and must stay open.
- **Provider-keyed system schema** comes later, with `system.provider`
  becoming an object whose `name` discriminates. It is a migration of the
  most load-bearing key in the store, so it waits for the rest to be
  written down.

**Where it runs:** inside `commit`, **before the reduce**. It is the only
place both write paths pass through and the only place a check is atomic
with the append. Cost is a map lookup and a branch per key, on patches that
are one to three keys.

**The rule that must not be got wrong:** validation applies to what a patch
**writes**, never to what a board already **holds**. Otherwise the first
aria carrying a stray key becomes unpatchable.

## 6. Subscription

```go
// Processed IN the queue, so registration and snapshot share one point.
func (f *Form) SubscribeFrom(after uint64, holder Lease) (Snapshot, uint64, <-chan Event, func())
```

**Semantics:** you receive the state as of version V together with every
event after V, and no event between them can be lost or duplicated, because
the snapshot and the registration happen at the same serialization point. Any
implementation that reads a snapshot and then subscribes has a window where a
patch is in neither.

**It is a queued command, not a direct call.** Registration is durable (it
takes a lease, §7), and a durable registration is a write, and a write needs
the writer. Calling it "inside the writer" from outside would re-enter the
lock. It goes through the same inbox as any other command and returns its
snapshot as the result.

**Who needs it:** derived forms (resume from a durable cursor rather than a
full re-fold); the libretto, to hear a studied form change and to hear it
die; `fig form listen`, which today reads a snapshot then attaches to the
fanout and has exactly this race; the node cache, replacing polling on the
presentation revision with an invalidation; and remote form runtimes later.

**Slow subscribers:** per-subscriber buffered channel; on overflow, drop,
count, and deliver a `Resync` marker when space frees. A silent drop is
unacceptable because a mirror that misses one patch is wrong forever without
knowing it. The worker must never block on a consumer.

## 7. Deletion, leases, reclamation

**The tombstone is state.** A final patch on the dying form's own channel
sets a reserved key, after which the channel is sealed and further patches
are refused. Subscribers hear it through the same stream as any other
change: one delivery path, one durability story, and an offline subscriber
reads it on resume.

**Leases, not refcounts.** A counter cannot distinguish "still reading" from
"died holding a reference". A subscriber registers `{id, holder, expires}`
and renews while it lives; a sweep drops expired registrations; a tombstoned
form with no unexpired lease is reclaimable.

- **Restart is a clean sweep, not a timeout.** Within one daemon the holder
  is the process instance, so every lease from a previous instance is
  provably dead at start and cleared in one pass. The TTL only covers a
  holder that is alive but silent, which today is nobody and later is a
  remote node.
- **Renewal rides existing timers**: the 2-second pid monitor renews, the
  120-second reclamation sweep expires. No new goroutine.
- **TTL in minutes**, configurable (§9). Being wrong in one direction holds
  a form on disk slightly too long; in the other it reclaims state a live
  reader still needs.

**Composition with §2:** a tombstoned node whose boundary survivors have not
detached must not be unlinked either. Two reasons to defer, one mechanism to
wait on.

## 8. Every form, and what it owes

A node has **channels**: the main IR log, the form channel, and
`translations-v2/<provider>`. The **form** is one channel. The IR and the
translator caches are **logs**, not forms: figaro-specific constructs with
their own lifecycles, coupled to the form that gives the node its identity.
When this distributes, the three activate together, because identity is the
bound form.

| state | kind | history observable | compaction | durability |
|---|---|---|---|---|
| bound form (an aria's) | form | **yes, load-bearing** | forbidden | sync before publish |
| unbound form / role | form | **yes, load-bearing** | forbidden | sync before publish |
| outfit node / default form | form | irrelevant (one patch) | unnecessary | sync at birth |
| **topology form** | form | no | **required**, single segment | sync before publish |
| derived spec | form | no | desired | sync before publish |
| derived values | form | no | desired | sync before publish (see §14) |
| IR | log | **yes, it is the conversation** | never | as today |
| translator cache | log | no | explicit `fig translator evict` only | durable, like the IR |

**Why the first two forbid compaction, precisely.** The projection renders a
form's transitions BETWEEN two stamps and re-derives them on every
retranslate. A compacted channel cannot answer what changed between two old
versions, so a retranslate would render a different context than the first
pass did, and the per-LT translation cache would make the disagreement
permanent. History IS the value for these.

**The rule as a property, not a promise:** a form may compact only if it
hands out no patch views and nothing renders its history. Enforced by the
type, or someone adds a `Patches` accessor to the topology form in a year
and the safety argument evaporates silently.

## 9. Configuration: one policy, several enforcement points

| clock | where | default | configured today |
|---|---|---|---|
| agent + cache reclamation | `EvictIdle` | 15 min | `[memory] dormant_after_minutes` |
| figwal head unload | `xwal.Store` `IdleUnload` | 5 min | **no** |
| actor linger | new | ~2 s | **no** |
| subscriber lease TTL | new | ~10 min | **no** |

```toml
[memory]
dormant_after_minutes = 15
handle_idle_minutes   = 5
actor_linger_ms       = 2000
lease_ttl_minutes     = 10
```

In-binary defaults must be right without any file, since the author's
`config.toml` is one line. **The test to write is the one that catches this
class**: a config supplying all four, read through the loader, asserted at
each enforcement point, not at the parser.

`[store] segment_size` (2 MiB) gains a companion for compacting channels,
which want a small segment so a roll is cheap.

## 10. Compaction, as segment normalization

Replace a channel's segments with one segment holding the folded document.
Not truncation in place. `absorbPrefix` already has the shape: write the new
segment, fsync, rename, fsync the directory. Either the old or the new is
present, and both fold correctly.

**Trigger: on roll.** Tune the segment small so rolls are frequent and each
is cheap. When a segment fills, the writer serializes the whole document into
a fresh segment before continuing: a pause proportional to the document, not
to history.

Acceptable for the topology form (a different concurrency domain, bursty and
rare writes, a small map) and not for a board (written mid-turn on the hot
path).

## 11. The topology form

`@trunks` becomes **the topology form**, `@topology`. One per store, which is
one per angelus, because the angelus owns the store.

- **One patch per change**, always. A promote is two keys in one record,
  which is what makes it atomic; today it is one `save()` of a whole file and
  the pair can half-land.
- **Validation inside the loop**, in the same critical section as the patch.
  Today promote reads topology, decides, and writes overrides in separate
  steps: a TOCTOU against a concurrent promote or a delete's `Forget`.
- **Reads are free**: the folded tree is an AVL root behind an atomic
  pointer.
- **History not observable**, single segment, compaction required.
- **Never listed, never forked, never bound.** Excluded from
  `Conversations()` and from `ls -g`'s form rows, or the tree grows a row
  describing itself.
- **Bootstrap is safe because absence is truthful**: a store whose topology
  form is not yet resolved renders where history puts things, which is
  right, not wrong. That is what keeps the cycle (the listing needs the
  tree, the tree lives in a node in the store) from being a deadlock.
- **Partition later** if one writer becomes a bottleneck: one form per child
  of the null root, or per child of null-or-outfit-stump. The document
  partitions cleanly because an edge names only its two endpoints.

## 12. Derived forms, and the libretto

A derived form is a **compound**: two forms, two concurrency domains.

- **The spec**: form id to observed subtype, a JSON tree of subscribed
  fields. Presence-only grammar for now,
  `{"@abc": {"brief": "this", "status": "this"}}`. An expression language
  later.
- **The values**: the same shape, holding values.
- **The derivation**: an actor with two producers, incoming patches from its
  subscription set and patches its own spec emits.

Two forms because the spec is user intent and the values are machine output:
different writers, different lifecycles, different protection classes. One
form would mean two writers on one node and the model collapses.

**`study` becomes an alias** for setting `{"@formid": "this"}` on the
libretto; `drop` is a key removal. Both are ordinary commands through an
ordinary writer, which deletes `study.go`'s board list, `study_hub.go`'s
dormant half, and the `SetObservedForms` mirror.

**Self-modifying derivations are refused structurally** at subscribe time. A
derivation subscribing to its own output is a feedback loop: harmless with
presence-only grammar, oscillating with expressions, and it will oscillate at
3am inside an actor nothing is watching. Do not build a fixpoint evaluator
with a step cap.

**Allocation.** Three copies per observed value (source snapshot, derived
tree, the figaro's read) plus the spec, where a study costs zero today
because the projection reads the source directly. `form.Snapshot` is an
immutable AVL where `Clone` is the identity, and a presence-only derivation
is literally a subtree selection, so **write it as tree surgery, not
marshal-and-unmarshal**. Very hard to retrofit once the derivation speaks
JSON internally.

## 13. Inconsistencies found while writing this down

Recorded because each was a real contradiction between two turns of the
design, and the resolutions are load-bearing.

1. **The libretto cursor write versus fsync-before-publish.** If the drain
   loop writes cursors on the IR hot path and that write is now an fsync,
   every IR record costs a form sync. Worse, if the write is made async, the
   IR could stamp a libretto version that is not yet durable, and a crash
   leaves the IR referencing a version that never existed: an ordering
   violation ACROSS forms.
   **Resolution: read-then-stamp, never write-then-stamp.** The loop stamps
   the libretto version it *read*, which is durable by construction because
   publish follows fsync. The cursor *update* is a separate non-blocking
   `Submit`. No cross-form ordering constraint exists, and the hot path pays
   nothing.
2. **"Validation inside the topology loop retires the `deleting` lock" was
   too strong.** A delete is filesystem repair (detach, unlink) plus a
   topology patch. The actor owns only the second. The `deleting` lock still
   guards the filesystem half; what the actor removes is the TOCTOU on the
   topology half.
3. **`SubscribeFrom` "inside the writer" would re-enter the lock**, because
   registration takes a durable lease, and a durable registration is a write.
   Resolved by making subscribe a queued command whose result is the
   snapshot.
4. **Rejecting removals of absent keys breaks birth dressing.** `-D` on a
   birth patch means "do not inherit this" and may name a key the parent
   closure lacks. Resolved by the `assert` / `ensure` intent in §4.2.
5. **"Every command gets an answer" versus "a patch that changes nothing is
   not an event."** Both are right, about different layers. Resolved by
   splitting the log rule from the protocol rule (§4.1).
6. **Compaction versus zero-copy patch views.** `PatchesBetween` returns a
   view into an immutable array whose safety rests on append-only. Resolved
   by the type-level rule in §8.
7. **A panicking commit sink now takes down the caller**, since main moved
   sinks off the actor goroutine and onto the caller under the write lock.
   Two lines of `recover` around the sink loop, and it belongs there
   regardless of the rest of this plan.
8. **The dot-prefix protection class is dropped** in favour of the schema
   alone (§5), which also retires the `.id` rename and its migration.

## 14. What this costs

1. **Solo write latency**: a form patch goes from a buffered memcpy (~5 µs)
   to a real fsync (~50 to 200 µs). `fig set`, every mantra update. The
   number to watch.
2. **Contended throughput improves**: group commit amortizes the fsync and
   takes figwal's per-lineage lock once per batch rather than once per patch.
3. **Goroutines drop for idle forms** and do **not** drop for blocking
   callers, who still park. `Submit` without `Await` is what removes those.
4. **Derived forms cost three copies per observed value** unless the tree
   surgery is written from the start.
5. **The topology form serializes globally.** Rare writes, free reads,
   partitions later.
6. **Compaction pauses the topology writer** for one document serialization,
   on roll.
7. **Deferred reclamation holds disk** for tombstoned forms with live leases.
8. **Validation costs a lookup and a branch per key**, on one-to-three-key
   patches.

**Measure before anything:**

- `BenchmarkFormApplyContended`, W in {1, 8, 64, 256}: ns/patch,
  allocs/patch, and **fsyncs per patch**, the number that proves group commit
  works.
- Solo latency at W=1, the interactive case and the one that regresses.
- `BenchmarkFormApplyManyForms`, to prove domains stay independent.
- Peak goroutines, the number the mutex change was made for.
- A soak asserting no submit is lost across a spawn/exit boundary.
- The twelve-aria live recipe before and after, with PSS and swap.

## 15. Build order

1. **`SubscribeFrom`** as a queued command. Fixes `form listen`'s existing
   race on its own.
2. **The lazy actor** in `internal/actor`, exit race handled and tested once.
3. **Group commit and sync-before-publish**, plus
   `XWAL.SyncChannelThrough`. Plus the sink `recover`.
4. **Command/event/ack on the wire**: `session`, `seq`, intent, and the
   acknowledgement of a no-op. The whole server side of optimistic
   replication, worth having before any replica exists because it also fixes
   the silent-no-op ambiguity.
5. **Schema validation** in `commit`, `KeySystemManaged` enforced.
6. **Tombstones and leases.**
7. **Segment normalization**, with the type-level rule.
8. **The topology form**, replacing `trunks.json`, with its migration.
9. **Derived forms**, libretto first, `study` as an alias.
10. **The API refactor** (§16), before the surface grows by the methods the
    above adds.

Steps 1 through 4 are worth having even if 5 through 10 never happen.

## 16. The API refactor

Last, deliberately: it is the smallest change and the one most likely to be
deferred, and it should be done AFTER the semantics settle, because the
methods this plan adds should be born under the rule rather than renamed
later. But it should be done BEFORE those methods exist, so the ordering is
"last of the design, first of the implementation of §§1 to 12".

### 16.1 What is wrong

The first segment of a method name means three different things depending on
which line you are on:

| prefix | means | examples |
|---|---|---|
| `figaro.` | sometimes *sent to an aria* | `figaro.qua`, `figaro.set`, `figaro.study` |
| `figaro.` | sometimes *about arias, sent to the daemon* | `figaro.create`, `figaro.list`, `figaro.kill` |
| `aria.` | *about an aria, answered from the store* | `aria.form`, `aria.context`, `aria.read` |
| `angelus.` | the daemon itself | `angelus.status`, `angelus.outfits` |
| `form.` | forms, three of them only | `form.create`, `form.bind`, `form.delta` |
| `pid.` | shell bindings | `pid.bind`, `pid.resolve` |
| (none) | | `turn.done`, `outfit.reload` |

So the prefix is sometimes the SUBJECT, sometimes the RECIPIENT, and in the
largest namespace both at once. There is no rule a newcomer could infer,
which is why every addition has been a coin flip.

Four concrete symptoms:

1. **The same question has two names**, chosen by where the state happens to
   live: `figaro.form` / `aria.form`, `figaro.context` / `aria.context`,
   `figaro.read` / `aria.read`. Residency is the implementation detail the
   hub exists to hide, and it already hides it for writes.
2. **Nouns and verbs are mixed with no convention.** `figaro.qua` and
   `figaro.set` are verbs; `figaro.form` and `figaro.queued` are nouns with
   an implied get. You cannot tell whether `figaro.form` reads or writes
   without opening the handler.
3. **The noun sometimes lies.** `figaro.kill` also deletes forms.
4. **The wire types are the internal types.** `type FormPatch = message.Patch`
   is an alias, so an internal refactor is a wire break with no compiler
   error at the boundary.

### 16.2 The rule

**The node socket is the object's interface. The angelus is the registry and
the topology.**

A message to one existing node goes to that node's socket. Anything that
needs to know about more than one node, or about a node that does not exist
yet, goes to the angelus. Bulk reads therefore stay on the angelus, because a
listing touching 600 boards must not stand up 600 sockets.

Names are `<subject>.<verb>`, subject in {`aria`, `form`, `node`, `outfit`,
`shell`, `angelus`}, verb always explicit.

This is also the posture: **a figaro extends a form.** The base interface is
what a form answers; the extension is what only a turn loop can answer; and
the socket is the subtype boundary. `fig set` is a CLI alias for
`form.patch` on whatever is attended.

### 16.3 The target surface

**On the node.**

```
form.get          form.patch        form.subscribe     form.watch(cancel)
aria.prompt       aria.interrupt    aria.read          aria.context
aria.queue.list   aria.queue.update aria.queue.delete
```

**On the angelus.**

```
node.create       node.delete       node.fork          node.attach
node.list         node.promote      node.import        node.normalize
node.gc
outfit.list       outfit.reload
shell.bind        shell.resolve     shell.unbind
angelus.hello     angelus.status    angelus.configure  angelus.save_bindings
```

**Pushes.** `aria.frame`, `turn.done`, `form.event` (replacing
`form.delta`, and now carrying `version`, `applied`, and the originating
`session`/`seq`).

### 16.4 What dies, and into what

| today | becomes |
|---|---|
| `figaro.form`, `aria.form` | `form.get` |
| `figaro.context`, `aria.context` | `aria.context` |
| `figaro.read`, `aria.read`, `aria.page` | `aria.read`, one paging shape |
| `figaro.set` | `form.patch` |
| `figaro.study`, `figaro.drop` | `form.patch` on the libretto (§12) |
| `figaro.cast` | stays a verb: it is two writes across two nodes |
| `figaro.create`, `form.create`, `form.bind` | `node.create` with kind + parent |
| `figaro.kill` | `node.delete` |
| `figaro.queued` | `aria.queue.list` |
| `figaro.gc`, `figaro.normalize`, `figaro.import`, `figaro.promote` | `node.*` |
| `figaro.attach` | `node.attach`, and the only method that hands out an endpoint, so also where role redirection lands |
| `pid.*` | `shell.*` |
| `angelus.info` | folded into `angelus.hello` |

Seven methods become three; three namespaces become one rule.

### 16.5 The wake property

`wake: never | if-needed | always` on node calls, defaulted per method,
replacing `rpc.MethodNeedsAgent`.

- **never**: refuse with a typed error if answering would require an agent.
  Every reader path wants this, and today the only way to ask for it is to
  pick a different method on a different socket.
- **if-needed**: today's behaviour. The default for `aria.prompt`,
  `aria.interrupt`, `aria.queue.*`.
- **always**: revive deliberately, which is what `fig at` means.

After this plan, `form.patch` and `form.subscribe` default to **never**,
because they genuinely never need the loop. That is the whole reason
`study_hub.go` exists, and it can be deleted.

The rung that must stay unconditional: **if an agent is resident, it handles
the call**, whatever the verb, because it holds the open streaming region,
partial tool arguments and the in-flight turn, none of which are in the
store. Serving the store's view of a live aria looks like a hang.

### 16.6 Migration, which is the only hard part

**Aliasing, not breaking.** Every old name routes to the same handler
forever. Mechanically a lookup table and a deprecation comment.

**Version skew needs a handshake.** A new daemon that changes behaviour
unconditionally breaks an old client in confusing ways. The worked example is
role redirection: if `node.attach` resolves `target-aria` server-side, an old
CLI receives the holder's endpoint, runs its own `redirectRole` against the
role id, dials what is now the aria's endpoint, reads a board with no
`target-aria`, and dies saying a figaro is not a figaro. Both halves correct,
one confusing failure.

So: `angelus.hello` returns a protocol version and a capability set,
negotiated once per connection, and every behaviour change is gated on a
capability rather than on an optional request field that can never be
removed. That is also what lets `wake`, `session`/`seq` and role redirection
ship without a flag day.

**Wire types stop being internal types.** `FormPatch` and the snapshot in
`FormResponse` become wire structs that internal types marshal into. More
code, and the difference between a refactor being a refactor and a refactor
being an outage.

**The wire reference does not exist and should.** There is no table of the
method set, its params, its responses and who answers. That absence is why
the surface drifted: nothing shows the whole shape at once, so nothing looks
inconsistent while you are adding to it. Write it with this refactor, and
generate the verb table in `skills/figaro/cli.md` from the help text so drift
becomes impossible rather than merely discouraged.

## 17. Open rulings

- Does the topology form reuse `store.Form` or get its own type? Own type
  makes compaction and no-views properties rather than promises; sharing
  keeps one commit protocol. Depends how good step 2 is.
- Does the libretto fork with its figaro? Instinct: a fork copies the SPEC
  and shares nothing else.
- Do derived VALUES get the same sync-before-publish? They are rebuildable,
  so relaxed is defensible; I lean against, because what the libretto holds
  is what the model is shown.
- Does `ensure` intent stay internal (birth dressing only) or become a
  client-facing flag?
