# The lock audit

Written because the standing rule is: **state lives in actors, not behind
mutexes.** This is the inventory, the rule that decides each case, and the
two that are fast-follows rather than part of the state-layer work.

## The rule

> A mutex is justified when it guards a **registry**: a map from id to
> object, read by many goroutines, mutated rarely, where the values are
> themselves independently synchronized. It is not justified when it guards
> **state**: the value of one object. State belongs to an actor.
>
> And no lock is ever held across I/O, or across anything that can block.

The second half is what the current `store.Form` violates most sharply: its
write mutex is held across a marshal, an append, and (after this work) an
fsync, plus every commit sink.

## Inventory

### Dies with the state-layer work

| lock | file | why |
|---|---|---|
| `Form.write` | `store/form.go:41` | becomes the inbox. The headline. |
| `Form.mu` (onCommit) | `store/form.go:44` | the subscriber set becomes an `atomic.Pointer` to an immutable slice, swapped by CAS: its own concurrency domain, no queue (durable-forms §6.1) |
| `XwalStore.observedMu` | `store/xwal_store.go:248` | the whole in-memory `observed` mirror dies with the libretto |
| `XwalStore.keepMu` | `store/xwal_store.go:238` | guards ONE STRING, and carries a comment explaining a lock-order inversion it exists to avoid. `atomic.Pointer[string]` removes the lock and the hazard together |
| `trunk.mu` | `internal/trunk/trunk.go:46` | dies with `trunks.json` when the topology form lands |

### Shrinks, with justification

| lock | after |
|---|---|
| `XwalStore.deleting` | keeps only the FILESYSTEM half. Detach and unlink are not form writes and still need serializing; what the topology actor removes is the TOCTOU on the topology half, not the lock |
| `cachedLog.mu` | should become the publish-a-snapshot pattern `formState` already uses: an `atomic.Pointer` to an immutable rows slice. Read-mostly, on the hot read path, and the change is mechanical |

### Justified as registries, kept

`XwalStore.mu` (the figwal handle plus the topology snapshot pointer),
`XwalBackend.mu` (forms, logs and translations by aria), `angelus.hub.mu`,
`angelus.hubs.mu`, `angelus.registry.mu`, `handlers.configMu`. Each guards a
map from id to an independently synchronized object.

`MemFormLog.mu` and `MemLog.mu` are in-memory test doubles. They survive
because they are not the real path; if either grows a second user it becomes
an actor.

---

## The two fast-follows

Both are real violations of the rule, both are bigger than the state-layer
work, and mixing either into it would make the whole changeset
unbisectable.

### 1. `figaro/agent.go:141` — `mu sync.RWMutex`

**What it guards:** `turnCtx`, `turnCancel`, the subscriber list for fanout,
live-render state (`turnStartLT` and the open unit), and metrics.

**Why it is wrong:** this is an aria's own state, guarded by a lock, sitting
next to an inbox that exists specifically to own an aria's state. The agent
already has a serialization point; it simply does not use it for everything.
Reads take `RLock` from RPC goroutines while the drain loop writes under
`Lock`, which is the exact shape the form had before this work.

**What folding it in means:**

- `turnCtx` and `turnCancel` become drain-loop-owned, and `Interrupt`
  becomes an inbox event rather than a cross-goroutine cancel. That is
  arguably better on its own: interrupt ordering against queued prompts is
  currently decided by lock acquisition, which is to say by luck.
- The fanout subscriber list becomes an `atomic.Pointer` to an immutable
  slice, exactly as the form's does.
- Live-render state is already only touched by the drain loop; it needs the
  lock today only because `Info()` reads it.
- Metrics become an atomic snapshot published by the loop.

**Why it is a fast-follow:** it touches interrupt, fanout and the live
stream, which are the three paths with the most existing tests and the most
subtle timing. It deserves its own branch, its own pty runs, and its own
before-and-after on the twelve-aria recipe.

**Risk if left:** none new. It is the status quo, and the status quo is
tested.

### 2. `angelus/protocol.go:239` — `restoreLocks map[string]*sync.Mutex`

**What it guards:** waking one aria. `restoreLock(ariaID)` hands out a
per-aria mutex so two concurrent requests do not both construct an agent.

**Why it is wrong:** a per-key mutex map is an actor per key wearing a
disguise. The hub already routes per node and already has a lifecycle per
node; the wake is the one operation that reaches around it.

**What folding it in means:** the wake becomes an operation on the node's
hub, serialized by the hub rather than by a side table, and
`restoreLocks` plus `restoreMu` both go. It fits naturally into the API
refactor (durable-forms §16), where the hub is already gaining the `wake`
property, so that is where it should land.

**Risk if left:** the map grows one entry per aria ever restored and is
never pruned. Small, but it is a leak with no upper bound other than the
number of arias.

---

## What to check when adding a lock

1. Is this a registry or is it state? State goes in an actor.
2. Is it held across I/O, or across anything that can block? Then it is
   wrong regardless of the answer to 1.
3. Could an `atomic.Pointer` to an immutable value do it instead? Publish
   and read is almost always available where the value is small and
   read-mostly, and it is what `formState`, the topology snapshot and the
   subscriber set all use.
