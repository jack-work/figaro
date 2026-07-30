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

figaro derives three node kinds from XWAL topology: the markerless root is
`null`, markerless depth-one stumps are loadouts, and live trunks are
conversations. The full tree, top to bottom:

- **null** — the genesis root, **one per store** (`xwal.CreateTrunks`).
  Ceremonial, **closed**. Pure structure.
- **loadout** (`name@content-hash`) — a named `CreateStump` child of null;
  **one per distinct loadout name + content-version**, deduped by its stump
  name. Each carries that loadout's chalkboard stamp baked once
  into a **shared prefix**: `system.loadout_name`/`system.loadout_version`,
  plus the whole loadout chalkboard — `skills.*`, `system.credo`,
  `system.model`, …. **Closed.**
- **conversation** — `SpawnUnderStump` from a *loadout*; inherits the loadout's
  rendered prefix via the fork watermark (cached once, shared by every
  conversation under it). The only **live** kind.
- **branch** — a fork of a conversation. Also a conversation, just one whose
  parent is another conversation rather than a loadout.

**Top-level aria vs branch.** A **top-level aria** is a conversation whose
parent is a loadout — a root of the conversation forest. A **branch** is a
conversation whose parent is another conversation. (Both are `kindConversation`
on disk; the distinction is lineage.)

**Cauterization:** the null root and loadout stumps are **closed** — you can't
append to or continue them; they're structure, not conversation. Forking or
sending "at" a cauterized trunk does *not* re-split it — it spawns a **fresh
child conversation** beneath it instead (`ForkAt` redirects through
`SpawnUnderRoot`/`SpawnUnderStump`). This is why "create" and "fork a
loadout" are the same mechanism.

## LT numbering

Every turn has a figwal **main-LT**, continuous along the trunk's node chain:

- `1` = genesis (the root tic; filtered from rendering/context)
- `2` = loadout birth (the chalkboard stamp message)
- `3+` = conversation turns

`figaro show` labels each **turn** by its **turn id**, and `send`/`fork`/
`attend`'s `:<turn>` address that — the shown number **is** the fork
coordinate. LT is the *model's* coordinate (it counts the steps the model
experienced, and most LTs sit mid-tool); it stays visible under `show -v/-l`
for debugging the fig IR, but it is not an address.

## Commands

- **`send <id>:<turn> -- …`** — fork the trunk so `<turn>` is **replaced**,
  then send to the new branch (and **rebind** this shell there;
  `--stay`/`--attend=false` to send but not move). Without `:<turn>`, plain
  append to the tail.
- **`fork [<id>[:<turn>]] [--stay] [-- <prompt>]`** — imperative branch. A
  `:<turn>` is an interior fork: everything through the end of turn
  `<turn>-1` is shared, the original suffix becomes the continuation, a fresh
  empty alternative diverges. No `:<turn>` = tail fork. Forking your **own** bound aria rebinds you to the
  continuation (same trunk/mantra, the alternative is the new branch);
  forking any other aria, or `--stay`, leaves your session untouched.

  **With a prompt** it forks *and sends*, the way `new -- <prompt>` does. The
  prompt always lands on the **alternative** — the fresh empty branch — and
  the continuation is never written to. `--stay` then governs only the
  **shell**: without it, forking your own aria moves you to the alternative
  (that is where the prompt went); forking anyone else's aria never moves
  you, so `fork <other>:12 --stay -- "try it this way"` is a clean fan-out.
  Flags are `send`'s (same parser, same dispatch): `-r` raw, `-v` verbatim,
  `-o` verbose, `-l` listen, `-x`(+`-n`/`-y`) exec, `-f` forget. `-e` is
  **rejected** (a fork mints a persistent branch), and a send flag without a
  prompt is an error rather than a no-op. `-j` prints one line —
  `mode:"fork-send"`, `aria_id` = the branch. `fork --` with an empty body
  opens the composer.

  > Deliberate asymmetry: `send <id>:<turn> --stay` parks the branch and
  > sends to the *original* trunk. `send`'s subject is the message (*where
  > does this land?*); `fork`'s is the branch (*what did I just make?*).
- **`attend <id>` / `<id>:<turn>` / `:<turn>`** (alias **`at`**) — bind this shell,
  like `cd`. CLI-native attendance: the pid↔trunk map (the angelus binding
  registry) is the binding authority; the figwal layer knows nothing of it. An
  `:<turn>` sets a **one-shot pending fork-point** consumed by the next bare
  prompt (`fig -- …` forks there and moves to the new branch); `:<turn>` alone
  re-pins the already-bound aria. **Terminal-only**: inside an aria's own bash
  tool `attend` refuses, because `FIGARO_ARIA` pins that shell to the aria
  that spawned it, permanently (see the figaro SKILL). An aria reaches another
  aria with an explicit `--id`, never by attending it.
- **`attend null`** (the literal `null`) — **go home**: unbind the shell. New
  conversations then default to the live loadout. The word echoes the
  **kindNull** genesis root that sits above every loadout. There is **no
  `detach`** (removed) — `attend null` is the unbind. `attend ~` is kept as
  a legacy alias (the tilde must be quoted in the shell). Attending a
  cauterized (null/loadout) aria is rejected with a nudge toward
  `attend null` / `ls -h` / `ls -g`.
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
