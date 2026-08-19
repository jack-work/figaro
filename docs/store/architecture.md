# Dependencies & layering

figwal is a strictly layered stack. Each layer depends only on the one
below it, and the public surface a consumer addresses is the *top* layer
(`xwal.Trunks`); everything underneath is plumbing.

```
  ┌─────────────────────────────────────────────────────────────┐
  │  xwal.Trunks      trunk-addressed view; disk is source of    │
  │  (trunks.go)      truth; .trunk markers; in-mem cache        │
  └───────────────────────────┬─────────────────────────────────┘
                              │ depends on
  ┌───────────────────────────▼─────────────────────────────────┐
  │  xwal.XWAL        multi-channel "triune" join; Main + reduc- │
  │  (xwal.go,        ible channels forked as one unit; genesis  │
  │   fork.go)        seeding; manifest; Config                  │
  └───────────────────────────┬─────────────────────────────────┘
                              │ depends on
  ┌───────────────────────────▼─────────────────────────────────┐
  │  disk.Log         the fork engine over segments; .fork       │
  │  (log.go,         markers; own-range vs inherited prefix;    │
  │   fork.go)        Fork / ForkRehome / ChildForkBases         │
  └───────────────────────────┬─────────────────────────────────┘
                              │ depends on
  ┌───────────────────────────▼─────────────────────────────────┐
  │  segment          append-only segment files + ValueHash;     │
  │  (segment.go,     the leaf — no deps on the layers above     │
  │   hash.go)                                                    │
  └─────────────────────────────────────────────────────────────┘
```

A consumer (e.g. the figaro coding agent) addresses `Trunks` and never
touches `XWAL`, `disk.Log`, or `segment` directly. A trunk id is the
stable handle; the per-fork node, the per-channel logs, and the segment
files are all internal mechanism the upper layers manage on its behalf.

---

## `segment` — the leaf

**File:** `segment/segment.go`, `segment/hash.go`

An append-only file framed by a `SegmentCodec`. A `Segment` owns one
file, a `baseIndex`, a count, and an in-memory offset index rebuilt by
`recover()` on open (the standard WAL recovery: scan forward, accept
clean frames, truncate any torn tail). It has no knowledge of forks,
channels, or trunks — it is the bottom of the stack and depends on
nothing above it.

Provides:

- `Create` / `Open` / `OpenReadOnly` (and `*Headered` variants),
  `Append`, `ReadIndex`, `ReadAt`, `Count`, `FirstIndex` / `BaseIndex` /
  `LastIndex`, `Sync`.
- **Block-0 header** (`WriteHeader` / `Header` / `HasHeader`): an opaque
  first record framed like any other but *not* counted in the entry
  index. This is the watermark slot the reducible model rides on; the
  segment stores the bytes verbatim and never interprets them.
- **`ValueHash`** (`hash.go`): value-stable content hashing. It
  canonicalizes JSON (object keys sorted, whitespace stripped, numbers
  preserved via `json.Number`) and returns the truncated SHA-256
  (16 hex chars / 64 bits) of the canonical form. JSON-equal inputs hash
  identically regardless of key order or formatting — the property the
  upper layers rely on to compare entries by value.

---

## `disk.Log` — the fork engine over segments

**File:** `disk/log.go`, `disk/fork.go`. **Depends on:** `segment`.

A `Log` is a directory of segment files plus the machinery to fork that
directory. It is where the fork model actually lives.

**Fork bases & the `.fork` marker.** A child fork's directory carries a
`.fork` file (`readForkMarker` / `writeForkMarker`) declaring the global
index (`base=<N>`) at which it begins; the parent is always the
immediate parent directory. On `Open`, a log with `forkBase > 0` and no
explicit `Parent` auto-walks `..` to resolve its parent chain.

**Own-range vs inherited prefix (watermarks).** A fork *owns* the index
range `[forkBase, …]`; indices below `forkBase` are inherited and served
by delegating to the parent chain (`Read`, `Range`, `HeaderAt`,
`StateAt` all walk up when `idx < forkBase`). This is the watermark
boundary: own-range entries live in this log's own segments, the
inherited prefix lives in ancestors. `LastIndex` of an empty-own fork
reports `forkBase-1` — it sits immediately after the parent's tail.
Reducible (header-mode) logs additionally carry a per-segment watermark
header computed by `OnSegmentOpen`; `StateAt(idx)` folds the segment's
watermark with its entries up to `idx`.

`ScanFromEnd` walks the same visible history in descending index order,
including inherited parent prefixes. It holds one log read lock per node
rather than reacquiring it per record and is the primitive used by xwal's
suffix-oriented foreign-key lookup.

`FirstIndex` prefers the inherited parent's first index when non-zero, but
falls back to the child's own first index when the parent is empty. The cached
`log.Log` snapshot follows the same rule.

Inherited `Range` traversal stops at the child's `forkBase` even if a legacy
parent still physically contains later records. The child own range is the
only source for indices at and beyond that boundary.

**`Fork` / `ForkRehome` / `ChildForkBases`** (`fork.go`):

- `Fork(atIdx, childName, oldFutureName?)` splits the log at `atIdx`: the
  log keeps the prefix `[FirstIndex, atIdx-1]` and becomes read-only; the
  suffix moves into an "old-future" subdir; a fresh empty child subdir is
  created, writable from `atIdx`. Branch points are N-ary and
  re-splittable. The *heuristic* re-home applies — on a re-split (own
  segments split) all existing children move into the old-future.
- `ForkRehome(atIdx, child, oldFuture, rehome)` forks with an *explicit*
  re-home list instead of the heuristic. The xwal joint fork uses this so
  every channel re-homes the **same** children (decided once from the
  main channel) and the triune's trees stay in lockstep.
- `ChildForkBases()` returns each child subdir's `.fork` base — the input
  the joint fork uses to decide which children re-home.

**Empty-log handling.** An empty root forks at index 1; an **empty-own**
fork child (`forkBase > 0`, all content inherited) forks at its
`forkBase`. Both materialize empty inheriting children, so every node gets
its own branch in every channel (per-trunk write isolation). Header-mode
children inherit the empty node's watermark. This keeps dynamically added
channels mirrored by later topology operations without adding payload
records.

Crash safety is per-log: a `.fork-pending` sentinel is written before any
destructive change and removed on completion; `Open` refuses a log that
still has the sentinel.

---

## `xwal.XWAL` — the multi-channel "triune" join

**File:** `xwal/xwal.go`, `xwal/fork.go`. **Depends on:** `disk.Log`.

`XWAL` binds several `disk.Log`s into one logical timeline. There is one
**Main** channel (a `ChannelLog`) plus zero or more related channels;
reducible channels (`ChannelReducible`, e.g. a jsonmerge chalkboard) ride
the per-segment watermark headers via `OnSegmentOpen`. Every channel
entry is a JSON envelope tagging its main-timeline LT. Legacy channels use
`{"m":<mainLT>,"p":<payload>,"x":<meta>}`. Channels whose manifest entry has
`opaque: true` use `{"m":<mainLT>,"p64":"<base64>","x":<meta>}`. The base64
field prevents the JSONL codec's recursive canonicalization from changing
provider-owned payload bytes. Decoding accepts both frame shapes.

A `Config` describes the join at creation:

- `Main` — the main channel name.
- `Channels` — the `ChannelSpec` list (name, kind, reducer key, runtime
  `SyncMode`, persisted `Opaque` payload encoding).
- `Registry` — maps reducer names to `Reducer{Reduce, Initial}`;
  resolved on every open (functions are never persisted).
- `Codec` — `"jsonl"` (default) or `"binary"`; persisted in the manifest.
- `Genesis` — custom main-channel genesis payload (creation only).
- `MintTrunkID` — pluggable opaque trunk-id generator (consumed by the
  Trunks layer).

After first open the on-disk `xwal.json` manifest is authoritative for channel
shape and codec. `ChannelSpec.SyncMode` remains runtime-only and is resolved
from `Config.Channels` by channel name on every private or shared open; it is
never written to the manifest. `ChannelSpec.Opaque` is an optional persisted
channel property. It is immutable for an existing channel: callers migrate to
a fresh channel namespace rather than mixing encoding policy implicitly.

Related-channel `Lookup` indexes lazily from the tail. Main LTs are
non-decreasing, so a lookup near the active end reads only the suffix needed
to prove the answer; later lookups extend that same reverse-built index.
Repeated and last-wins lookups remain O(1) once their suffix is indexed.

**Forks all channels as a unit** (`fork.go`). `XWAL.Fork(atMainLT,
child, oldFuture)` builds a `forkPlan`: the main channel forks at
`atMainLT`, each related channel forks at its own boundary
(`boundaryFor` — the first own entry whose main LT ≥ `atMainLT`, or
own-tail+1 if it has not caught up; empty-own channels fork at their own
first index). The re-home set is decided once from the main channel and
applied to every channel by name via `ForkRehome`. A `.xwal-fork-pending`
plan sentinel makes the join crash-atomic across channels: `Open`
roll-forward-completes any interrupted fork before serving a branch, so
the triune is never observed half-diverged.

Before planning a fork, XWAL validates that every manifest channel has the
complete logical branch path and valid fork markers. A missing or invalid path
returns `ErrTopologyIncomplete` before a fork plan or filesystem mutation.
`OpenTrunks` and `EnsureChannel` validate and repair reducible watermarks while
all hot generations are retired. An inherited related-channel tail that
reaches the missing main fork boundary is ambiguous and is rejected rather
than copied or filtered.

`Trunks` caches successful fork preflight validation against its topology
version and validation generation. Normal in-process topology mutations carry
that validation forward because they mirror every channel atomically. Forks
still perform a shallow structural probe for missing paths, markers, and
watermark files; only an unknown or incomplete generation runs the deep
parent-state repair. `Refresh` and explicit external-generation retirement
invalidate the cache.

---

## `xwal.Trunks` — the trunk-addressed view

**File:** `xwal/trunks.go`. **Depends on:** `xwal.XWAL`.

`Trunks` is the surface a consumer addresses. A **trunk** is one
continuation chain across all channels (the triune forked as a unit) —
the stable handle — while the per-fork **node** is plumbing.

**DISK IS THE SOLE SOURCE OF TRUTH.** The node tree *is* the main
channel's directory tree: `nN` subdirs plus `.fork` markers. Everything
about a node is derivable from that tree by walking it — *except* a
node's trunk id. That single non-derivable datum, and only that, is
persisted: a `.trunk` file per node dir (in the main channel), sitting
alongside `.fork`. The in-memory structure (`nodes`, `heads`, sequence
counters) is a derived cache that `rebuild()` reconstructs by one walk of
the tree on open; it cannot diverge from disk because it is read from
disk.

Open XWAL heads, channel state, segment handles, and recovered offset indexes
are retained in a generation-scoped `log.Store`. `Head`, append, state folds,
and full listing therefore borrow shared channel views instead of reopening
the manifest or rescanning segments. Foreign-key suffix indexes live on the
shared channels, so repeated head views continue the same O(delta) lookup
rather than rebuilding it. A topology rebuild or XWAL filesystem mutation
retires the generation; existing borrowers keep it alive until `XWAL.Close`,
while new opens use a fresh store. A shared `XWAL` detaches to private handles
before `Fork`, `Clear`, or `AddChannel`, so filesystem mutation never runs
through a stale shared generation. `Trunks.Close` releases the current hot
generation; callers should use it when a trunk store has a shorter lifetime
than the process.

Ordinary appends take a per-trunk lineage mutex and a shared topology lock.
Appends to unrelated trunks can therefore write concurrently, while appends
within one lineage remain ordered. Forks and other filesystem topology
changes additionally take a root-global mutation lease shared by every
registered `Trunks` for that root. Callers must close borrowed
`Head`/`StumpHead` views on every peer before a topology mutation; the
mutation returns `ErrTopologyBusy` rather than risking a partial rename or
removal while Windows still has segment handles open.

Provides the trunk-level operations: `CreateTrunks` / `OpenTrunks`,
`Head`, `Append`, `ForkAt` / `ForkTail`, `SpawnChild`, `Remove`,
`OwnerTrunk`, `EnsureChannel`, `AppendChannel`, `SyncChannel`,
`LatestChannelRecord`, `List` / `Nodes`. `EnsureChannel` runs under topology
exclusion, backfills the manifest channel across existing nodes, installs its
runtime sync policy, and retires shared hot generations before later opens.
It writes and syncs `.xwal-channel-pending` before changing the channel tree;
`Open` and `OpenTrunks` complete that plan idempotently, publish the final
manifest only after backfill, then remove and sync the sentinel.
For existing reducible nodes it reads the persisted `.fork` base and repairs
only the watermark at that base, deriving its state from the parent. Existing
watermarks are opened and their headers checked rather than trusted by name.
Repairs write and fsync a temporary segment, atomically rename it, and sync the
directory. The Windows two-rename fallback is guarded by a synced
`.replace-pending` sentinel and recoverable `.invalid` backup. Missing or
malformed branch markers are repaired only by `EnsureChannel`; forks fail
closed.
`AppendChannel` and `SyncChannel` use the same per-lineage mutex.
`LatestChannelRecord` scans backward from one immutable hot snapshot and
stops after the newest record, including duplicate-main-LT checkpoints and
inherited fork prefixes. Opaque records retain payload bytes exactly across
append, cached reopen, inherited prefixes, and divergent forks.

This durability surface implements workload
`212c263a-64b4-47af-9570-95702e164055`. See
[`primitives.md`](./primitives.md) for each one's signature and
invariant.

## Recency: LastTS

Every record xwal writes carries a server timestamp (`t`, unix millis) in
its frame, stamped at append time from `Config.Now` (a test seam;
defaults to the real clock). It is mandatory and supplied by xwal itself —
callers cannot set or omit it. Records written before timestamps existed
simply lack the field and decode with `TS == 0`; they are tolerated
forever and never migrated (the JSONL `_hash` covers the payload, so old
bytes stand).

"When was this node last written" is answered by `Trunks.LastTS(trunk)`
(and `StumpLastTS(name)` for legacy stumps), designed for the consumer
that asks it N times per listing:

- **The counter outlives the handle.** `Trunks` owns a registry of
  per-node `atomic.Int64` counters (16 bytes per node ever addressed,
  retained for the life of the `Trunks`). An XWAL opened through Trunks
  wires its internal `lastTS` pointer to the registry's counter, so every
  append through any handle — and every `sharedView` of it — advances the
  one counter a listing reads. Open, close, and hot-store eviction do not
  touch it. This is not a cache of derived data: it is the same primitive
  the appender bumps, merely kept.
- **Warm reads are free.** Registry hit: one map lookup, one atomic load.
  Measured: ~28ns, 0 B, 0 allocs per call.
- **Cold reads are a file read.** First query for a node this process has
  never opened runs a bounded tail probe: the lexically newest segment
  file per channel directory the node owns, scan to the last complete
  frame, decode its `t`, take the max — no full `Open`, no segment
  indexing, no watermark folds. Measured: ~36µs, ~73KB transient, 126
  allocs, paid once per node per process (the probe hydrates the retained
  counter). A full `Open` later merges monotonically (CAS-max) into the
  same counter, so probe and hydration can never regress an append.
- **Never a wake.** LastTS opens (at most) segment files. Whether the
  consumer's node has an agent, a daemon, or anything else attached is
  invisible from here — a listing over dormant nodes stays dormant.

The frame's `t` sits in ALPHABETICAL key position (`m, p/p64, t, x`)
because the JSONL codec canonicalizes key order on disk; encoder output
and disk output are byte-identical, and the fast-path frame decoder
speaks that one grammar. (The first draft ordered the encoder differently
from the disk and every meta-carrying segment read silently fell to the
reflection path — found by benchmark comparison, not by tests, and pinned
by canaries that round-trip the codec since.)
