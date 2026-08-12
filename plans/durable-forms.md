# Durable forms: the actor, the patch, and what a delete owes the log

**Status: designed, not built.** This is the merge of two threads (the lazy
actor and group commit; derived forms and the libretto) plus the topology
form already sketched in
[contributing/trunk-singleton-form.md](../skills/figaro/contributing/trunk-singleton-form.md).
Read that one first; this supersedes its concurrency section and keeps its
document shape, its reserved node and its migration.

## 0. Vocabulary

**Patched**, not committed. A patch is the unit. Each patch yields a
consistent state, and every operation that changes a hierarchy must fit in
exactly one patch, because a pair of writes that can half-land is a state
nobody designed.

"Group commit" survives as a name for one fsync covering several patches,
because that is what the literature calls it and inventing a word for it
would cost more than it saves.

## 1. Base principles

1. **One writer per form.** The inbox is the serialization. Not a mutex, not
   a convention: a queue with exactly one drainer.
2. **Durable before visible.** A patch reaches disk, and is synced, before it
   reaches the published state. The reverse is not a lost write but a
   hallucinated one: the model is shown the state as a reminder, so a crash
   would make it act on something that never happened.
3. **Reads are lock-free.** The live state is an immutable AVL root behind an
   atomic pointer. A reader loads two words. Reading never blocks a writer,
   never wakes anything, and never serializes behind a turn.
4. **A patch that changes nothing is not an event.** Reduced against the
   published state, in the writer, atomically with the append.
5. **Absence is the truthful default.** State that describes other state
   (the topology map, a derivation spec) holds OVERRIDES, never a full
   picture, so a lost document degrades to the truth rather than to a lie.
6. **Forking never consults presentation.** A presentation edge must never
   decide where data comes from. `internal/topo` says it; nothing here
   changes it.
7. **Deletion is a record, not an event.** A subscriber that was offline
   must be able to learn about it, and a replay must reproduce it.
8. **History is observable unless the form declares otherwise.** The
   exceptions are enumerated in §4 and each one has to earn it.

## 2. What a delete does to the log, and why it must

This is the operation that normalizes the log's arrangement, and nothing
else in the system does. It is worth understanding completely before
anything is built on top of it.

**The arrangement.** A forked node does not own its whole history. Its
records `[1, base)` physically live in its ancestor's segment files, and it
reads through them: `.from` names the parent, and a per-channel `.fork`
gives the base index above which the node owns its own records. This is why
a fork is cheap and why one rendered prefix is shared by a whole family.

**The problem a delete creates.** Unlinking a node's directories takes away
files that surviving descendants still read through. The survivors are not
in the delete set and did nothing wrong.

**The repair, in order.**

1. **Refuse first.** `RemoveLeaf` computes the delete set from the DRAWN
   tree and refuses a non-recursive delete of something with branches
   before touching anything, because the boundary repair below rewrites
   surviving arias and cannot be taken back.
2. **Find the boundary.** `topo.Boundary(adjacency, deleteSet)`: survivors
   outside the set whose lineage runs through it.
3. **Detach each one** (`Trunks.Detach`). For every channel whose
   `ForkBase() > 1`, copy `[1, base)` out of the ancestor chain into the
   node's own directory as **one segment based at 1**. One, not many: a
   reducible channel's segment header carries the folded state at its
   start, and only a segment based at 1 can honestly carry the INITIAL
   state. Chunking would have to re-fold a watermark per chunk.
4. **Publish the detach by rename**, `.fork` per channel first and the node
   marker (`.from`) LAST. That order is load-bearing. A crash between two
   channels' flips leaves each channel individually correct, because
   `.from` still names the parent: a flipped channel reads its own absorbed
   copy, an unflipped one still delegates, and the two are byte-identical.
   Clearing `.from` first would strand every channel not yet flipped.
5. **Only then unlink** (`removeLocked`). A crash between 4 and 5 leaves
   survivors reading through directories that are still present, which is
   correct and merely wasteful.
6. **Repair presentation** with the store lock released: `Forget` every
   edge naming a doomed node, `Reparent` each survivor to a home that
   outlives the delete. Without this a survivor falls back to the topology
   and lands under the genesis root with no outfit, which is the fossil
   this path exists to stop making (109 of 503 conversations in the real
   store, before the figwal recursive-remove fix).
7. **Collect the stump** only if nothing is left wearing it.

**Crash-safe with no journal**, by ordering alone: absorbed records are
written below the node's own fork base, where every read still delegates to
the ancestor and so cannot see them. The rename is the only thing that
publishes them. Idempotent: a re-run overwrites the ignored files.

**Two consequences for everything below.**

- **Detach is already a compaction.** It folds a prefix into one segment
  and publishes by atomic rename. Segment normalization for the topology
  form is the same operation with a different trigger, and figwal's own
  comment already calls packing "segment normalization's job, and it is
  deferred". We are commissioning that.
- **Reclamation is not immediate and never was.** A delete already defers
  stump collection until it knows where survivors land. Deferring node
  reclamation until subscribers have advanced (§7) is the same shape, not a
  new kind of debt.

## 3. Durability, and why no patch needs to be reversible

The question was: do we need reverse patches, so that a failed fsync can be
rolled back?

**No, and the current code is already ordered correctly.** `Form.commit`
does, in this order:

```go
applied := effectivePatch(st.snap, w.patch)   // pure: reads state, mutates nothing
payload, _ := json.Marshal(applied)
version, err := f.log.AppendPatch(payload)    // durable half
next := &formState{snap: st.snap.Apply(applied), version: version, ...}
f.state.Store(next)                           // visible half
```

The reduce is a pure function of the published state. Nothing is mutated
until after the append returns. So the only change required is an fsync
between the append and the store, and a failure at any point before the
`Store` leaves the published state exactly as it was. **There is nothing to
roll back because nothing has been applied.**

This is worth stating loudly because reverse patches are a whole subsystem
(derive an inverse from an AVL diff, keep it alive across the window, apply
it under failure, test the failure) and the ordering makes them unnecessary.

**What is missing today is only the fsync.** figwal's `Log.Write` buffers
and syncs when pending bytes exceed `maxLag`, whose default is **64 MiB**. A
form patch is a few hundred bytes, so nothing syncs on the write. The
comment in `store.Form` that says durability precedes visibility is true of
ORDERING and false of DURABILITY, today.

**figwal needs one method**: `XWAL.SyncChannelThrough(channel string, idx
uint64) error`. `Log.SyncThrough` already exists; `XWAL` exposes only `Sync`
and `SyncCoherent`, and the latter syncs the main channel first and bounds
related channels to the durable main tail, which would couple a form's
durability to the IR's.

**Group commit is what makes it affordable.** An fsync is roughly 50 to 200
microseconds. Per patch, at 256 concurrent writers, that is tens of
milliseconds serialized. Per batch, 256 patches cost one fsync. The actor is
what makes a batch exist.

## 4. Every form, and what it owes

| form | writer | history observable | compaction | durability |
|---|---|---|---|---|
| **aria board** (bound form) | the aria's actor | **YES**, load-bearing | forbidden | sync before publish |
| **unbound form / role** | its own actor | **YES**, load-bearing | forbidden | sync before publish |
| **outfit node / default form** | birth only | irrelevant (one patch) | unnecessary | sync at birth |
| **topology form** (singleton) | its own actor | **no** | **required**, single segment | sync before publish |
| **derived form: spec** | its own actor | no | desired | sync before publish |
| **derived form: values** | its own actor | no | desired | derived; relaxed is defensible |
| **translator cache** | provider | no | already invalidated wholesale | none (regenerable) |

**Why the first two forbid compaction, precisely.** The projection renders a
board's transitions BETWEEN two stamps: `PatchesBetween(after, upTo]` is
answered as a zero-copy view into the published patch array, and a studied
form's window is folded from the same. Both are re-derived on every
retranslate. A compacted channel cannot answer what changed between two old
versions, so a retranslate would render a different context than the first
pass did, and the per-LT translation cache would make the disagreement
permanent. **History is the value for these two.**

**Why the topology form does not need history.** Nobody will ever ask what
the hierarchy looked like on Tuesday. Only the fold is the value. Left
uncompacted it grows one record per promote forever and every daemon start
replays them all to answer a question two map writes could have answered.

**The rule, as a property rather than a promise:** a form may compact only
if it hands out no patch views and nothing renders its history. That must be
enforced by the type, not by a comment, or someone adds a `Patches`
accessor to the topology form in a year and the safety argument evaporates
silently.

## 5. The actor

One structure, in `internal/actor`, reused by all three customers.

**Lifecycle.** Three states on one atomic word:

```
idle       no goroutine exists
running    a goroutine is draining
lingering  drained, waiting out the affinity window
```

- **Submit**: enqueue, then `idle -> running` (CAS wins: spawn) or
  `lingering -> running` (CAS: signal, no spawn) or `running` (nothing).
- **Drain**: take up to N (cap ~64), patch them in turn, one fsync,
  publish, advance the ticket, emit to subscribers, repeat until empty.
- **Linger**: `running -> lingering`, wait on a signal or a timer.
- **Dormant**: timer wins, `lingering -> idle`, exit. **Then re-check the
  queue**, and if it is non-empty CAS back to `running`. A submit landing
  between the emptiness check and the state change is the unique failure
  mode of this design, and it presents as a hung `fig set` that unhangs on
  the next write. Test it before writing it.

**Blocking and non-blocking on one mechanism.** No reply channel per call:

```go
type Ticket uint64
func (f *Form) Submit(patch, ifVersion) (Ticket, error)   // never waits
func (f *Form) Await(t Ticket) (uint64, Patch, error)     // caller's goroutine
func (f *Form) Apply(patch, ifVersion) (...)              // Submit + Await
```

The worker keeps `patched atomic.Uint64` and a broadcast tick (a
`chan struct{}` closed and replaced at each publish). `Await` reads the tick,
checks `patched >= mine`, waits on the tick, repeats. **No goroutine per
call, no channel allocated per call**, one broadcast serving every waiter.
The result rides the caller's own submitted struct, filled before the
release-store of `patched`.

Because the ticket advances only after the fsync, a blocking `Apply` returns
when the patch is DURABLE. That is what makes `fig set -j` trustworthy to a
script.

**Dormancy of the handle too.** The actor going idle should release the xwal
handle, which figwal already unloads on its own schedule (`IdleUnload`). See
§8: these must become one policy.

## 6. Subscription, and exactly where it is needed

```go
// Registered INSIDE the writer, atomically with the read.
func (f *Form) SubscribeFrom(after uint64) (Snapshot, uint64, <-chan Patched, func())
```

**The semantics.** You receive the state as of version V, together with a
stream of every patch after V, and no patch between them can be lost or
duplicated, because the snapshot and the registration happen at the same
serialization point. Any implementation that reads the snapshot and then
subscribes has a window in which a patch lands and is neither in the
snapshot nor in the stream. That window is not hypothetical; it is the
default outcome of the obvious code.

**Who needs it, concretely.**

1. **Derived forms.** A derivation subscribes to each form in its spec. On
   daemon start it resumes from its last folded version; the durable cursor
   is what makes a restart cheap rather than a full re-fold.
2. **The libretto**, as the first derived form: it hears a studied form
   change and a studied form die.
3. **`fig form listen`.** Today the client reads a snapshot and then
   attaches to the delta fanout, which is exactly the race above. Nobody has
   reported it because the window is small and the consequence (a mirror one
   patch behind, forever) is silent.
4. **The node cache.** Main keys it on both figwal's topology version and
   the presentation revision, because a promote moves no bytes. A
   subscription to the topology form replaces that polling with an
   invalidation.
5. **Remote form runtimes**, later. The catch-up cursor is the thing that
   makes a reconnect cheap, and it is the same mechanism.

**Slow subscribers.** The worker must never block on a consumer. Per
subscriber, buffered; on overflow, drop and set a `missed` count, and
deliver a `Resync` marker when space frees. A silent drop is unacceptable
because a mirror that misses one patch is wrong forever without knowing it.

## 7. Deletion: tombstone, refcount, reclamation

**The tombstone is state, not an event.** A final patch on the dying form's
own channel sets a reserved key, after which the channel is sealed and every
further patch is refused. Subscribers hear it through the same stream as any
other change: no second delivery path, no second durability story, and a
subscriber that was offline reads it on resume.

**Refcounting, per your design.** Subscribing increments a durable counter
on the form; unsubscribing decrements it. A tombstoned form with zero
subscribers is reclaimable. Delete becomes: tombstone now, unlink when the
last reader has gone.

**The footgun this creates, and it is a real one.** A subscriber that dies
without unsubscribing (a crashed daemon, a killed remote node) never
decrements, and the form is never reclaimed. A durable counter cannot tell
"still reading" from "died holding a reference".

The fix is a **lease, not a counter**: a subscriber registers with an
identity and an expiry, renews while it lives, and a sweep drops expired
registrations. Within one daemon the identity can be the process instance,
so every registration from a previous instance is provably dead at start and
cleared in one pass. That degenerates to your counter in the single-process
case while remaining correct when the system is distributed, which is the
direction you have said this is going.

**Interaction with §2.** Deferred reclamation composes with detach: a
tombstoned node whose survivors have not yet detached must not be unlinked
anyway. Two reasons to defer, one mechanism to wait on.

## 8. Configuration: one policy, three enforcement points

There are three idle clocks today and they do not know about each other:

| clock | where | default | configured today |
|---|---|---|---|
| agent + cache reclamation | `EvictIdle` | 15 min | `[memory] dormant_after_minutes` |
| figwal head unload | `xwal.Store` `IdleUnload` | 5 min | **no** |
| actor linger | new | ~2 s | **no** |

One policy in `[memory]`, three enforcement points derived from it:

```toml
[memory]
dormant_after_minutes = 15     # agents and their caches (exists)
handle_idle_minutes   = 5      # figwal head unload (new; wire the knob figwal has)
actor_linger_ms       = 2000   # the writer goroutine's affinity window (new)
```

In-binary defaults must be reasonable without any config file, since that is
the state of the author's own config today (`default_outfit` and nothing
else). The test to write is the one that would have caught this class:
**a config file supplying all three, read back through the loader, asserted
at each of the three enforcement points**, not just at the parser.

`[store] segment_size` (default 2 MiB) gains a companion for compacting
channels, which want a much smaller segment so a roll is cheap: see §9.

## 9. Compaction, stated as segment normalization

**What it is:** replace a channel's segments with one segment holding the
folded document. Not truncation in place. `absorbPrefix` already does
exactly this shape for detach: write the new segment, fsync, rename, fsync
the directory. Either the old segment or the new one is present, and both
fold to a correct document.

**When it runs:** on roll. Tune the segment small enough that a roll happens
often and each one is cheap. When a segment fills, the writer serializes the
whole tree into a fresh segment before continuing, which is a pause
proportional to the document, not to history.

**Why the pause is acceptable for the topology form and not for a board:**
the topology form is a different concurrency domain from every aria, its
writes are bursty and rare, and its document is a small map. A board is
written mid-turn on the hot path.

## 10. The topology form

Renaming `@trunks` to something that says what it is: **the topology form**,
`@topology` as the reserved node name. Singleton for now, one per store,
which is one per angelus because the angelus owns the store.

- **Writes are one patch**, always. A promote is two keys in one record,
  which is what makes it atomic; today it is one `save()` of a whole file
  and the pair can half-land.
- **Validation happens inside the loop**, in the same critical section as
  the patch. Today `promote` reads topology, decides, and writes overrides
  in separate steps, which is a TOCTOU against a concurrent promote or a
  delete's `Forget`. Main had to add a store-wide `deleting` lock to stop
  deletes interleaving; validating inside one loop makes that lock
  redundant rather than adding a second one.
- **Reads are free**: the folded tree is an AVL root behind an atomic
  pointer, exactly like a board's snapshot.
- **History is not observable**, single segment, compaction required.
- **Never listed**, never forked, never bound. It must be excluded from
  `Conversations()` and from `ls -g`'s form rows, or the tree grows a row
  describing itself.
- **Bootstrap is safe because absence is truthful**: a store whose topology
  form has not been resolved yet renders where history puts things, which
  is right, not wrong. That property is what keeps the cycle (the listing
  needs the tree, the tree lives in a node in the store) from being a
  deadlock.
- **Partitioning later**, if one writer becomes a bottleneck: one form per
  child of the null root, or per child of null-or-outfit-stump. The
  document shape (overrides keyed by aria id) partitions cleanly because an
  edge names only its two endpoints.

## 11. Derived forms, and the libretto as the first

A derived form is a **compound**: two forms, two concurrency domains.

- **The spec**: form id to observed subtype, a JSON tree of subscribed
  fields. Presence-only grammar for now:
  `{"@abc": {"brief": "this", "status": "this"}}`. An expression language
  later.
- **The values**: the same shape, holding values instead of expressions.
- **The derivation**: an actor with two producers, the incoming patches
  from its subscription set and the patches its own spec emits.

Two forms because the spec is user intent and the values are machine
output: different writers, different lifecycles, and in the protection
taxonomy, different classes. One form would mean two writers on one node and
the whole model collapses.

**`study` becomes an alias** for setting `{"@formid": "this"}` on the
libretto, and `drop` a key removal. Both are ordinary patches through an
ordinary writer. That deletes a large amount of bespoke machinery:
`study.go`'s board list, `study_hub.go`'s dormant half, and the
`SetObservedForms` mirror.

**Self-modifying derivations need a rule now.** A derivation that subscribes
to its own output is a feedback loop; harmless with presence-only grammar,
oscillating with an expression language, and it will oscillate at 3am inside
an actor loop nothing is watching. Refuse it structurally at subscribe time.
Do not build a fixpoint evaluator with a step cap.

**Allocation.** Three copies of every observed value (source snapshot,
derived tree, the figaro's read) plus the spec tree, where a study costs
zero copies today because the projection reads the source directly. The
mitigation must be a stated goal from the start: `form.Snapshot` is an
immutable AVL where `Clone` is the identity, and a presence-only derivation
is literally a subtree selection, so write it as tree surgery, not as
marshal-and-unmarshal. It is very hard to retrofit once the derivation
speaks JSON internally.

## 12. The RPC surface: consolidation

The rule that settles almost everything, in your own words: **the node
socket is the object's interface; the angelus is the registry and the
topology.** If a call is a message to one existing node it belongs on that
node's socket. If it needs to know about more than one node, or about a node
that does not exist yet, it belongs to the angelus.

**The duplicates that die.**

| today | becomes |
|---|---|
| `figaro.form` + `aria.form` | `form.get` on the node, `wake: never` |
| `figaro.context` + `aria.context` | `aria.context` on the node |
| `figaro.read` + `aria.read` + `aria.page` | `aria.read` on the node, one paging shape |
| `figaro.set` | `form.patch` on the node (`fig set` stays a CLI alias) |
| `figaro.create` + `form.create` + `form.bind` | one `node.create` with a kind and a parent |
| `figaro.kill` (which also deletes forms) | `node.delete` |

**The exception, which the rule already covers:** bulk reads stay on the
angelus, because a listing touching 600 boards must not stand up 600
sockets. That is "more than one node".

**The wake property replaces `MethodNeedsAgent`.** Node calls carry
`wake: never | if-needed | always`, defaulted per method. Reads default to
never; prompt and interrupt to if-needed; `form.patch` and `form.subscribe`
to never, because after this redesign they genuinely never need the loop.
The three-case switch that was wrong once, and cost us `study_hub.go`, goes
away.

**Naming rule going forward:** `<subject>.<verb>`, subject in
{`aria`, `form`, `node`, `outfit`, `shell`, `angelus`}, verb always
explicit. Old names alias to the same handlers forever.

**Version skew is the only hard part.** A new daemon that redirects or
renames unconditionally breaks an old CLI in confusing ways (see the role
redirection case: the old client re-resolves an already-resolved id and dies
saying a figaro is not a figaro). So: `angelus.hello` returning a protocol
version and a capability set, negotiated once per connection, and behaviour
changes gated on it rather than on optional request fields that can never be
removed.

## 13. What this costs

Stated plainly, because most of it is a regression:

1. **Solo write latency.** A form patch goes from a buffered memcpy (~5 µs)
   to a real fsync (~50 to 200 µs). `fig set`, every mantra update, every
   study. Interactively invisible; it is the number to watch.
2. **Throughput under contention improves**, because group commit amortizes
   the fsync and takes figwal's per-lineage lock once per batch instead of
   once per patch.
3. **Goroutines drop for idle forms** (the actor exists only under load)
   and do NOT drop for blocking callers, who still park. `Submit` without
   `Await` is what removes those, and the libretto cursor write is its
   first customer.
4. **Derived forms cost three copies per observed value** unless the tree
   surgery is done from the start.
5. **The topology form serializes globally.** Promotes and deletes are
   rare, reads are free, and it partitions later if that stops being true.
6. **Compaction pauses the topology writer** for the length of one document
   serialization, on roll.
7. **Deferred reclamation holds disk** for tombstoned forms with live
   subscribers.

**What to measure, before anything:**

- `BenchmarkFormApplyContended`, W in {1, 8, 64, 256}, one form: ns/patch,
  allocs/patch, and **fsyncs per patch**, which is the number that proves
  group commit works.
- Solo latency at W=1, which is the interactive case and the one that
  regresses.
- `BenchmarkFormApplyManyForms`, to prove domains stay independent.
- Peak goroutines, the number the mutex change was made for.
- A soak asserting no submit is lost across a spawn/exit boundary.
- The twelve-aria live recipe, before and after, with PSS and swap.

## 14. Build order

1. **`SubscribeFrom` inside the writer.** Small, precise, everything stands
   on it, and it fixes `form listen`'s existing race on its own.
2. **The lazy actor** in `internal/actor`, with the exit race handled and
   tested once.
3. **Group commit and sync-before-publish** in `store.Form`, plus
   `XWAL.SyncChannelThrough` in figwal.
4. **Deletion tombstones and leases.**
5. **Segment normalization** (compaction) as a channel option, with the
   type-level rule that forbids it where views are handed out.
6. **The topology form**, replacing `trunks.json`, with its migration.
7. **Derived forms**, with the libretto first and `study` as an alias.
8. **The RPC consolidation and `angelus.hello`**, which should land before
   the surface grows by the five to eight methods the above adds.

Steps 1 through 3 are worth having even if 4 through 8 never happen.

## 15. Open rulings

- Does the topology form reuse `store.Form` or get its own type? Own type
  argues for compaction and no views as properties rather than promises;
  sharing argues for one commit protocol. Depends how good step 2 is.
- Does the libretto fork with its figaro? Instinct: a fork copies the SPEC
  and shares nothing else.
- Do derived VALUES get relaxed durability (they are rebuildable) or the
  same sync-before-publish? Relaxed is defensible and I lean against it,
  because what the libretto holds is what the model is shown.
- Lease TTL for subscribers, and whether a single-process deployment gets
  the degenerate counter or the full lease from day one.
