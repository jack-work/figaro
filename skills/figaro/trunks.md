# Trunks & forking

figaro's aria store is backed by **figwal** (a segmented WAL with native
forking). The aria id IS a figwal **trunk id**, and it is **stable across
forks** — the continuation line keeps it. This file is the model: trunks,
cauterization, LT numbering, and the `attend`/`ls`/`fork`/`kill` surface. For
how the bytes sit on disk, see [arias.md](arias.md).

## A trunk is a path, not a node

The store is a fork *forest* of nodes (`n0/n1/…`). A **trunk is a
root-to-leaf path** through it, not a single node. When you fork, the
**continuation line keeps the trunk id** (the canonical trunk); the
**alternative branches off** as a new trunk. So your id never moves out from
under you — forking your own trunk doesn't relocate you.

Node ids are pure plumbing; nothing in the CLI ever addresses them.

## Trunk kinds (the policy layer)

figaro layers three kinds over figwal's generic trunks:

- **null** — the root genesis trunk (one per store). Ceremonial, **closed**.
- **loadout** — `SpawnChild` of null; **one per `name@content-version`**
  (deduped in `policy.json`). Carries the loadout's chalkboard stamp
  (`system.loadout_name/version`, plus the whole loadout chalkboard:
  `skills.*`, `system.credo`, `system.model`, …). **Closed.**
- **conversation** — `SpawnChild` of a loadout; inherits the loadout's
  rendered prefix (cached once, shared by every conversation under it). The
  only **live** kind. A conversation inherits the loadout's full chalkboard.

**Cauterization:** the null and loadout trunks **never append**. Forking or
sending "at" a cauterized trunk does *not* re-split it — it spawns a **new
child conversation** beneath it instead. (This is why "create" and "fork a
loadout" are the same mechanism.)

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
- **`attend <id>` / `<id>:<LT>` / `:<LT>`** (alias **`at`**) — bind this
  shell. CLI-native attendance: the pid↔trunk map is the binding authority.
  An `:<LT>` sets a **one-shot pending fork-point** consumed by the next bare
  prompt (`fig -- …` forks there and moves to the new branch); `:<LT>` alone
  re-pins the already-bound aria. **`detach`** unbinds.
- **`kill <id>`** — remove a trunk **and its whole subtree** (children
  included). Needs `--recursive`/`-r` to remove a trunk that has live
  branches.

## ls / list — attend is `cd`

`attend` is the `cd` of the forest; `ls`/`list` is relative to it:

- **attended** → `figaro ls` roots at the bound trunk's **whole conversation
  tree** (its top-level ancestor) and marks the current trunk with `●`.
- **detached**, or **`ls /`** (or `ls <id>`) → the whole forest (`<id>` roots
  at that subtree). `fig list <id>` likewise scopes to a subtree.

Columns: **ARIA** (mantra, or `aria <id>`, with tree glyphs + a
`●`this-shell / `▸`running / `○`idle marker), **ID** (opaque hex), **LOADOUT**,
**VER** (`live` or a short content-hash), **FORK** (`@N` — the LT a branch was
taken at, blank for roots), AGE, MSGS, CTX, CWD. `-j/--json`, `-a/--all`,
`-n <count>`.

## promote (planned, not built)

re-elect which root-to-leaf path is the *canonical* trunk (swap a branch with
its parent). It is a **view/representation** concern — likely **not**
core-store state (a UI-layer or separately-serialized overlay), with no
figwal/xwal hierarchy mutation. Don't assume it exists yet.
