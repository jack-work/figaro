# Next-pass notes (figaro xwal fork tree)

Captured mid-build while the fork tree is being tested. Ordered roughly
by "design is settled" → "still open." See PHASE_B_PLAN.md for the
original plan and the commit log on `xwal-forking` for what landed.

## 0. USER NOTES (2026-06-27, captured verbatim-ish: highest priority)

### 0a. Aria "trunk": thread identity across forks
The current fork semantics feel odd: forking freezes the parent and mints
two brand-new ids, so nothing visibly "continues the thread." Fix with a
**trunk tag**: a thread/limb identity separate from the per-node aria id.

- On fork the **aria ids still split** (parent freezes, two new children),
  but the **trunk tag is inherited by ONLY the continuation**. The
  alternative founds a **new trunk** (fresh tag + its own mantra +
  friendly name). The caller rescopes (rebinds) to the continuation.
- A trunk = the chain of continuations; its identity/mantra/name flow
  down the main line even though each node's aria id changes per fork.
- The trunk tag can be a **vector** = lineage of child-indices:
  continuation appends `.0`, alternatives `.1`, `.2`, … So depth + branch
  position are encoded (`0` → `0.0` → `0.0.0` down the trunk; `0.1`,
  `0.1.0` for an alternate limb). Usable in the UI to denote nesting.
- **Mantra is per-trunk**: every node on a limb shows its trunk's mantra
  with progressive nesting; a forked alternative shows its own. A trunk
  also has a friendly name. (UI can just show the leading thread's mantra
  for the whole limb.)
- Target rendering (also `--json`-able):

  ```
  vector | mantra   | aria id
  ---------------------------
  0      | mantra 1 | 1234
  0.0    | mantra 1 | 5678
  0.0.0  | mantra 1 | 9012
  0.1    | mantra 2 | 3456
  0.1.0  | mantra 2 | 7890
  ```

Maps onto current code: `Fork` already distinguishes continuation vs
alternative (`runFork` rebinds the shell to the continuation). Add: a
trunk tag on `nodeRec` (continuation inherits parent's, alternative mints
a new one + mantra/name); vector derivable from the tree (path of child
indices) or stored; `list` renders vector|mantra|id with nesting.

### 0b. `fig list` / `fig ls`: `--json` + visual nesting (still unbuilt)
Originally requested, not yet built (was build-order step 6). Default
view: hierarchy with the vector-column nesting above (~10 most-recent +
lineage, depth-truncated). `--json` for the machine view. Fold together
with 0a (the trunk/vector model IS the list model).

### 0c. Data-quality bugs found on disk (diagnosed 2026-06-27)
Inspecting `arias/ir/<outfit>/<conv>/...jsonl`:
- **`logical_time` is always 0 on disk**: never written meaningfully
  (only set on read via `unwrapMessages` from the frame `_idx`). The real
  LT is the frame `_idx`/`m` (coherent + monotonic). Fix: drop it from the
  persisted payload (`omitempty` + never set at write); frame index is the
  single source of truth.
- **`timestamp` non-monotonic**: structural tics (genesis + outfit birth)
  carry `timestamp:0`, AND the **assistant message is never stamped**
  (provider appends the sealed assistant `Message` with no `Timestamp`).
  So you see 0 (genesis) → real (user) → 0 (assistant). Fix: stamp the
  assistant tic at seal; optionally stamp structural tics with created
  time.

## 1. UI projection as a channel (not built)

The current UI IR is turn-shaped. `internal/uiir` projects canonical IR into
`aria.Turn`; `aria.Server` holds the newest mutable suffix in memory, emits
`aria.Page` frames, and hydrates a dormant aria by recomposing canonical IR.
There is no persisted `ui` channel today.

A future cache remains isomorphic to a translation channel, but its immutable
unit is now a **sealed turn**, not separate prompt/assistant messages:

- Channel: `ui`, append-only, one entry per `aria.Turn` with `Sealed:true`,
  keyed to the turn's first main LT.
- Write point: turn seal only. Never write `Turn.Live`, `NodeDelta`, or any
  in-progress suffix to xwal.
- Serialize the neutral UI IR (`Turn` plus `livedoc.Node` snapshots), never
  renderer rows, ANSI, width, theme, transcript state, or spinner frames.
- Fingerprint the projection schema/compose behavior and invalidate exactly as
  translation caches do. Canonical IR remains truth; the UI channel is always
  derivable.
- `figaro.read` and `show --json` may read through the channel and fall back to
  `internal/uiir` on miss/stale data. A live turn still comes only from the
  actor's in-memory `aria.Server`.
- Crash/interrupt before seal writes no UI-cache entry; canonical-IR repair and
  recomposition produce the honest turn on the next open.
- Joint forks inherit the cached sealed prefix and project only divergence.
- Status: cache/performance and prefix byte-stability, not correctness. Lower
  priority than provider translation caches.

Build = register the `ui` channel, define its fingerprinted sealed-turn codec,
append exactly at seal, and add read-through/fallback. Do not revive the retired
`log.snapshot` / `node.open` / `node.patch` / `node.set` protocol.

## 2. Declarative channel set ("track trees via an xwal store")

Today `storeConfig()` hardcodes `ir` + `form`; translations are
added dynamically via `AddChannel`. Generalize: a declarative channel
registry so `ui`, `translations/*`, future projections are uniform -
each a "related tree" in the per-aria xwal store, all joint-forked
together. This is the generalization the user described; the machinery
(N-ary channels, joint fork, AddChannel) already exists.

## 3. Paginated range-query API for any stream

User wants `fig show <stream>` over ANY channel (ir / form /
translations / ui) with range + next-cursor semantics. `aria.read`
already has the shape (`From`/`Limit` → `Total`/`NextFrom`). Generalize
it channel-agnostically on `xwal.ReadAt` + channel bounds so it's a small
reach, not a refactor. (Keep the read path channel-agnostic now so this
stays cheap later.)

## 4. Remaining build-order items (from the cutover)

- **Hierarchy `list`**: `--json`, default ~10 most-recently-interacted +
  full lineage truncated at depth, horizontal ancestor "vector column"
  layout (use `XwalStore.Nodes()` Depth/Parent). Still the flat table.
- **endTurn meta sidecar**: DONE (backend.SetMeta + node Touch).
- **`figaro fork` CLI**: DONE.
- **TTL eviction of idle aria handles**: XwalBackend currently keeps
  handles until Fork/Remove/Close (warm cache, bounded by #arias touched
: fine for a local daemon). Add refcount+TTL if churn shows up.

## 5. Known correctness edge (open)

A form `set` keyed to the next IR LT, with a fork at that exact LT
and NO intervening turn, lands the pending patch on the fork boundary -
rides only the continuation, not the alternative. Realistic flow (set
rides its committed turn) inherits to both. Proper fix: reducible channel
should inherit entries keyed beyond the main tail (pending/future) on
fork, a small xwal refinement.

## 6. Cache-control fork-graph scoring (longer horizon)

Per the cache_control work: retention should become a per-span score
(descendant/child count from the fork graph) rather than one flat
setting, promoting shared prefixes to 1h retention. The fork tree now
exists to carry that graph; the hook is `resolveCacheControl` in
provider/anthropicsdk/assemble.go.

## 7. Versioned frontend protocol and capabilities (open)

Alternate native frontends can already attach to an aria, pull `figaro.read`,
and follow `figaro.aria` / `turn.done` over NDJSON JSON-RPC. That mechanism is
revision-coupled, not yet a public compatibility contract.

- Add an explicit protocol version and capability list to the angelus/aria
  handshake. Distinguish required capabilities from optional additive fields.
- Define version negotiation, compatibility ranges, and the failure returned
  before an incompatible client starts folding frames. Exact build matching is
  only the current CLI guard.
- Move wire structs and the delta-folding client out of `internal/`, and publish
  a language-neutral schema plus reconnect/desync examples.
- Give each subscriber a bounded delivery queue or equivalent isolation. The
  actor must preserve wire order without blocking on a slow external reader;
  disconnect/drop-to-resync is preferable to stalling the turn.
- Keep the current Unix-domain socket as the trusted local native path. A
  browser frontend needs an authenticated loopback WebSocket bridge and origin
  checks rather than exposing the unauthenticated aria RPC surface directly.
- Consider a versioned `figaro events --jsonl` bridge as the smallest portable
  frontend seam; it can hide endpoint discovery and transport differences.
