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
  │   manifest.go)    seeding; manifest; Config                  │
  └───────────────────────────┬─────────────────────────────────┘
                              │ depends on
  ┌───────────────────────────▼─────────────────────────────────┐
  │  disk.Log         segments plus an inherited parent prefix;  │
  │  (log.go,         .fork markers; own-range vs inherited      │
  │   fork.go)        prefix                                     │
  └───────────────────────────┬─────────────────────────────────┘
                              │ depends on
  ┌───────────────────────────▼─────────────────────────────────┐
  │  segment          append-only segment files + ValueHash;     │
  │  (segment.go,     the leaf - no deps on the layers above     │
  │   hash.go)                                                    │
  └─────────────────────────────────────────────────────────────┘
```

A consumer (e.g. the figaro coding agent) addresses `Trunks` and never
touches `XWAL`, `disk.Log`, or `segment` directly. A trunk id is the
stable handle; the per-fork node, the per-channel logs, and the segment
files are all internal mechanism the upper layers manage on its behalf.

---

## `segment`: the leaf

**File:** `segment/segment.go`, `segment/hash.go`

An append-only file framed by a `SegmentCodec`. A `Segment` owns one
file, a `baseIndex`, a count, and an in-memory offset index rebuilt by
`recover()` on open (the standard WAL recovery: scan forward, accept
clean frames, truncate any torn tail). It has no knowledge of forks,
channels, or trunks, it is the bottom of the stack and depends on
nothing above it.

Provides:

- `Create` / `Open` / `OpenReadOnly` (and `*Headered` variants),
  `Append`, `ReadIndex`, `ReadAt`, `Count`, `FirstIndex` / `BaseIndex` /
  `LastIndex`, `Sync`.
- **Block-0 header** (`WriteHeader` / `Header`): an opaque
  first record framed like any other but *not* counted in the entry
  index. This is the watermark slot the reducible model rides on; the
  segment stores the bytes verbatim and never interprets them.
- **`ValueHash`** (`hash.go`): value-stable content hashing. It
  canonicalizes JSON (object keys sorted, whitespace stripped, numbers
  preserved via `json.Number`) and returns the truncated SHA-256
  (16 hex chars / 64 bits) of the canonical form. JSON-equal inputs hash
  identically regardless of key order or formatting, the property the
  upper layers rely on to compare entries by value.

---

## `disk.Log`: segments over an inherited prefix

**File:** `disk/log.go`, `disk/fork.go`. **Depends on:** `segment`.

A `Log` is a directory of segment files that may read a prefix through a
parent log.

**Fork bases & the `.fork` marker.** A child fork's directory carries a
`.fork` file (`ForkBaseOf`) declaring the global
index (`base=<N>`) at which it begins. Its parent is the `Parent` given to
`Open` (xwal's flat forks always give one); without one, a log with
`forkBase > 0` walks `..` to resolve its parent chain.

**Own-range vs inherited prefix (watermarks).** A fork *owns* the index
range `[forkBase, …]`; indices below `forkBase` are inherited and served
by delegating to the parent chain (`Read`, `Range`, `HeaderAt`,
`StateAt` all walk up when `idx < forkBase`). This is the watermark
boundary: own-range entries live in this log's own segments, the
inherited prefix lives in ancestors. `LastIndex` of an empty-own fork
reports `forkBase-1`, it sits immediately after the parent's tail.
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

`Open` refuses a log holding a `.fork-pending` sentinel, which only the
nested fork of older builds wrote.

---

## `xwal.XWAL`: the multi-channel "triune" join

**File:** `xwal/xwal.go`, with `append.go`, `read.go`, `manifest.go`,
`watermark.go` and `frame.go` beside it. **Depends on:** `disk.Log`.

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

- `Main`, the main channel name.
- `Channels`, the `ChannelSpec` list (name, kind, reducer key, persisted
  `Opaque` payload encoding and `Unkeyed` flag).
- `Registry`, maps reducer names to `Reducer{Reduce, Initial}`;
  resolved on every open (functions are never persisted).
- `Codec`, `"jsonl"` (default) or `"binary"`; persisted in the manifest.
- `Genesis`, custom main-channel genesis payload (creation only).
- `MintTrunkID`, pluggable opaque trunk-id generator (consumed by the
  Trunks layer).

After first open the on-disk `xwal.json` manifest is authoritative for channel
shape and codec. `ChannelSpec.Opaque` is an optional persisted
channel property. It is immutable for an existing channel: callers migrate to
a fresh channel namespace rather than mixing encoding policy implicitly.

Related-channel `Lookup` indexes lazily from the tail. Main LTs are
non-decreasing, so a lookup near the active end reads only the suffix needed
to prove the answer; later lookups extend that same reverse-built index.
Repeated and last-wins lookups remain O(1) once their suffix is indexed.

A node has a directory in a related channel only if it wrote that channel,
or it was forked from a parent that had one. A missing directory means the
node inherited nothing there: it reads as an empty log (an Initial state for
a reducible channel), a read creates nothing, and a fork of it gets no
directory either. The first write materializes the node's directory with
`.fork base=1` (and the Initial watermark for a reducible channel) under the
lineage lock, then reopens the log. This is the state the old eager backfill
wrote for every node, so stores backfilled by earlier builds need no
migration.

---

## `xwal.Trunks`: the trunk-addressed view

**File:** `xwal/trunks.go`, with `topology.go`, `hot.go`, `fork.go`,
`stumps.go` and `trunkinfo.go` beside it. **Depends on:** `xwal.XWAL`.

`Trunks` is the surface a consumer addresses. A **trunk** is one
continuation chain across all channels (the triune forked as a unit),
the stable handle, while the per-fork **node** is plumbing.

**DISK IS THE SOLE SOURCE OF TRUTH.** The node tree *is* the main
channel's directory tree: `nN` subdirs plus `.fork` markers. Everything
about a node is derivable from that tree by walking it, *except* a
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
before `Clear`, so filesystem mutation never runs
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
`Head`, `Append`, `ForkAt` / `ForkTail`, `SpawnChildKind`, `Remove`,
`Owner`, `EnsureChannel`, `AppendChannel`, `SyncChannel`,
`LatestChannelRecord`, `List` / `Nodes`.

`EnsureChannel` registers a channel: it creates the channel's root directory
(and root watermark for a reducible channel) and then appends the manifest
entry, under a per-root manifest mutex that `OpenTrunks` also holds while it
settles the manifest. It
writes no node directory, takes no topology mutation, retires no hot
generation and does not wait on borrowed heads. It bumps a per-root
registration counter; an open head that is asked for a channel it does not
know re-reads the manifest only when that counter has moved. A
`.xwal-channel-pending` sentinel left by an older build is recovered on open:
an unpublished channel's half-built tree is removed and the channel is
registered afresh; a published one only loses the sentinel.

The root topology mutation guards what removes or rewrites directories a
live head may read: `OpenTrunks`/`CreateTrunks`, `Refresh`, `Remove`,
`RemoveStump`, `RemoveComponent`, and `Store.Clear`/`Store.TruncateFrom`.
Flat forks, spawns, `Detach` and move imports take only the Trunks mutex
(`beginFlatCreate`). Channel registration and a node's first write to a
channel take neither.

Existing reducible watermarks are opened and their headers checked rather than
trusted by name. Repairs write and fsync a temporary segment, atomically rename
it, and sync the directory. The Windows two-rename fallback is guarded by a
synced `.replace-pending` sentinel and recoverable `.invalid` backup.
`AppendChannel` and `SyncChannel` use the same per-lineage mutex.
`LatestChannelRecord` scans backward from one immutable hot snapshot and
stops after the newest record, including duplicate-main-LT checkpoints and
inherited fork prefixes. Opaque records retain payload bytes exactly across
append, cached reopen, inherited prefixes, and divergent forks.

This durability surface implements workload
`212c263a-64b4-47af-9570-95702e164055`.

## Recency: LastTS

Every record xwal writes carries a server timestamp (`t`, unix millis) in
its frame, stamped at append time from `Config.Now` (a test seam;
defaults to the real clock). It is mandatory and supplied by xwal itself,
callers cannot set or omit it. Records written before timestamps existed
simply lack the field and decode with `TS == 0`; they are tolerated
forever and never migrated (the JSONL `_hash` covers the payload, so old
bytes stand).

"When was this node last written" is answered by `Trunks.LastTS(trunk)`,
designed for the consumer
that asks it N times per listing:

- **The counter outlives the handle.** `Trunks` owns a registry of
  per-node `atomic.Int64` counters (16 bytes per node ever addressed,
  retained for the life of the `Trunks`). An XWAL opened through Trunks
  wires its internal `lastTS` pointer to the registry's counter, so every
  append through any handle, and every `sharedView` of it, advances the
  one counter a listing reads. Open, close, and hot-store eviction do not
  touch it. This is not a cache of derived data: it is the same primitive
  the appender bumps, merely kept.
- **Warm reads are free.** Registry hit: one map lookup, one atomic load.
  Measured: ~28ns, 0 B, 0 allocs per call.
- **Cold reads are a file read.** First query for a node this process has
  never opened runs a bounded tail probe: the lexically newest segment
  file per channel directory the node owns, scan to the last complete
  frame, decode its `t`, take the max, no full `Open`, no segment
  indexing, no watermark folds. Measured: ~36µs, ~73KB transient, 126
  allocs, paid once per node per process (the probe hydrates the retained
  counter). A full `Open` later merges monotonically (CAS-max) into the
  same counter, so probe and hydration can never regress an append.
- **Never a wake.** LastTS opens (at most) segment files. Whether the
  consumer's node has an agent, a daemon, or anything else attached is
  invisible from here, a listing over dormant nodes stays dormant.

The frame's `t` sits in ALPHABETICAL key position (`m, p/p64, t, x`)
because the JSONL codec canonicalizes key order on disk; encoder output
and disk output are byte-identical, and the fast-path frame decoder
speaks that one grammar. (The first draft ordered the encoder differently
from the disk and every meta-carrying segment read silently fell to the
reflection path, found by benchmark comparison, not by tests, and pinned
by canaries that round-trip the codec since.)
