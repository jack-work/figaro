# Forking & Trunks — design of record

Status: **design settled, implementation pending.** This document is the canonical
reference for figaro's conversation-forking model: the figwal/xwal substrate it sits
on, the terminology, the codepaths as they exist today, and the agreed target design
("trunks"). Read this before touching anything under `internal/store`, the angelus
fork/create/bind handlers, or the `fig send`/`fork`/`new` CLI verbs.

It is written so someone with zero prior context can follow the whole stack from the
physical log up to the CLI, and so the *target* is unambiguous when we go to build it.

---

## 0. One-paragraph orientation

A figaro conversation is an append-only log that can **fork**: at any point, history
can diverge into two branches that share an immutable prefix. The storage substrate is
**figwal** (a segmented write-ahead log with a native fork engine) and its multi-channel
wrapper **xwal** (which forks several parallel logs — the IR, the chalkboard, the
translation caches — together as one unit). figaro stacks a **fork tree** over xwal:
one null root → loadout nodes → conversation nodes, each node a forkable point. The
*target* of this design promotes a higher-level identity — the **trunk** — to be the
thing humans and the API address, demoting the per-fork **node id** to plumbing.

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

### Layer 3 — figaro store (`internal/store/`) — the forest
This is the de-facto **forest manager** bolted on top of xwal. `XwalStore` owns the tree;
`XwalBackend` adds the memoized per-aria handle cache + the `store.Backend` interface.

- **`nodeRec`** (`xwal_store.go:89-111`) — one per fork node: `ID`, `Branch` (fork path),
  `Parent`, `Kind`, `Loadout`/`Version`, `Children`, `Frozen`, `CreatedMS`/`LastMS`, and
  the forest fields **`Trunk`** + **`Vector`**.
- **`nodeKind`** (`:83-87`): `null` | `loadout` | `conversation`.
- **`nodeIndex`** (`:113-116`): `Nodes map[id]*nodeRec` + `Loadouts map["name@version"]id`,
  persisted to `{root}/index.json`.
- **The fork tree:** `null` root (the `arias` dir, `NullAriaID="arias"`, genesis-seeded then
  frozen) → **loadout nodes** (content-versioned via `segment.ValueHash` over the stable
  loadout patch; dedup'd by `name@version`) → **conversation nodes** (forked from a loadout).
- **Trunk + Vector assignment** (the current rule, `forkAtLocked` ~`:284-305`):
  - root conversation (fork of a loadout) → `Trunk = own id`, `Vector = [rootIndex]`.
  - fork **continuation** (the old-future side) → **inherits** `parent.Trunk`, `Vector + [0]`.
  - fork **alternative** (the child side) → **founds** a new trunk (`Trunk = own id`),
    `Vector + [1]`.
  - null/loadout nodes carry empty Trunk/Vector today.
- **Fork ops:** `Fork(id)` (head, `:204`) and `ForkAt(id, atMainLT)` (interior, `:225`) both
  funnel to `forkAtLocked` (`:235`), which freezes the parent and mints two new-id children.
  `forkChildLocked` (`:345`) is the create path (loadout/conversation births).
  `seedChildHandle` (`:321`) gives a fresh fork child its own genesis IR tic on the *live*
  fork handle (a hard-won fix: appending to a re-opened head-fork branch doesn't persist).
- **`Backend` interface** (`store.go:38-102`): `Open`/`OpenTranslation`/`ChalkboardState`/
  `ApplyChalkboard`/`ChalkboardPatches`/`CreateLoadout`/`CreateConversation`/`Fork`/`ForkAt`/
  `Node`/`Nodes`/`Meta`/`List`/`Remove`/`Close`. `XwalBackend` memoizes one shared handle per
  aria and closes it on Fork/Remove/Close (callers never close what `Open` returns).

### The daemon & client (`internal/angelus/`, `internal/cli/`, `internal/rpc/`)
- **Create** (`protocol.go:208-304`): resolve loadout name (or `config.DefaultLoadout`) →
  `outfitter.Load` → stable `loadoutPatch` → `CreateLoadout` (dedup by content version) →
  `CreateConversation` → append a per-conversation boot transition (runtime fill-ins +
  `req.Patch`) to the chalkboard channel.
- **Fork** (`protocol.go:342-375`): rejects non-conversation nodes; kills the live agent;
  `ForkAt`/`Fork`; returns `{Parent, Continuation, Alternative}`.
- **Attend/bind** (`registry.go`, `bindings.go`, `protocol.go`): `pid → figaroID` map,
  persisted to `bindings.json` with PID start-time for reuse detection; `Bind`/`Resolve`/
  `Unbind` RPCs. The client resolves "current" via `os.Getppid()`.
- **Guards** (`restoreByID`, `protocol.go:761-780`): attaching/sending rejects non-conversation
  and **frozen** nodes ("attach a child").
- **CLI verbs** (`cli.go`): `send` (`:136`), `new` (`:181`), `fork` (`:278`), `loadout` (`:395`).
  `runFork` (`manage.go:200`) parses `<id>:<LT>` and `--stay`; `list` (`manage.go:24-94`) renders
  a flat-but-vector-indented table.

---

## 2. Glossary — the rename (figwal vocab vs figaro vocab)

The target promotes the **trunk** to the primary identity and renames accordingly.

| Concept | figwal name | figaro name | What it is |
|---|---|---|---|
| The continuation-chain identity | **trunk id** | **aria id** | Stable thread identity; flows down the continuation side of every fork; never moves, only grows. **The only thing the API/CLI addresses.** |
| One fork node | **node id** | **node id** / aria-node id | A single forkable point in the tree (current `nodeRec.ID`). Plumbing; debug-only in the UI. |
| The whole tree | — | "the arias" | The forest under the null root. |
| Logical time | **LT** (channel/main index) | **LT** | Per-branch, gapless, continuous across a trunk's node chain. |

So: **today's "aria id" becomes "node id"; the trunk id becomes the new "aria id."**
`attend`/`resolve`/`send`/`fork` accept only aria(trunk) ids; node ids are never addressed.

---

## 3. The trunk model (target)

**A trunk is the chain of continuations** — the "keep working" side of every fork. It has a
stable id (the new aria id), a dynamically-resolved **head node** (the live writable leaf),
a **mantra** (essence phrase, from the chalkboard), and a parent trunk + **branched-at LT**.

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

## 4. Settled semantics

### 4.1 Attendance is client-only; the server is stateless about "current"
**Principle:** the figaro server / RPC never knows about "attending." All RPC methods are
**fully resolved to a trunk** by the client *before* the call. The pid↔trunk mapping (today
in the angelus registry) is treated by the client as a **separate system** — a binding
authority it consults to resolve "current," not a thing the conversation API is aware of.
(Eventually the two may merge, but the API boundary stays clean.) The client owns: `pid →
attended trunk`. The RPC owns: tail inference and fork-if-interior. The client never needs to
know a tail.

### 4.2 `send` vs `fork`
- **`send <trunk>[:<LT>]`** — LT omitted or `== tail` → **pure append, no fork**. LT `< tail`
  → **interior fork**: a *new* trunk sprouts at LT, the existing trunk stays live and intact
  (it is *not* bisected). Branch-and-send **attends the new trunk by default**
  (`--attend=false` to stay) — with discretion to *not* auto-attend when the target trunk was
  itself inferred from a compound/remote resolution.
- **`fork [<trunk>]`** — **tail only; no `:LT` accepted** (error if provided). Bisects the
  present: the head freezes, producing a continuation (keeps the trunk) + an alternative (new
  trunk) — two new leaves sharing the full prefix. Seldom-used. **Attends the new alternative
  trunk by default**; `--attend=false` keeps the shell on the existing trunk at the fork
  point. `fork` (no arg) = `fork <current trunk>` via the client's pid resolution.

`fork` survives precisely because it produces what `send` cannot (a true two-leaf bisection
of the present). Everything else is a `send` at an LT.

### 4.3 The resolution table

| You type | Client resolves | RPC receives | RPC does |
|---|---|---|---|
| `send -- msg` | pid → trunk (fail if none, like `status`) | `send <trunk>` | infer tail, append |
| `send <trunk> -- msg` | — | `send <trunk>` | infer tail, append |
| `send :<LT> -- msg` | pid → trunk (fail if none) | `send <trunk>:<LT>` | tail→append; interior→fork new trunk, append |
| `send <trunk>:<LT> -- msg` | — | `send <trunk>:<LT>` | same |
| `fork` | pid → trunk | `fork <trunk>` | tail fork only |
| `fork <trunk>` | — | `fork <trunk>` | tail fork only |
| `new [--loadout L]` / `send --` *unattended* | resolve default/named loadout trunk | `fork <loadout>` → bind → send | mint a conversation trunk |
| `send <loadout-trunk> …` | — | — | **rejected** (loadouts are closed) |

### 4.4 Loadouts are non-attendable trunks; create = fork a loadout
- A loadout **version** is its own ceremonial trunk. **`attend`/`send` to a loadout trunk are
  rejected** — it is permanently closed. The only operation on it is `fork` (a tail fork —
  N-ary, always works on the frozen branch point).
- `fig new`, `fig new --loadout <id>`, and `fig send --` *with nothing attended* all alias to:
  **fork the (default or named) loadout trunk → bind the shell to the new conversation trunk →
  send.** This unifies "create" and "fork" into one mechanism (it already is one under the
  hood — `CreateConversation` makes N-ary siblings under the loadout; `protocol.go:284-289`).

### 4.5 Loadout materialization on bootstrap + reload
- On daemon start, figaro **hashes the default loadout** and materializes it as a trunk. If the
  hash matches an existing loadout-version trunk, no action. If it differs, a **new** default
  loadout-version trunk is created and becomes "the default"; **old versions stick around
  unchanged** and are only reusable if addressed explicitly (discouraged).
- The same action is exposed at runtime as **`figaro loadout reload`**. (Today this materializes
  lazily on first create, `CreateLoadout`; the change is to make it eager + on-demand and to
  treat loadouts as first-class trunks.)

---

## 5. Identity & addressing

- **Aria id = trunk id** is the durable, stable handle. It survives forks and re-homes
  (re-home is a `mv`; ids/LTs unchanged). It is the *only* thing clients address.
- **Node ids** are internal; resolved via `trunk → head node`. Shown in `list` only behind
  `-c`/debug.
- **An LT is a trunk-relative position**, continuous across the trunk's node chain. `send
  <trunk>:<LT>` resolves which (possibly closed) node owns LT; you never name a closed node.
- **`list` is a list of trunks.** Default view: one row per trunk (live head), closed ancestors
  hidden; `-c`/`--show-closed-ancestors` reveals the frozen segment nodes; `--last N`
  (default 10) / `--all`; `--json` (carries `state` + node detail; honors the same flags);
  a `--render` verb reads the JSON schema from stdin so `… --json | jq … | fig list --render`
  works (query in jq, one shared renderer). Pretty view is width-aware (truncate the mantra);
  `state` lives in JSON only.

---

## 6. Current state vs. target (the delta to build)

**Already in place** (data + primitives):
- `nodeRec.{Trunk,Vector,Parent,Frozen,Children,Kind}` and `nodeIndex` (`xwal_store.go`).
- N-ary siblings, re-split-below, freeze-on-fork, joint atomic fork, reducible watermarks
  (figwal v0.5.1).
- Interior fork (`ForkAt`), the pure-ish fork RPC (`ForkRequest.AtMainLT`), non-conversation/
  frozen attend rejection, `pid↔id` bindings.
- Content-versioned loadout nodes; create = fork-loadout under the hood.

**To build** (the trunk pass):
1. **Rename + bind-to-trunk** — aria id = trunk id; `pid → trunk`; node ids hidden; RPC accepts
   only trunk ids; delete the `runFork` rescope conditional (bind-to-trunk makes "fork doesn't
   move you" automatic where it should, and the new attend rules drive the rest).
2. **One `send` path + resolution split** — collapse `Fork`/`ForkAt`/append into a single
   `send(trunk, atLT?)` RPC (omitted/tail → append; interior → fork-new-trunk-and-append,
   `--attend` default true). Client adds the `:<LT>` shorthand + fail-if-unattended.
3. **`fork` = tail-only** — `fork [<trunk>]`, reject `:LT`; attends the new alternative by
   default (`--attend=false` to stay).
4. **Loadouts non-attendable + create-as-fork** — reject attend/send on loadout trunks; route
   `new`/`--loadout`/unattended-`send` through `fork <loadout> → bind → send`; wire `--loadout`
   to the create RPC's existing `Loadout` field.
5. **Loadout materialization action** — bootstrap hook + `figaro loadout reload`: hash loadouts,
   mint a trunk per new version; default = latest hash.
6. **Trunk-leaf `list`** — hide closed ancestors (`-c`), `--last`/`--all`, `--json`, `--render`,
   mantra, width-aware.

**Deferred** (separate, well-tested passes):
7. **Re-split-below through figaro** — arbitrary historical forks at LTs inside *closed*
   ancestors (figwal supports it at the disk layer; figaro currently guards against frozen
   nodes). Until landed, `send <trunk>:<oldLT>` into frozen history must error cleanly;
   interior forks are limited to the live head node's LT range.
8. **Lift the forest into xwal** — relocate the proven mechanism (a generic `Node`/`Trunk`/
   `Forest` + `AppendAt`) down into figwal once the semantics are settled in figaro. This is a
   client-invisible internal move (aria id = trunk id regardless of where the impl lives). The
   motivation is **invariant consolidation**: the fork/head/empty-own-log/re-home seams that
   we've repeatedly gotten wrong from figaro's side belong where the fork engine lives, tested
   once. The split: **xwal owns** trunks/nodes/heads/forks/LTs; **figaro owns** policy — kinds
   (null/loadout/conversation), loadout content-versioning, mantra, the channel set + reducer.

---

## 7. Known edges & assumptions

- **`set`-then-immediate-`fork`** with no committed turn between drops the pending chalkboard
  patch at the boundary (it keys to next-LT, which is the fork point) — commit a turn first.
- A freshly-forked **dormant** child shows `MSGS 0` in `list` until it takes a turn (count comes
  from the per-turn meta sidecar; could be computed from the IR instead later).
- **`--attend` discretion:** branch-and-send attends by default, but when the target trunk was
  inferred via compound/remote resolution, the client may choose not to auto-attend.
- **Default loadout source:** the configured `default_loadout` (`config.go:17-19`), latest hash.

---

## 8. Build sequence (the order of attack)

`1 → 2 → 3 → 4 → 5 → 6`, then deferred `7` and `8`. Steps 1–4 are the spine and are tightly
scoped; each keeps the tree green. Recommended: prove trunk semantics in figaro's `XwalStore`
first (it already carries `Trunk`/`Vector`), then relocate to xwal (step 8) once settled.
