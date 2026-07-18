# Long-aria performance refactor

## Goal

Make Figaro's own latency effectively independent of total conversation length
except where bytes must actually be read, rendered, or transmitted. Preserve
the actor model and append-only truth while removing compensating caches,
repeated scans, global locks, and dead compatibility layers.

The intended hierarchy is:

1. Figwal/XWAL owns durable append-only data, immutable hot snapshots, and
   physically shared fork prefixes.
2. Figaro owns one actor state machine per aria and incremental derived state.
3. Provider translators cache input-ready bytes and process only the
   untranslated suffix.
4. CLI viewers hold bounded pages and render only the viewport.

## Measured baseline

NixOS WSL, revision `10dd80d`, synthetic histories:

| Path | 1k messages | 10k messages | 50k messages |
|---|---:|---:|---:|
| Cached decoded-log `Read` | 0.14 ms / 172 KB | 1.94 ms / 1.69 MB | 5.48 ms / 8.41 MB |
| Live-tail composition | 0.13 ms / 173 KB | 1.82 ms / 1.69 MB | 3.72 ms / 8.41 MB |
| Session metric refresh | 0.31 ms / 312 KB | 3.98 ms / 3.06 MB | 12.20 ms / 15.21 MB |
| Warm Copilot Responses input | 0.15 ms / 59 KB | 1.75 ms / 977 KB | 13.77 ms / 6.32 MB |
| Warm transcript frame | 3.65 ms / 1.75 MB | 33.31 ms / 19.27 MB | 175.00 ms / 96.39 MB |
| UI history reconstruction | 0.67 ms / 489 KB | 8.92 ms / 6.01 MB | 42.35 ms / 32.16 MB |

Other measured costs:

- Fresh XWAL head open at only 600 messages / 1.2 MB: about 15 ms,
  526 KB, and 1,685 allocations.
- Isolated warm `figaro list`: about 75 ms fixed, 214 ms at 100 dormant
  arias, and 624 ms at 300 dormant arias.
- A 50k-message recent-page read scales from about 5 microseconds at 1k
  messages to 94-122 microseconds because it scans from the beginning.

Profiles show that complete-slice copies, row/separator reconstruction, and
garbage collection dominate these paths. This is Figaro latency, not model
latency.

## Invariants

- Fork prefixes are one physical append-only prefix on disk and one immutable
  snapshot chain in memory. A fork must not copy or re-decode its prefix.
- Translation catch-up is `O(untranslated messages)`, normally one or two.
  Model/fingerprint changes may deliberately rebuild a cache lineage.
- Provider request serialization may remain `O(prompt bytes)` because the
  complete prompt must be transmitted. Translation and cache validation may
  not hide another `O(history)` pass inside it.
- One aria actor owns mutable conversation state. Provider I/O, terminal
  painting, and immutable snapshot reads do not hold the actor.
- A fork of a running aria is serialized by its actor, does not kill it, and
  publishes a continuation that resumes on the same socket and identity.
- Derived caches are disposable. Canonical IR remains authoritative.
- No JSON-RPC, on-disk, OAuth, or cache-prefix change is part of this phase.

## Phase 1: native XWAL hot state

1. Make XWAL channels use Figwal's immutable cached-log snapshots rather than
   opening `disk.Log` directly for each operation.
2. Keep persistent hot heads per trunk/lineage. Append acquires only the
   lineage writer, writes durably, then atomically publishes the new snapshot.
3. Reuse the exact parent snapshot pointer for sibling forks. Keep the parent
   directory as the physical prefix; child directories contain only divergent
   suffixes and fork markers.
4. Replace the global append/fork mutex with topology synchronization plus
   per-lineage serialization. Unrelated arias must progress independently.
5. Cache topology views and fork bases against the existing topology version.
   `ListLight` must not read one marker file per trunk on every call.
6. Expose immutable range/count/tail views so Figaro does not add another
   decoded full-history cache.

No disk layout change is required. Recovery must still derive all hot state
from the canonical tree.

## Phase 2: actor-owned incremental Figaro state

1. Remove `cachedLog` once XWAL supplies immutable decoded/range snapshots.
2. Compose live output directly from the turn watermark, not a copied full
   history.
3. Maintain usage, context, message, turn, tail, and metadata counters as one
   actor-owned reducer updated on append. Persist the existing metadata shape
   from that reducer.
4. Delete inert derivation registries, unused translation metadata sidecars,
   dead list APIs, write-only fields, legacy inbox aliases, and unused log
   methods.
5. Route live fork requests through the actor. The actor asks XWAL to publish
   the fork between durable appends while the provider/tool stream continues.
6. Keep external reads on immutable snapshots so list/status/read RPCs do not
   take the actor or writer lock.

## Phase 3: delta translators

For each `(aria, provider fingerprint)` keep:

```text
translatedThroughIRLT
chalkboardSnapshotAtWatermark
per-message input-ready bytes
flattened provider input view
```

On a turn:

1. Read the current immutable IR snapshot.
2. Validate the cached fingerprint once per lineage, not once per request.
3. Encode only `IR[translatedThrough+1:]`.
4. Append translation entries durably and advance the watermark.
5. Hand request assembly the cached input view plus the new suffix.

The translation cache remains derivable and byte-stable. Request-body encoding
and network upload are measured separately from catch-up.

## Phase 4: bounded transcript

Use current `ReadBefore`/`Read` wire methods:

- decoded page LRU, byte bounded;
- rendered-row LRU keyed by page, width, and render fingerprint;
- compact page descriptors retained after payload eviction;
- viewport plus two screens of overscan;
- separate committed watermark, mutable tail page, and open live record;
- selection anchors stored as `(LT, node ordinal, node hash)`, not loaded-node
  sets or rendered rows.

An evicted descriptor reloads exactly with:

```text
ReadBefore(desc.LastLT + 1, desc.Count)
```

Copy rehydrates selected pages in bounded chunks. Forward navigation normally
uses retained descriptors; an exact multi-probe fallback is acceptable when
both page payload and descriptor have been evicted.

## Phase 5: list and read projections

- Cache the forest snapshot by XWAL topology version.
- Read dormant list/status fields from existing metadata and inherited
  chalkboard snapshots without opening/canonicalizing every aria.
- Invalidate one entry on actor metadata publication and the forest cache on a
  topology mutation.
- Use indexed/binary LT lookup for `Read`, `ReadBefore`, and raw `aria.read`.
- Keep the current non-streaming list wire unless measurements prove the
  bounded cached response still misses the target.

## Acceptance

- 50k-message live composition: below 100 microseconds and 16 KB allocation.
- Warm translation catch-up with two new messages: below 1 ms and independent
  of prefix length, excluding request serialization.
- XWAL append latency is history-independent and bounded primarily by fsync.
- Forking a 100k-message aria performs no prefix copy/re-encode and does not
  interrupt the active actor.
- Warm list at 300 dormant arias: below 75 ms end to end; server handler below
  10 ms.
- Transcript render/navigation p99 below 16 ms after traversing one million
  messages; decoded and rendered caches remain within configured byte caps.
- Recent-page latency at 50k messages is within 20% of latency at 1k.
- Full Go, race, vet, Windows build, Nix build, and crash/fork property tests
  pass.

## Later approval gates

These are not required for the no-format-change phase:

- Persisted `ui` projection channel for instant cold restore.
- Durable translator/chalkboard checkpoint records beyond existing XWAL
  watermarks.
- Snapshot/epoch token on the read wire for server-enforced fork isolation.
- Literal-search index and cursor protocol.

Each changes disk or wire contracts and requires a separately reviewed
migration.
