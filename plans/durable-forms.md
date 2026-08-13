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
1. **Durable before visible, with no buffer in between.** A patch is
   fsynced before it reaches the published state, on every channel including
   the IR. A sync that fails REJECTS the patch rather than half-applying it.
   The reverse ordering is not a lost write but a hallucinated one: the model
   is shown state as a reminder, so a crash would leave it acting on
   something that never happened.
1. **Reads are lock-free.** The live state is an immutable AVL root behind an
   atomic pointer. A read is one load. It never blocks a writer, never wakes
   anything, never serializes behind a turn.
1. **Command and event are different things.** A command may be refused. An
   event is a fact and can only be observed. The reduction from one to the
   other is pure and happens in the writer.
1. **Every command receives an answer.** Silence is not a legal outcome, even
   when the log gets nothing.
1. **Absence is the truthful default.** State describing other state holds
   overrides, never a full picture, so a lost document degrades to the truth
   rather than to a lie.
1. **Forking never consults presentation.** A presentation edge must never
   decide where data comes from.
1. **Deletion is a record, not an event in memory.** A subscriber that was
   offline must be able to learn it; a replay must reproduce it.
1. **History is observable unless the form declares otherwise**, and a form
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
1. **Find the boundary**: survivors outside the delete set whose lineage runs
   through it (`topo.Boundary`).
1. **Detach each** (`Trunks.Detach`): copy `[1, base)` out of the ancestor
   chain into the node's own directory as **one segment based at 1**. One,
   because a reducible channel's segment header carries the folded state at
   its start and only a segment based at 1 can honestly carry the initial
   state.
1. **Publish by rename**, `.fork` per channel first, the node marker
   (`.from`) LAST. A crash between two channels' flips leaves each channel
   individually correct, because `.from` still names the parent: a flipped
   channel reads its absorbed copy, an unflipped one delegates, and the two
   are byte-identical.
1. **Only then unlink.**
1. **Repair presentation** with the store lock released: `Forget` every edge
   naming a doomed node, `Reparent` survivors to a home that outlives the
   delete. Without it a survivor falls back to topology and lands under the
   genesis root with no outfit, which is the fossil this path exists to stop
   making (109 of 503 conversations in the real store, before figwal
   v0.16.1).
1. **Collect the stump** only if nothing is left wearing it.

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

**All three have wire equivalents** (§16.3): `form.submit` returns a ticket
without waiting, `form.await` blocks on one, and `form.patch` is the pair,
which a client may implement locally rather than as a third round trip. A
client that wants optimistic replication uses `form.submit` plus the event
stream and never calls `form.await` at all.

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

**How an append is made durable, and whether that is cheaper than an
in-place update.** Three facts:

- `fsync` flushes the file's data AND its metadata. `fdatasync` skips
  metadata that is not needed to read the data back, but a file's SIZE is
  needed, and an append changes the size. So for a plain append the two cost
  about the same: both write the inode.
- An in-place overwrite does not change the size, so `fdatasync` there
  flushes data blocks only, and is genuinely cheaper. But an in-place update
  is not available to us: a torn overwrite destroys a record that was
  already durable, which is exactly what a log must never do. It is why
  segment logs never rewrite bytes anyone might be reading.
- The database trick that gets both is **preallocation**: `fallocate` the
  segment to its full size at creation, so appends write into space that
  already exists and the size never changes, and then `fdatasync` is the
  cheap one. figwal does not do this today (it uses `f.Sync()`, plus a
  directory fsync for creations and unlinks), and it is the first place to
  look if the sync cost turns out to matter.

So: appends are correct and currently pay a metadata flush. If the measured
cost is a problem, preallocate; do not reach for in-place writes.

The reduce is a pure function of published state; nothing is applied until
after the append. **A failure anywhere before the `Store` leaves the
published state untouched, so there is nothing to roll back.** Reverse
patches are a whole subsystem (derive an inverse, hold it across the window,
apply under failure, test it) that the ordering makes unnecessary.

**What is missing is the fsync, and the buffer it hides behind.** figwal's
`Log.Write` buffers and syncs only when pending bytes exceed `maxLag`,
default **64 MiB**. A form patch is a few hundred bytes, so nothing syncs.
The comment in `store.Form` claiming durability precedes visibility is true
of ORDERING and false of DURABILITY.

**Ruling (Gluck, 2026-08-12): figwal becomes a true WAL and the flusher
goes.** Every figaro append syncs before anything reaches memory, on every
channel including the IR. A patch that fails to sync is REJECTED: it never
enters the published state, so there is nothing to reconcile and no
"succeeded but might not have" outcome for a caller to handle. The lag
buffer may survive in figwal behind a config for other users of the library,
but figaro sets it to zero.

This reverses decisions taken before the log was understood as a log, and
it will cost. The recovery is in batching, not in buffering: one fsync per
BATCH is the optimization, and it is available precisely because the actor
exists.

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
(Gluck approves on batching the durability but not the semantics.)

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

| intent             | set    | remove               | who                                  |
| ------------------ | ------ | -------------------- | ------------------------------------ |
| `assert` (default) | reduce | **reject if absent** | `fig unset`, `form delete`, an agent |
| `ensure`           | reduce | reduce if absent     | birth dressing `-D`, machine writers |

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

### 6.1 The subscriber set is its own concurrency domain

**Yes, and it is better than putting it in the queue.** The registry is an
atomic pointer to an immutable subscriber slice, replaced by
compare-and-swap on register and unregister. No lock, no queue, its own
domain, and `emit` reads it with one load.

What made me route it through the queue was the snapshot race: read a
snapshot and then subscribe, and a patch landing between the two is in
neither. **Reverse the order and the race is gone:**

1. **Register first** (atomic CAS into the subscriber set).
2. **Then read the published snapshot** (one atomic load).

Any event that lands between the two is *delivered* AND *contained in the
snapshot*: a duplicate, not a gap. The subscriber discards events at or
below its snapshot version and is exactly correct. Duplicates are
recoverable; gaps are not, and the ordering is what decides which one you
get.

So `SubscribeFrom` is **not** a queued command. It is two atomic
operations in that order, and the subscriber's own version filter.

## 7. Deletion, leases, reclamation

**The tombstone is state.** A final patch on the dying form's own channel
sets a reserved key, after which the channel is sealed and further patches
are refused. Subscribers hear it through the same stream as any other
change: one delivery path, one durability story, and an offline subscriber
reads it on resume.

**Leases, in memory, best effort.** The first draft made them durable and
refcounted. The libretto's intervals (§12.3) removed the need: what a durable
count was for was keeping a source alive long enough for a reader to see it,
and the intervals record what was seen instead.

So the registry is in-memory: `{id, holder, expires}`, renewed while the
holder lives, swept when stale, lost on restart, which is correct because
every holder is lost on restart too. A tombstoned form with no unexpired
lease is reclaimable.

- **Restart is a clean sweep**: no lease survives, so nothing waits out a
  TTL for the common case. The TTL covers a holder that is alive but silent,
  which today is nobody and later is a remote node.
- **Renewal rides existing timers**: the 2-second pid monitor renews, the
  120-second reclamation sweep expires. No new goroutine.
- **TTL in minutes**, configurable (§9).
- **Missed reclamations are tolerated and logged.** A holder that dies
  without unsubscribing leaves a form on disk until its lease expires; a
  sweep that races a subscribe may reclaim something a reader wanted, and
  that reader gets a "the form you were following is gone" error rather than
  a corruption. Both are recoverable, both are logged, and neither justifies
  a distributed protocol. Self-recover where possible and iterate once the
  libretto is working.

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

| state                      | kind | history observable              | compaction                           | durability                    |
| -------------------------- | ---- | ------------------------------- | ------------------------------------ | ----------------------------- |
| bound form (an aria's)     | form | **yes, load-bearing**           | forbidden                            | sync before publish           |
| unbound form / role        | form | **yes, load-bearing**           | forbidden                            | sync before publish           |
| outfit node / default form | form | irrelevant (one patch)          | unnecessary                          | sync at birth                 |
| **topology form**          | form | no                              | **required**, single segment         | sync before publish           |
| derived spec               | form | no                              | desired                              | sync before publish           |
| derived values             | form | no                              | desired                              | sync before publish (see §14) |
| IR                         | log  | **yes, it is the conversation** | never                                | as today                      |
| translator cache           | log  | **yes, retained**               | never; tail truncation only          | durable, like the IR          |

**The translator cache, corrected.** It is retained indefinitely and never
compacted. The only removals are from the TAIL: an API error or an explicit
eviction truncates back to a point and the suffix is regenerated. Nothing
earlier than the tail is ever rewritten, which is the same discipline the IR
has and the reason both can be trusted by a reader holding an old position.

It already has the two coordinates this needs: `store.Entry` carries `LT`
(its own position in the translation channel) and `FigaroLT` (the IR record
it translates), so the index from a cached translation back to its source
exists today and is what `Cache.Lookup(entry.LT)` uses.

Durability equal to the IR: sync before publish, same as everything else.
The word "observable" in this table means **retained**, not "rendered to a
model": a channel is observable when a reader can still ask what it looked
like at an old version.

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

| clock                     | where                     | default | configured today                 |
| ------------------------- | ------------------------- | ------- | -------------------------------- |
| agent + cache reclamation | `EvictIdle`               | 15 min  | `[memory] dormant_after_minutes` |
| figwal head unload        | `xwal.Store` `IdleUnload` | 5 min   | **no**                           |
| actor linger              | new                       | ~2 s    | **no**                           |
| subscriber lease TTL      | new                       | ~10 min | **no**                           |

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

## 10. Retention, which is what "compaction" means here

Compaction is not a distinct operation and introduces no new pause. It is a
**retention policy**: how many sealed segments a channel keeps.

Every reducible channel already rolls segments, and **a reducible segment's
header is the folded state at its start**. So the fold that a compacted
channel needs is written by the ordinary roll, for every channel, today. The
only difference between a normal form and the topology form is what happens
to the sealed segments behind the newest one:

| channel | policy |
|---|---|
| bound form, unbound form, derived form | keep all |
| topology form | keep 1 |
| indexed forms (future) | configurable N |

Deleting the segments behind the newest is a background unlink. Nothing
blocks, nothing is serialized into a fresh file specially, and the crash
story is the roll's own: the newest segment folds correctly on its own, and
an interrupted unlink leaves a file that the next pass removes.

The blocking cost that does exist is the **roll itself**, which serializes
the fold into the new segment's header. That is paid by every reducible
channel, is proportional to the document rather than to history, and is
tuned by segment size: small segments roll often and cheaply, large ones
rarely and expensively. The topology form wants small.

**The rule that still holds:** a channel may set a retention policy only if
it hands out no patch views and nothing renders its history, because
`PatchesBetween` returns a view into an array whose safety rests on the
records still being there. Enforced by the type (§8).

**All of it is configurable** (§9) and the values are fuzzed, because a
retention policy that is wrong by one segment is a data-loss bug that only
appears under a specific roll boundary.

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

- **It always exists, and it is outside the graph it describes.** Corrected
  from the first draft, which said absence was a safe state: it is not a
  state at all. The angelus creates the topology form at store open if it is
  not there, holds it for the daemon's lifetime, and closes it at shutdown.
  It is not a node in the node graph, has no parent, cannot be forked, and
  **does not depend on the topology it holds**, which is what makes the
  bootstrap trivial rather than circular.

  What IS truthful-by-absence is an **entry inside** the document: an aria
  with no override is drawn where its history puts it. That is the property
  worth keeping, and I conflated it with the document itself.

  Of every form in the system this one has the least in common with the
  others: no lineage, no fork, no birth patch, no outfit, one instance,
  daemon lifetime.

- **Not a bottleneck in practice.** Reads are free. Writes are promotes,
  deletes and **forks**, and a fork already goes through the angelus, so the
  serialization point is one the operation was passing through anyway. If
  read pressure ever justifies it, the answer is a derived replica rather
  than a partition.

Partitioning (one form per child of the null root, or per child of
null-or-outfit-stump) is possible because an edge names only its two
endpoints, but it is far off and explicitly not in scope here.

## 12. Derived forms, and the libretto

### 12.1 A derived form is ONE form with one actor

The first draft made it a compound of two forms, spec and values, on the
argument that user intent and machine output need different writers. That
was over-design: **one actor is one writer**, and an actor handling two kinds
of operation is not two writers. Spec changes are rare enough (a study, a
drop) that they share the queue with the folds without contending for
anything.

So: one form, one actor, two namespaces inside the document.

```jsonc
{
  // SPEC: what is observed, and which paths. User intent.
  "spec.@abc123": {"*": true},                        // the whole form
  "spec.@def456": {"brief": true, "status": true},    // a projection

  // RANGES: the durable record of WHEN each was observed, in the
  // source's own versions. Machine output.
  "range.@abc123": [[3, 41], [55, null]],             // dropped at 41, re-studied at 55, open
  "range.@def456": [[7, null]]
}
```

Protection follows the namespace rather than a sigil: `spec.*` is settable,
`range.*` is `KeySystemManaged` and refuses an external write (§5).

**The projection you designed weeks ago comes back here**, where it always
belonged: `study -P brief,status` writes a spec entry with those paths, and
`study` with no `-P` writes `{"*": true}`. The whole form is the default.

### 12.2 One libretto per STUDIED FORM, shared, refcounted

Final shape (Gluck, 2026-08-12), reversing two earlier drafts:

- **One libretto per studied form**, not per figaro. Named
  `libretto::<formid>`, so a form can find its own libretto and a figaro can
  derive the name from the form id it studies.
- **Librettos do not fork.** They are free-standing derived forms with their
  own lifetime, shared by every figaro observing that form.
- **The figaro's bound form holds a `study-set`**: the reserved key naming
  the forms it studies. The libretto for each is derivable from the id, so
  the board needs only the set, not a map.
- **The libretto holds the refcount** of figaros studying it, which is what
  makes it reclaimable when the last one drops.
- **`study-set` may only be written by the study verb**, and it is a
  different actor loop from the libretto's, so the two never contend.

The libretto is still a COPY (§12.3): the projected subset of the form's
state, materialized, with its own history. Sharing it means one copy per
form rather than one per observer, which is most of the duplication cost
gone before it is ever paid.

### 12.2.1 Study is a two-participant write

`study` must leave two nodes consistent: the libretto (subscription and
refcount up) and the figaro's board (`study-set` gains the id). Two actors,
two logs, no shared transaction.

**Not two-phase commit.** The idiom this codebase already uses for the same
shape is the delete path: crash-safe by ORDERING, with no journal. The same
applies here, and the trick is to choose the order so that **every crash
fails in the safe direction**.

- **study**: libretto first (refcount up, subscribe), board second.
- **drop**: board first (stop claiming it), libretto second (refcount down).

Both orders leave, on a crash, a refcount that is **too high**. Too high
delays reclamation; too low reclaims state a live observer still needs. One
is a leak, the other is data loss, and only one of them is recoverable.

**Reconciliation makes the leak finite.** The authoritative fact is "figaro
X studies form Y", and it lives in X's board. A sweep (at daemon start, and
behind a repair verb) **recomputes** each libretto's count from the boards
that name it, rather than adjusting it. Recomputing is what makes it a
backstop for §12.2.2 as well: it repairs an UNDER-count exactly as readily
as an over-count. It runs at daemon start or on demand, so it narrows the
window rather than closing it, and the orderings above and below are still
required. Cheap, idempotent, the same discipline as the delete path's
boundary repair.

### 12.2.2 Fork, import and kill are also participants (found by 057ebc2e)

The ordering in §12.2.1 makes every crash over-count. **Three write sites
outside the study verb break that in the unrecoverable direction**, and the
design as written never named them, because its only fork sentence (§12.2,
"librettos do not fork") rules out forking the libretto NODE and says
nothing about `refs` when an OBSERVER forks.

- **Fork under-counts.** A child inherits the board, therefore the
  `study-set`, therefore every study its parent held — and no libretto is
  incremented. Fork, then let the parent drop: `refs` reaches zero, the
  libretto is reclaimed, and the child is still observing it. **The order
  must be: read the parent's `study-set`, increment each named libretto,
  THEN create the child.** A crash between the increment and the birth
  over-counts, which the sweep repairs; the reverse cannot be.
- **`import` under-counts** identically: it restores a board wholesale whose
  `study-set` names librettos that were never incremented for it.
- **`kill` / `node.delete` must decrement** every libretto its board names,
  or `refs` stays high forever. That is the safe direction, but nothing
  collects it until a sweep.

The rule, stated so a fourth site inherits it: **any operation that brings a
board carrying a `study-set` into or out of existence is a participant in
the refcount.** Only the study verb knows that today.

**This is a bug the libretto INTRODUCES.** Today the relationship rides the
copied board as `system.studies` and a fork inheriting it is correct and
free, precisely because nothing counts. Counting is what makes every copier
of a board a writer of the count.

Neither half blocks the figaro's actor loop. Both may block the board's own
writer and the libretto's writer, which is what keeps the count honest.

`drop` on a form that has since been deleted is **legal**: it removes the
subscription and decrements, and the board stops naming it. A figaro that
does not drop keeps studying a dead form, which is meaningful: if a form
with that id is created again the libretto resumes, because patches are
fully persisted and the subscription survives.

### 12.3 The libretto holds a COPY, not a reference

The libretto is the observed form's state, **materialized**: the projected
subset, held as ordinary keys, with its own patch history, plus the
bookkeeping.

```jsonc
{
  "refs":  3,                      // figaros studying this form
  "paths": {"brief": true, "status": true},   // the union of what subscribers asked for
  "alive": true,                   // false once the source is deleted

  "brief":  "ship the thing",
  "status": "merged"
}
```

An earlier draft had it recording INTERVALS into the source's history. That
is broken, and the reason is worth keeping: the translator would have to read
the source's patches inside those ranges, so a studied form could never be
deleted while a libretto named a range in it, and any retranslation would
demand those records still exist. Refcounting and retention coupling through
the back door. Derived state is a COPY; deriving a pointer is not derivation.

**What holding the copy buys:**

1. **The translator never touches a source form.** It reads
   `PatchesBetween(prev, cur]` on the libretto, the same code path the board
   already uses.
2. **Source forms become freely deletable.** The libretto records the death,
   sets `alive: false`, and stops listening. The copy remains, so history
   still renders.
3. **The render special cases become ordinary state.** The begin-mark
   baseline exists because the first window is `(0, V]` and folds to a whole
   form; now it is simply the patch that created the keys.
4. **The libretto may not be compacted**, because the translator takes
   `PatchesBetween` views of it. Same type-level rule as a board.

**NOTE, for when retention lands:** with the refcount already here, a
libretto can later hold LT RANGES into the source instead of a copy, and
retention on the source can be driven by the ranges its librettos still
name. That removes the duplication entirely and is deliberately not built
now, because it requires the retention machinery to exist first.

### 12.4 The write path, in order

- **`study`** patches the libretto (refs, paths, subscribe) then the board's
  `study-set`. See §12.2.1 for why that order.
- **The libretto's actor subscribes** to its one source form, folds incoming
  patches through `paths` into its own keys, hears a tombstone and records
  it, syncs, publishes.
- **The figaro reads the libretto** at each IR record and stamps its version.
  It never writes it on that path (§13.1).

### 12.5 What the IR stamps, corrected

Because librettos are per-form, an aria observing N forms stamps **N
cursors**, one per libretto: the same shape `StudyVersions` has today,
pointing at librettos instead of sources. The "one cursor" claim in an
earlier draft was a consequence of the per-figaro libretto and dies with it.

The ambiguity that used to haunt those stamps (absence meaning "not
studying" versus "could not read") is now answered elsewhere: **the observed
set is derivable from the board's own `study-set` at the board version the
record already stamps.** The stamp says where each libretto stood; the board
says which ones were being observed. Neither has to encode the other.

### 12.6 Persistence and retention

Derived forms are **fully persistent, no retention limit, for now**. The
option to configure one exists (§10) and is not exercised. Only indexed
forms and the topology form take a retention policy today.

### 12.7 Allocation, and what "tree surgery" means

`form.Snapshot` is an immutable AVL with structural sharing: `Clone` is the
identity function, and `Apply` copies only the O(k log n) nodes on the paths
a patch touches. Two ways to build a derived value from a source value:

- **Marshal and unmarshal**: serialize the selected keys to JSON, parse them
  into the derived form's tree. Allocation proportional to the BYTES
  selected, on every event. On the measured `large` fixture (5k keys) a full
  copy is 2.0 ms and 12.1 MB; on a real board, 3.7 µs and 18 KB.
- **Tree surgery**: the derived tree points at the SAME immutable value nodes
  the source holds, because a presence-only derivation is literally a subtree
  selection. Allocation proportional to the nodes on the touched paths, which
  is what `Apply` already costs.

**Where it becomes perceivable:** a small board with one observer, never. A
2 MB board, or the fifty-observer storm the study benchmarks already model,
immediately: 50 × 12 MB per patch versus 50 × a few hundred bytes.

It has to be the design from the first line, because it is very hard to
retrofit once the derivation speaks JSON internally.

### 12.8 What was BUILT, and four corrections to the sections above

Built in session 3 (aria d604c755): the libretto itself, its fold, its
refcount, the reconciliation sweep, `doctor librettos`, the study/drop verbs
on both halves, and fork/kill as participants. Not built: the projection
switch (§12.5) and reclamation. Corrections, in the order they were found:

1. **A verbatim mirror must not copy the source's TOMBSTONE.** §12.3 says the
   libretto records the death and keeps the copy. It does — but a form SEALS
   itself when it carries `system.tombstone`, so mirroring that key made the
   libretto commit suicide the moment its source died, which is the precise
   opposite of the copy outliving it. The death is recorded as
   `system.libretto.alive`, which is a fact ABOUT the source rather than an
   instruction to this form. Bookkeeping lives under `system.libretto.*` for
   the same class of reason: a whole-form mirror copies arbitrary board keys,
   and a board may legitimately hold one called `refs`.

2. **`refs == 0` is necessary and NOT sufficient for reclamation**, which
   §12.2 implies and never says. An IR record stamps the libretto version it
   was rendered against (§12.5), so an aria that studied a form and dropped
   it still references that libretto for the whole of its history. Unlinking
   on refs==0 makes those records unrenderable — the coupling the COPY exists
   to remove (§12.3), reintroduced from the other end. Reclamation needs a
   second question ("does any surviving IR reference it") and a ruling about
   what an old transcript may lose.

3. **§12.2.2's list is written in terms of VERBS and must be read in terms of
   ENTRY POINTS.** "Fork" is three functions here (`Fork`, `ForkAt`,
   `ForkWith`), and a live `fig fork` takes the third. Putting the refcount
   on the first two passed every unit test — because the tests called what
   the tests chose — and under-counted on a real daemon, which is §12.2.2's
   own unrecoverable direction reintroduced by an incomplete fix to it. The
   rule is better stated as: every code path that gives a board a COPY, or
   takes one out of existence, is a participant.

4. **The stamps are durable history, so §12.5 is a MIGRATION, not a switch.**
   Every IR record ever written by a studying aria carries source-form
   versions under `study:`. Reinterpreting them as libretto versions reads
   every one against the wrong log — silently wrong ranges rather than absent
   ones, made permanent by the per-LT cache. A second cursor namespace lets
   old records keep their meaning; the projection then carries both paths,
   and the accessor map can be keyed so that no new field is needed on
   `IncrementalProjection` (the seam that has eaten two fields already).

## 13. Inconsistencies found while writing this down

Recorded because each was a real contradiction between two turns of the
design, and the resolutions are load-bearing.

1. **The libretto cursor write versus fsync-before-publish.** The form in
   question is **the libretto**, and the hot path is the figaro's drain loop
   at each IR record.

   The trap: if the loop WRITES the libretto on that path and the write now
   syncs, every IR record costs a libretto fsync. Make the write async to
   avoid that, and a worse thing happens: the IR record stamps a libretto
   version that is not yet durable, so a crash leaves the IR pointing at a
   version that never existed. That is an ordering violation ACROSS two
   forms, and neither form can detect it alone.

   **Resolution: read-then-stamp, never write-then-stamp.**

   The loop *reads* the libretto's published version and stamps that number
   into the IR record. Because publish follows fsync, **any version the loop
   can observe is already durable**, so the IR can never reference something
   that is not on disk. The stamp is a read of an atomic pointer: no queue,
   no sync, no wait.

   The libretto's own updates (extending an interval as a source moves) are
   driven by the libretto's actor from its subscriptions, not by the figaro's
   loop, and they are ordinary batched patches. The two never need to be
   ordered against each other, because the only thing the IR asserts is "at
   this record I had observed the libretto at version V", and V was durable
   when it was read.

   Generalised: **a cross-form reference may only name a version the
   referrer has observed as published.** Publication implies durability, so
   the reference is safe by construction and no cross-form protocol is
   needed.

1. **"Validation inside the topology loop retires the `deleting` lock" was
   too strong.** A delete is filesystem repair (detach, unlink) plus a
   topology patch. The actor owns only the second. The `deleting` lock still
   guards the filesystem half; what the actor removes is the TOCTOU on the
   topology half.
1. **`SubscribeFrom` does not belong in the queue at all**, and the first
   draft put it there for the wrong reason. I routed it through the writer
   because registration was going to take a DURABLE lease, and a durable
   registration is a write. Two things changed:

   - Leases became **in-memory and best-effort** (§7), because the libretto's
     intervals record what was observed, so nothing durable needs to count
     readers.
   - The snapshot race, which was the other reason, is closed by ORDERING
     rather than by serialization: **register first, then read the
     snapshot**. An event landing between the two is delivered *and* present
     in the snapshot, which is a duplicate; the subscriber drops events at or
     below its snapshot version. Do it the other way and the same event is in
     neither, which is a gap. Duplicates are recoverable, gaps are not.

   So the subscriber set is an `atomic.Pointer` to an immutable slice, swapped
   by CAS, in its own concurrency domain, with no lock and no queue: exactly
   the shape you asked for.
1. **Rejecting removals of absent keys breaks birth dressing.** `-D` on a
   birth patch means "do not inherit this" and may name a key the parent
   closure lacks. Resolved by the `assert` / `ensure` intent in §4.2.
1. **"Every command gets an answer" versus "a patch that changes nothing is
   not an event."** Both are right, about different layers. Resolved by
   splitting the log rule from the protocol rule (§4.1).
1. **Compaction versus zero-copy patch views.** `PatchesBetween` returns a
   view into an immutable array whose safety rests on append-only. Resolved
   by the type-level rule in §8.
1. **A panicking commit sink now takes down the caller**, since main moved
   sinks off the actor goroutine and onto the caller under the write lock.
   Two lines of `recover` around the sink loop, and it belongs there
   regardless of the rest of this plan.
1. **The dot-prefix protection class is dropped** in favour of the schema
   alone (§5), which also retires the `.id` rename and its migration.

## 14. What this costs

1. **Every append now syncs**, on every channel. A form patch goes from a
   buffered memcpy (~5 µs) to a real fsync (~50 to 200 µs), and an IR record
   pays the same. `fig set`, every mantra update, every message of every
   turn. This is the cost of the WAL being a WAL, it is deliberate, and it
   is the number to watch above all others. The recovery is batching, and
   after that preallocation plus `fdatasync` (§3.3).
1. **Contended throughput improves**: group commit amortizes the fsync and
   takes figwal's per-lineage lock once per batch rather than once per patch.
1. **Goroutines drop for idle forms** and do **not** drop for blocking
   callers, who still park. `Submit` without `Await` is what removes those.
1. **Derived forms cost a copy per observed value** unless they share tree
   structure with the source instead of re-encoding it. Defined, with the
   numbers and the scale at which it is perceivable, in §12.7.
1. **The topology form serializes globally.** Rare writes, free reads,
   partitions later.
1. **Retention introduces no new pause.** See §10: it is a policy on how
   many sealed segments to keep, not a distinct operation, and the fold it
   depends on is written by the ordinary roll every reducible channel already
   performs.
1. **Deferred reclamation holds disk** for tombstoned forms with live leases.
1. **Validation costs a lookup and a branch per key**, on one-to-three-key
   patches. "Validation" here means the schema check of §5: is this key
   system-managed and is this caller privileged, is the value the declared
   shape, and is an `assert` removal naming a key that is actually there. A
   map lookup and a comparison, in the writer, before the reduce.

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
1. **The lazy actor** in `internal/actor`, exit race handled and tested once.
1. **Group commit and sync-before-publish**, plus
   `XWAL.SyncChannelThrough`. Plus the sink `recover`.
1. **Command/event/ack on the wire**: `session`, `seq`, intent, and the
   acknowledgement of a no-op. The whole server side of optimistic
   replication, worth having before any replica exists because it also fixes
   the silent-no-op ambiguity.
1. **Schema validation** in `commit`, `KeySystemManaged` enforced.
1. **Tombstones and leases.**
1. **Segment normalization**, with the type-level rule.
1. **The topology form**, replacing `trunks.json`, with its migration.
1. **Derived forms**, libretto first, `study` as an alias.
1. **The API refactor** (§16), before the surface grows by the methods the
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

| prefix     | means                                       | examples                                      |
| ---------- | ------------------------------------------- | --------------------------------------------- |
| `figaro.`  | sometimes *sent to an aria*                 | `figaro.qua`, `figaro.set`, `figaro.study`    |
| `figaro.`  | sometimes *about arias, sent to the daemon* | `figaro.create`, `figaro.list`, `figaro.kill` |
| `aria.`    | *about an aria, answered from the store*    | `aria.form`, `aria.context`, `aria.read`      |
| `angelus.` | the daemon itself                           | `angelus.status`, `angelus.outfits`           |
| `form.`    | forms, three of them only                   | `form.create`, `form.bind`, `form.delta`      |
| `pid.`     | shell bindings                              | `pid.bind`, `pid.resolve`                     |
| (none)     |                                             | `turn.done`, `outfit.reload`                  |

So the prefix is sometimes the SUBJECT, sometimes the RECIPIENT, and in the
largest namespace both at once. There is no rule a newcomer could infer,
which is why every addition has been a coin flip.

Four concrete symptoms:

1. **The same question has two names**, chosen by where the state happens to
   live: `figaro.form` / `aria.form`, `figaro.context` / `aria.context`,
   `figaro.read` / `aria.read`. Residency is the implementation detail the
   hub exists to hide, and it already hides it for writes.
1. **Nouns and verbs are mixed with no convention.** `figaro.qua` and
   `figaro.set` are verbs; `figaro.form` and `figaro.queued` are nouns with
   an implied get. You cannot tell whether `figaro.form` reads or writes
   without opening the handler.
1. **The noun sometimes lies.** `figaro.kill` also deletes forms.
1. **The wire types are the internal types.** `type FormPatch = message.Patch`
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
form.get          form.patch        form.submit        form.await
form.subscribe    form.unsubscribe
aria.prompt       aria.interrupt    aria.read          aria.context
aria.queue.list   aria.queue.update aria.queue.delete
```

`form.submit` returns a ticket and does not wait; `form.await` blocks on
one; `form.patch` is the pair, and a client is free to implement it locally
rather than spend a third round trip. An optimistic client uses `submit`
plus the event stream and never calls `await`.

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

| today                                                              | becomes                                                                                             |
| ------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------- |
| `figaro.form`, `aria.form`                                         | `form.get`                                                                                          |
| `figaro.context`, `aria.context`                                   | `aria.context`                                                                                      |
| `figaro.read`, `aria.read`, `aria.page`                            | `aria.read`, one paging shape                                                                       |
| `figaro.set`                                                       | `form.patch`                                                                                        |
| `figaro.study`, `figaro.drop`                                      | `form.patch` on the libretto (§12)                                                                  |
| `figaro.cast`                                                      | stays a verb: it is two writes across two nodes                                                     |
| `figaro.create`, `form.create`, `form.bind`                        | `node.create` with kind + parent                                                                    |
| `figaro.kill`                                                      | `node.delete`                                                                                       |
| `figaro.queued`                                                    | `aria.queue.list`                                                                                   |
| `figaro.gc`, `figaro.normalize`, `figaro.import`, `figaro.promote` | `node.*`                                                                                            |
| `figaro.attach`                                                    | `node.attach`, and the only method that hands out an endpoint, so also where role redirection lands |
| `pid.*`                                                            | `shell.*`                                                                                           |
| `angelus.info`                                                     | folded into `angelus.hello`                                                                         |

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

Settled since the first draft, recorded so they are not reopened:

- A delete of an absent key is **rejected** under `assert`, reduced under
  `ensure` (§4.2).
- The dot-prefixed reserved path is **dropped**; protection is the schema
  (§5).
- Command ids are **monotonic per session** (§4.1).
- A derived form is **one form with one actor**, not a compound (§12.1).
- **One libretto per studied FORM**, shared, refcounted, and it does **not**
  fork (§12.2). Two earlier drafts said otherwise; this one is final.
- The libretto holds a **copy** of the projected state, not intervals and not
  references (§12.3).
- The IR stamps **one cursor per observed libretto**, and the observed SET is
  derivable from the board's own `study-set` at the board version the record
  already carries (§12.5).
- **Study is a two-participant write**, made safe by ordering rather than by
  two-phase commit: every crash over-counts, and a reconciliation sweep makes
  the leak finite (§12.2.1).
- **figwal becomes a true WAL**: the lag buffer goes, every append syncs
  before it is visible, a failed sync rejects the patch (§3.3).
- Leases are **in-memory and best-effort** (§7).
- Subscription is **register-then-snapshot**, lock-free, outside the queue
  (§6.1).
- Retention replaces "compaction" and introduces **no new pause** (§10).
- The topology form **always exists**, has an angelus lifetime, and is
  outside the graph it describes (§11).
- Derivations may subscribe only to **primary forms** (§12).
- The translator cache is **retained, never compacted, tail-truncated only**
  (§8).

Still open:

- **The union projection** (§12.3 `paths`): two figaros studying one form
  with different `-P` sets share one libretto, so it must hold the union and
  each figaro filters on read. Where the per-figaro path set lives is the
  last undecided piece of the `study-set` shape. `[q13]`
- **How `libretto::<formid>` resolves to a node.** `[q14]`
- **`agent.mu` and `restoreLocks`**: fast-follow, documented in
  `plans/lock-audit.md`.
