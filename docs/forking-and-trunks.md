# Forking & Trunks — design of record

Status: **shipped** (the trunk pass is built and on `main`). This document is the
canonical reference for figaro's conversation-forking model: the figwal/xwal substrate
it sits on, the terminology, and the codepaths as they exist today. Read it before
touching anything under `internal/store`, the angelus fork/create/bind handlers, or the
`fig send`/`fork`/`attend`/`kill`/`ls` CLI verbs.

> The word **trunk** echoes opera's *aria di baule* — the "trunk aria" (or "suitcase
> aria") a singer carried from production to production, packed in their travel trunk and
> inserted wherever it fit. A figaro **trunk** is likewise the portable canonical line a
> conversation carries through its forks.

It is written so someone with zero prior context can follow the whole stack from the
physical log up to the CLI. (A user-facing condensed version lives in the first-party
skill at `skills/figaro/trunks.md`; this doc is the deep substrate reference.)

---

## 0. One-paragraph orientation

A figaro conversation is an append-only log that can **fork**: at any point, history
can diverge into two branches that share an immutable prefix. The storage substrate is
**figwal** (a segmented write-ahead log with a native fork engine), its multi-channel
wrapper **xwal** (which forks several parallel logs — the IR, the chalkboard, the
translation caches — together as one unit), and figwal's **`xwal.Trunks`** forest layer
(nodes + trunks + heads on disk). figaro stacks only *policy* on top: a null root →
loadout stumps → conversation trunks. The **trunk** is the thing humans and the API
address — its id is the aria id, **stable across forks** (the continuation keeps it) —
while the per-fork **node id** (`n0/n1/…`) is pure plumbing, never addressed.

---

## 1. The stack, bottom-up (with terminology)

Four layers. Each owns a strict slice of the problem; the dividing lines matter.

### Layer 0 — `segment` (figwal: `segment/`)
The physical atom: an append-only file of length-framed records, addressed by a global
index. Two codecs: `BinaryCodec` (`.seg`) and `JSONLCodec` (`.jsonl`, the default —
human-readable NDJSON, which is *why* the on-disk tree is greppable). A segment may
carry an opaque **block-0 header** (the "watermark"), uncounted by the index — this is
how reducible state rides along (see Layer 2).

### Layer 1 — `disk.Log` (figwal: `disk/`) — the fork engine
A directory of segments plus the fork structure. **This is where forking physically
lives.** Key facts:

- **Append-only, index-addressed.** `Write(idx, payload)` only accepts `idx == LastIndex+1`
  (or `forkBase` for a fresh fork). No overwrite, no interior insert — interior placement
  has exactly one coherent meaning, which is *fork*. `disk/log.go:175` gates writes on
  `readOnly`.
- **A fork = a subdirectory.** Forking splits the log at `atIdx`; the prefix `[first,atIdx-1]`
  becomes **read-only** (a "branch point"), the original continuation moves to an
  "old-future" child subdir, and a fresh child subdir is created. `.fork` markers carry
  `base=N`; the parent resolves by walking `..`.
- **Freeze-on-fork is an invariant.** Any node with child subdirs is read-only; there is
  **no fork-in-place**. `disk/fork.go:487` sets `readOnly`; only leaves are writable.
- **Copy-on-write reads.** A fork's `Read`/`Range` delegate to the parent chain for
  `idx < forkBase`; the shared prefix is never duplicated. The global index is continuous
  across the parent→child seam — so within any one branch, indices are unique and gapless.
- **N-ary branch points.** Forking again at the tail (`atIdx == LastIndex+1`) just adds
  *another sibling* child — no data moves (`disk/fork.go:254-270`, `oldFutureExists==false`).
- **Re-split-below.** Forking *below* an index where children already exist inserts an
  intermediate branch point and **re-homes the existing child subdirs** into the old-future
  via directory moves; `..`-walk parent resolution adapts (`disk/fork.go:222-227` captures
  children, `:438-446` re-homes them). This is the mechanism for forking deep history.
- **Crash safety** via a `.fork-pending` sentinel.

### Layer 2 — `xwal.XWAL` (figwal: `xwal/`) — the triune
A **multi-channel** wrapper: one **main** channel plus N **related** channels, forked as a
unit. figaro's three channels (the "triune"):

| channel | kind | role |
|---|---|---|
| `ir` | `ChannelLog` (main) | the canonical message timeline; LTs come from here |
| `translations/<provider>` | `ChannelLog` | cached wire-bytes per IR LT (preserves thinking signatures) |
| `chalkboard` | `ChannelReducible` | structured state as patches on a watermark base |

Terminology & mechanics (`xwal/xwal.go`, `xwal/fork.go`):
- **`XWAL` = one *opened branch*** of the multi-channel log. `branch []string` is the chain
  of fork names from the root (empty = the trunk-of-the-xwal, distinct from figaro "trunk").
- **Every channel entry is `(channelLT, mainLT, payload, meta)`.** `mainLT` is the foreign
  key to the IR timeline; it must be non-decreasing per channel and may reference *future*
  IR LTs (for catch-up). `AppendMain(payload,meta)` (`xwal.go:397`) writes the IR and returns
  its LT; `Append(channel, mainLT, payload, meta)` (`xwal.go:411`) writes a related channel.
- **Reducible channels** ride a per-segment **watermark** + patches; `StateAt(channel, lt)`
  (`xwal.go:474`) folds the nearest watermark with the patches after it. The chalkboard is
  this — there is **no `chalkboard.json`**; the channel is the durable truth.
- **`meta`** is an opaque per-entry side-channel (`xwal.go:546-557`) — figaro stores the
  translation fingerprint here.
- **Joint fork** (`xwal/fork.go:51`): `Fork(atMainLT, childName, oldFutureName) → *XWAL`.
  The **main channel forks at `atMainLT`**; each related channel forks at its own boundary —
  the first channel LT whose `mainLT >= atMainLT` (`boundaryFor`, `fork.go:228`). The
  **old-future is the original continuation; the child is the new alternative** — both names
  are used identically across every channel, so a branch is addressable as a unit. The fork
  is **crash-atomic** across channels (a `.xwal-fork-pending` plan sentinel; `Open` rolls a
  partial fork forward). Empty / empty-own-log channels are skipped (`fork.go:85-97`).
- `AddChannel` (for a newly-seen provider), `Clear` (cache invalidation).

> **What xwal does NOT have today:** any notion of a *node*, a *tree of branches*, a *trunk*,
> or a *head pointer*. It models exactly one opened branch + a joint fork. The whole
> forest/tree layer currently lives one level up, in figaro.

### Layer 2.5 — `xwal.Trunks` (figwal: `xwal/trunks.go`) — the forest
The forest manager now lives **in figwal**, not figaro (the deferred "lift into xwal" of
the old plan landed). `xwal.Trunks` owns the node tree, trunk identity, and heads on disk;
**disk is the sole source of truth.** The node tree is the **main channel's directory tree**
(`ir/`, with `n0/n1/…` child dirs + `.fork` markers); the only datum not derivable from it
is the trunk id per node, kept in a **`.trunk` marker** in each node's `ir/` dir. Key API:

- `CreateTrunks(dir, cfg) → (*Trunks, rootTrunkID)` seeds the genesis root trunk;
  `OpenTrunks(dir, cfg)` reopens.
- `SpawnChild(trunk)` mints an N-ary child trunk under a (typically cauterized) trunk —
  the create path for both loadouts and conversations.
- `ForkTail(trunk)` / `ForkAt(trunk, atMainLT)` branch; the **continuation keeps the
  trunk id**, the alternative is the returned new id. `Owner(id, atMainLT)` resolves
  which root, stump, or trunk owns an interior LT.
- `Head(trunk) → *XWAL` opens the trunk's live writable leaf. `Remove(trunk, recursive)`
  deletes a trunk and (with `recursive`) its subtree. `List()` returns live trunks;
  closed ones aren't listed.

### Layer 3 — figaro store (`internal/store/`) — policy only
With the forest in figwal, figaro keeps **only policy**. `XwalStore`
(`xwal_store.go`) is a thin layer over `xwal.Trunks`; `XwalBackend`
(`xwal_backend.go`) adds the memoized per-aria handle cache + the `store.Backend`
interface.

- **`nodeKind`**: `null` | `loadout` | `conversation`. It is derived from XWAL
  topology: the markerless root is null, markerless depth-one stumps are
  loadouts, and live trunks are conversations.
- There is no policy side-file. Loadout stump names (`name@version`) provide
  durable identity and deduplication.
- **The full tree (four layers):**
  - **`null`** — the genesis root, **one per store** (`xwal.CreateTrunks`). Ceremonial,
    **closed**. Pure structure.
  - **`loadout(name@content-hash)` stumps** — `CreateStump`, **one per distinct
    loadout name + content-version** (content-versioned via `segment.ValueHash` over the
    stable loadout patch, dedup'd by its `name@version` stump name). Each carries a
    renderable `RoleUser` birth message stamping that loadout's chalkboard — `skills.*`,
    `system.credo`, `system.model`, the `keyLoadoutName`/`keyLoadoutVer` stamp — baked
    **once** into a shared prefix. **Closed.**
  - **`conversation` trunks** — `CreateConversation` = `SpawnUnderStump(loadout)`; inherit the
    loadout's rendered prefix via the fork watermark. **Live.** A conversation whose parent
    is a loadout is a **top-level aria** (a root of the conversation forest).
  - **branches** — forks of conversations; a conversation whose parent is *another
    conversation*. (Still `kindConversation`; the distinction is lineage.)
- **Cauterization** (`cauterized` = kind is null or loadout): the root and loadout stumps are
  **closed** — you can't append to or continue them; they are structure, not conversation.
  A fork/send "at" a cauterized trunk does **not** re-split it: `Fork`/`ForkAt` redirect to
  `SpawnChild(owner)` — a fresh child conversation — instead of `ForkTail`/`ForkAt`. This
  is why "create" and "fork a loadout" are one mechanism.
- **The aria id is the trunk id**, returned stable from `Fork`/`ForkAt` as `cont == id`
  (bind-to-trunk: forking your own trunk doesn't move you).
- **Forest vectors** (`vectorsLocked`): each conversation trunk gets a child-index path
  among conversation trunks (roots `[0],[1],…`; a branch is `parentVec+[k]`). Used by
  `list` for tree indentation and `topLevelAncestor`. `NodeView.BranchedLT` is the trunk's
  first own LT — the displayed fork point is `BranchedLT-1`.
- **`Backend` interface** (`store.go`): `Open`/`OpenTranslation`/`ChalkboardState`/
  `ApplyChalkboard`/`ChalkboardPatches`/`CreateLoadout`/`CreateConversation`/`Fork`/`ForkAt`/
  `Node`/`Nodes`/`Conversations`/`ConversationIDs`/`Meta`/`SetMeta`/`Remove`/`Close`.
  `XwalBackend` memoizes one shared row cache per aria; callers never close what `Open`
  returns.

### The daemon & client (`internal/angelus/`, `internal/cli/`, `internal/rpc/`)
- **Create**: resolve loadout name (or `config.DefaultLoadout`) → `outfitter.Load` → stable
  `loadoutPatch` → `CreateLoadout` (dedup by content version) → `CreateConversation` → append
  a per-conversation boot transition (runtime fill-ins + `req.Patch`) to the chalkboard
  channel. The conversation inherits the loadout's full chalkboard (`skills.*`,
  `system.credo`, `system.model`, …).
- **Fork**: kills the live agent; `ForkAt`/`Fork`; returns `{Parent, Continuation,
  Alternative}` (Continuation == the stable aria id).
- **Attend/bind**: `pid → trunkID` map (the angelus binding registry), persisted with PID
  start-time for reuse detection; `Bind`/`Resolve`/`Unbind` RPCs. Bind carries an optional
  `atMainLT` — a **one-shot pending fork-point** consumed by the next bare prompt. The
  client resolves "current" via `os.Getppid()`. Attendance is **entirely CLI-side state**:
  the figwal layer knows nothing of it, the binding authority is consulted by the client,
  and the conversation RPCs are fully resolved to a trunk before the call. `attend null`
  (the required literal; `attend ~` is a legacy alias that needs quoting in the shell) is
  "go home" — `Unbind`; new conversations then default to the live loadout. Attending a
  **cauterized** (null/loadout) aria is rejected with a nudge toward
  `attend null` / `ls -h` / `ls -g`.
- **The store flock**: the angelus is a strict singleton via an exclusive flock on
  `<store>/arias/.daemon.lock`, acquired **before** the backend opens and before the socket
  binds (`cli/angelus.go:lockStore`). Fixed a TOCTOU where two daemons could race-spawn and
  both open the store, corrupting it.
- **CLI verbs** (`cli.go`): `send`/`fork`/`attend`(`at`)/`kill`/`list`(`ls`)/
  `show`/`status`/`state`. (`detach` was **removed** — `attend null` is the unbind; `~` is
  kept as a legacy alias.)
  `send <id>:<LT> -- …` forks at LT then sends to the new branch
  (rebinds; `--stay` to park). `fork [<id>[:<LT>]] [--stay]` is the imperative no-prompt
  branch (`runFork`, `manage.go`). `kill <id>` removes a trunk + subtree (`--recursive` for
  live branches). `show [<id>]` takes the aria id **positionally** (bare-N replaced by
  `-n/--last`); units are labeled by figaro LT, realigned so the shown N **is** the `:N`
  fork coordinate. `status -m/--more` surfaces derived detail (mantra, cwd, loadout version,
  fork origin, created); `-j/--json` (`-mj` clusters). `list`/`status`/`state` all take
  `-j/--json`. The old `derive` verb was **removed** — its values surface in `status --more`
  (the derivation *workers* still run, feeding `list`/`status`).

---

## 2. Glossary (figwal vocab vs figaro vocab)

The **trunk** is the primary identity; the rename below shipped.

| Concept | figwal name | figaro name | What it is |
|---|---|---|---|
| The continuation-chain identity | **trunk id** | **aria id** | Stable thread identity; flows down the continuation side of every fork; never moves, only grows. **The only thing the API/CLI addresses.** |
| One fork node | **node id** (`n0/n1/…`) | node id | A single forkable point in the main-channel dir tree. Plumbing; never addressed. |
| The whole tree | — | "the arias" | The forest under the null root. |
| Logical time | **LT** (channel/main index) | **LT** | Per-branch, gapless, continuous across a trunk's node chain; `show` labels by it, `:<LT>` addresses it. |

`attend`/`send`/`fork`/`kill` accept only aria(trunk) ids; node ids are never addressed.

---

## 3. The trunk model

**A trunk is the chain of continuations** — the "keep working" side of every fork; a
root-to-leaf path through the fork forest. It has a stable id (the aria id), a
dynamically-resolved **head node** (the live writable leaf), a **mantra** (essence phrase,
from the chalkboard, auto-seeded from the first user message), and a parent trunk +
**branched-at LT**.

```
T0 "fork tree"  A[1–31 frozen] ─┬─ B[31–52 frozen] ─┬─ C[52–98 live]   ← T0 head
                                 │                    └─ a1b2[52–]        ← T3 "rewrite cli"
                                 └─ 3456[31–39] ─┬─ 7890[39–61]          ← T1 head
                                                 └─ 4d0c[39–]            ← T2 "repro wal"
```
T0 = `A→B→C`; the closed nodes (A, B) are T0's frozen segments, C is its live head. The node
ids are plumbing; you address `T0`.

**Invariants:**
- **Trunks are append-only and immutable.** A trunk only ever grows at its tail or spawns a
  *new* trunk at an interior point. Its identity never moves and its content is never
  rewritten. (Internally an interior fork still freezes a node and re-homes a suffix — but
  from the trunk's view nothing it owns changed.)
- **Continuation inherits the trunk; alternative founds a new one.** (Maps directly onto
  xwal's old-future-vs-child distinction.)
- **Only leaves are writable; the head is the writable leaf.** Resolving an aria id =
  resolving its trunk → its head node → endpoint.
- **`attend` is pure client/session state.** See §4.1.

---

## 4. Semantics (as shipped)

### 4.1 Attendance is client-only; the server is stateless about "current"
**Principle:** the figaro server / RPC never knows about "attending." All RPC methods are
**fully resolved to a trunk** by the client *before* the call. The pid↔trunk mapping (the
angelus binding registry) is treated by the client as a **separate system** — a binding
authority it consults to resolve "current," not a thing the conversation API is aware of.
The client owns: `pid → attended trunk` (plus an optional one-shot pending fork-point LT).
`attend <id>`/`<id>:<LT>`/`:<LT>` set it; **`attend null`** (the required literal; `~` is a
legacy alias that needs quoting in the shell) clears it —
"go home," after which new conversations default to the live loadout. There is **no
`detach`** (removed). Attending a cauterized (null/loadout) aria is rejected with a nudge
toward `attend null` / `ls -h` / `ls -g`.

### 4.2 `send` vs `fork`
- **`send <trunk>:<LT> -- …`** — fork the trunk at `<LT>`, then send to the new branch and
  **rebind** there (`--stay`/`--attend=false` to send without moving). Without `:<LT>` it is
  a plain **append** to the tail. The interior-fork case is cauterization-aware: if `<LT>` is
  owned by the root or a loadout stump, a fresh child conversation is spawned instead of a
  re-split (`store.ForkAt` via `Owner`).
- **`fork [<trunk>[:<LT>]] [--stay]`** — the **imperative, no-prompt** branch. No `:<LT>` =
  tail fork (freeze the head; continuation keeps the trunk, a fresh empty alternative is the
  new branch). `:<LT>` = interior fork (shares `[1..LT]`). Forking your **own** bound aria
  rebinds you to the continuation (same trunk/mantra); forking any other aria, or `--stay`,
  leaves your session untouched. `fork` (no arg) = `fork <current trunk>`.

### 4.3 The resolution table

| You type | Client resolves | RPC does |
|---|---|---|
| `send -- msg` | pid → trunk (fail if none) | infer tail, append |
| `send <trunk> -- msg` | — | infer tail, append |
| `send :<LT> -- msg` | pid → trunk (fail if none) | fork at LT, send to new branch, rebind |
| `send <trunk>:<LT> -- msg` | — | same |
| `fork [<trunk>[:<LT>]]` | pid → trunk if bare | imperative tail/interior fork, no message |
| `attend <id>` / `:<LT>` | pid → trunk | bind shell (+ one-shot pending fork-point) |
| `attend null` | — | unbind (go home); next conversation defaults to the live loadout |
| `kill <trunk>` | — | remove trunk + subtree (`-r` for live branches) |
| `send` *unattended* / `new` | resolve default/named loadout stump | spawn a conversation under it, send |

### 4.4 Loadouts are cauterized stumps; create = spawn under a loadout
- A loadout **version** is its own ceremonial stump (one per `name@content-version`), and is
  **closed**: forking/sending "at" it never re-splits it — it **spawns a new child
  conversation** (cauterization). A conversation inherits the loadout's full chalkboard
  (`skills.*`, `system.credo`, `system.model`, the loadout name/version stamp).
- `fig new`, `fig new --loadout <id>`, and `fig send --` *with nothing attended* all resolve a
  loadout stump → spawn a conversation under it → bind → send (`CreateLoadout` dedups by
  content version; `CreateConversation` = `SpawnUnderStump(loadout)`).
- Chalkboard-key completion falls back to the **default loadout** when no aria is bound.

### 4.5 Loadout materialization
- Loadouts materialize **lazily** on first create (`CreateLoadout`): the stable loadout patch
  is content-hashed (`segment.ValueHash`); a matching `name@version` stump is reused, a new
  hash mints a new loadout stump. Old versions stick around unchanged. (An eager
  bootstrap/`loadout reload` action remains a possible future refinement, not a current
  command.)

---

## 5. Identity & addressing

- **Aria id = trunk id** is the durable, stable handle (opaque 4-byte hex). It survives forks
  and re-homes (re-home is a `mv`; ids/LTs unchanged). It is the *only* thing clients address.
- **Node ids** (`n0/n1/…`) are internal plumbing; resolved via `trunk → head node`. Never
  shown, never addressed.
- **An LT is a trunk-relative position** (figwal main-LT), continuous across the trunk's node
  chain: `1`=genesis, `2`=loadout birth, `3+`=conversation turns. `send`/`fork`/`attend`
  `:<LT>` resolves which root, stump, or trunk owns LT (`Owner`); `show` labels each
  unit by the same LT, **realigned** so the shown number is the `:N` fork coordinate.
- **`list`/`ls` is the conversation forest, with `attend` as `cd`.** The shipped navigation
  surface:
  - **`figaro ls`** — *current scope*: **attended** → your aria's fork tree (top-level
    ancestor's whole tree, `●` marking you); **detached** → home (all top-level arias).
  - **`figaro ls <id>`** — scope to that aria's subtree.
  - **`-h`/`--home`** — the home view (all top-level arias + branches) **without unbinding**;
    `●` stays on your real aria.
  - **`-g`/`--global`** — home **plus** the null + versioned-loadout anchors drawn *above*
    the conversations (the infrastructure trunks).
  - **cap:** default = the **10 most-recently-used**; **`-a`/`--all`** removes the cap;
    **`-n N`** sets it (`-a`/`-n` mutually exclusive).
  - **`--json`** — a pro/dev escape hatch: the global state of **all** arias incl. null +
    loadouts, **always**; rejects every other flag.
  - Columns: **ARIA** (mantra or `aria <id>`, tree glyphs + `●`this/`▸`running/`○`idle),
    **ID**, **LOADOUT**, **VER** (`live` or short content-hash), **FORK** (`@N` = the LT a
    branch was taken at, blank for top-level arias), **AGE**, **MSGS**, **CTX**, **CWD**.

---

## 6. What shipped (and what's left)

**Shipped** (the whole trunk pass):
- **The forest lives in figwal** (`xwal.Trunks`): nodes/trunks/heads/forks/LTs on disk, disk
  as the sole source of truth. The markerless root and named loadout stumps make separate
  policy state unnecessary. The old per-aria-dir / `nodeRec` / `index.json` model is gone.
- **Aria id = trunk id, stable across forks** (continuation keeps it; `cont == id`).
  Bind-to-trunk: forking your own trunk doesn't move you.
- **One `send` path**: `send <id>:<LT>` forks-then-sends (rebinds; `--stay`); bare `send`
  appends. `fork [<id>[:<LT>]] [--stay]` is the imperative no-prompt branch. `attend`/`at`
  (with `attend null` to go home — `detach` **removed**, `~` kept as a legacy alias),
  `kill <id>` (+ subtree, `-r`).
- **Cauterization**: the null root and loadout stumps are closed — forking/sending "at"
  them spawns a child conversation (`Owner` + `SpawnUnderRoot`/`SpawnUnderStump`).
- **The four-layer loadout tree**: `null` → content-versioned **loadout** stumps (dedup'd by
  `name@version`) → **top-level arias** (conversations under a loadout) → **branches** (forks
  of conversations); conversations inherit the loadout chalkboard.
- **Trunk forest `list`/`ls`** (attend = `cd`): current-scope `ls`, `ls <id>` subtree,
  `-h/--home` (view without unbinding), `-g/--global` (+ null/loadout anchors), cap
  `-a/--all` | `-n N` (default 10), `--json` (all arias incl. null + loadouts, rejects other
  flags); `status -m/-j`, `state -j`, positional `show <id>` with `-n/--last`; LT realigned
  so shown N == `:N`.
- **Single-daemon flock** on `<store>/arias/.daemon.lock` (`cli/angelus.go`).
- **`derive` verb removed** (its values surface in `status --more`).

**Left / future:**
- **Re-split-below into closed history through figaro**: figwal supports interior forks below
  indices owned by closed anchors at the disk layer (and cauterization routes loadout/null LTs
  through `SpawnUnderStump`/`SpawnUnderRoot`); arbitrary deep historical re-splits inside
  *conversation* ancestors are exercised via `Owner` + `ForkAt`.

---

## 7. Known edges & assumptions

- **`set`-then-immediate-`fork`** with no committed turn between drops the pending chalkboard
  patch at the boundary (it keys to next-LT, which is the fork point) — commit a turn first.
- A freshly-spawned **dormant** child shows `MSGS 0` in `list` until it takes a turn (count
  comes from the per-aria `_meta` sidecar).
- **Default loadout source:** the configured `default_loadout` (`config.go`), latest hash;
  chalkboard-key completion falls back to it when no aria is bound.
