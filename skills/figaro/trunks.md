# Trunks & forking

figaro's aria store is backed by **figwal** (a segmented WAL with native
forking). The aria id IS a figwal **trunk id**, and it is **stable across
forks** — the continuation line keeps it. This file is the model: trunks,
cauterization, LT numbering, and the `attend`/`ls`/`fork`/`kill` surface. For
how the bytes sit on disk, see [arias.md](arias.md).

> The word **trunk** echoes opera's *aria di baule* — the "trunk aria" (or
> "suitcase aria") a singer carried from production to production, packed in
> their travel trunk and slotted in wherever it fit. A figaro **trunk** is
> likewise the portable canonical line a conversation carries through its forks.

## A trunk is a path, not a node

The store is a fork *forest* of nodes (`n0/n1/…`). A **trunk is a
root-to-leaf path** through it, not a single node. When you fork, the
**continuation line keeps the trunk id** (the canonical trunk); the
**alternative branches off** as a new trunk. So your id never moves out from
under you — forking your own trunk doesn't relocate you.

Node ids are pure plumbing; nothing in the CLI ever addresses them.

## The loadout tree (the policy layer)

figaro layers **four kinds** over figwal's generic trunks
(`kindNull`/`kindLoadout`/`kindConversation`; `kindOf` derives the kind from
lineage, nothing is stored). The full tree, top to bottom:

- **null** — the genesis root, **one per store** (`xwal.CreateTrunks`).
  Ceremonial, **closed**. Pure structure.
- **loadout** (`name@content-hash`) — `SpawnChild(null)`; **one per distinct
  loadout name + content-version** (deduped in `policy.Loadouts`, keyed
  `"name@version"`). Each carries that loadout's chalkboard stamp baked once
  into a **shared prefix**: `system.loadout_name`/`system.loadout_version`,
  plus the whole loadout chalkboard — `skills.*`, `system.credo`,
  `system.model`, …. **Closed.**
- **conversation** — `SpawnChild` of a *loadout*; inherits the loadout's
  rendered prefix via the fork watermark (cached once, shared by every
  conversation under it). The only **live** kind.
- **branch** — a fork of a conversation. Also a conversation, just one whose
  parent is another conversation rather than a loadout.

**Top-level aria vs branch.** A **top-level aria** is a conversation whose
parent is a loadout — a root of the conversation forest. A **branch** is a
conversation whose parent is another conversation. (Both are `kindConversation`
on disk; the distinction is lineage.)

**Cauterization:** the null and loadout trunks are **closed** — you can't
append to or continue them; they're structure, not conversation. Forking or
sending "at" a cauterized trunk does *not* re-split it — it spawns a **fresh
child conversation** beneath it instead (`Fork`/`ForkAt` redirect to
`SpawnChild(owner)`). This is why "create" and "fork a loadout" are the same
mechanism.

## LT numbering

Every turn has a figwal **main-LT**, continuous along the trunk's node chain:

- `1` = genesis (the root tic; filtered from rendering/context)
- `2` = loadout birth (the chalkboard stamp message)
- `3+` = conversation turns

`figaro show` labels each unit by this LT, and `send`/`fork`/`attend`'s
`:<LT>` address it — the shown number **is** the fork coordinate (they were
realigned so `show`'s N == the `:N` you pass).

## Commands

- **`send <id>:<LT> -- …`** — fork the trunk at `<LT>`, then send to the new
  branch (and **rebind** this shell there; `--stay`/`--attend=false` to send
  but not move). Without `:<LT>`, plain append to the tail.
- **`fork [<id>[:<LT>]] [--stay]`** — imperative branch, **no prompt**. Bare
  `:<LT>` is an interior fork (history below `<LT>` is shared; the original
  suffix becomes the continuation, a fresh empty alternative diverges). No
  `:<LT>` = tail fork. Forking your **own** bound aria rebinds you to the
  continuation (same trunk/mantra, the alternative is the new branch);
  forking any other aria, or `--stay`, leaves your session untouched.
- **`attend <id>` / `<id>:<LT>` / `:<LT>`** (alias **`at`**) — bind this shell,
  like `cd`. CLI-native attendance: the pid↔trunk map (the angelus binding
  registry) is the binding authority; the figwal layer knows nothing of it. An
  `:<LT>` sets a **one-shot pending fork-point** consumed by the next bare
  prompt (`fig -- …` forks there and moves to the new branch); `:<LT>` alone
  re-pins the already-bound aria.
- **`attend ~`** (the literal `~`) — **go home**: unbind the shell. New
  conversations then default to the live loadout. There is **no `detach`**
  (removed) — `attend ~` is the unbind. Attending a cauterized (null/loadout)
  aria is rejected with a nudge toward `attend ~` / `ls -h` / `ls -g`.
- **`kill <id>`** — remove a trunk **and its whole subtree** (children
  included). Needs `--recursive`/`-r` to remove a trunk that has live
  branches.

## ls / list — attend is `cd`

`attend` is the `cd` of the forest; `ls`/`list` navigate relative to it.

**Scope:**

- **`figaro ls`** — current scope. **Attended** → your aria's fork tree
  (with `●` marking you); **detached** → home (all top-level arias).
- **`figaro ls <id>`** — scope to that aria's subtree.

**Views (don't unbind you):**

- **`-h`/`--home`** — the home view (all top-level arias + their branches)
  *without* unbinding; `●` stays on your real aria.
- **`-g`/`--global`** — home **plus** the null + versioned-loadout anchors,
  drawn above the conversations (the infrastructure trunks).

**Cap:**

- default = the **10 most-recently-used**; **`-a`/`--all`** removes the cap;
  **`-n N`** sets it. `-a` and `-n` are mutually exclusive.

**JSON:**

- **`--json`** — a pro/dev escape hatch: the global state of **all** arias
  incl. null + loadouts, **always**. Rejects every other flag.

Columns: **ARIA** (mantra, or `aria <id>`, with tree glyphs + a
`●`this-shell / `▸`running / `○`idle marker), **ID** (opaque hex), **LOADOUT**,
**VER** (`live` or a short content-hash), **FORK** (`@N` — the LT a branch was
taken at, blank for top-level arias), AGE, MSGS, CTX, CWD.

## promote (planned, not built)

re-elect which root-to-leaf path is the *canonical* trunk (swap a branch with
its parent). It is a **view/representation** concern — likely **not**
core-store state (a UI-layer or separately-serialized overlay), with no
figwal/xwal hierarchy mutation. Don't assume it exists yet.
