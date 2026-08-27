# Trunks & forking

figaro's aria store is backed by **figwal** (a segmented WAL with native
forking). The aria id IS a figwal **trunk id**, and it is **stable across
forks**: the continuation line keeps it. This file is the model: trunks,
cauterization, LT numbering, and the `attend`/`ls`/`fork`/`kill` surface. For
how the bytes sit on disk, see [arias.md](arias.md).

> The word **trunk** echoes opera's *aria di baule*: the "trunk aria" (or
> "suitcase aria") a singer carried from production to production, packed in
> their travel trunk and slotted in wherever it fit. A figaro **trunk** is
> likewise the portable canonical line a conversation carries through its forks.

## A trunk is a path, not a node

The store is a fork *forest* of nodes (`n0/n1/…`). A **trunk is a
root-to-leaf path** through it, not a single node. When you fork, the
**continuation line keeps the trunk id** (the canonical trunk); the
**alternative branches off** as a new trunk. So your id never moves out from
under you: forking your own trunk doesn't relocate you.

Node ids are pure plumbing; nothing in the CLI ever addresses them.

## The outfit tree (the policy layer)

figaro derives three node kinds from XWAL topology: the markerless root is
`null`, markerless depth-one stumps are outfits, and live trunks are
conversations. The full tree, top to bottom:

- **null**: the genesis root, **one per store** (`xwal.CreateTrunks`).
  Ceremonial, **closed**. Pure structure.
- **outfit** (`name@content-hash`), a named `CreateStump` child of null;
  **one per distinct outfit name + content-version**, deduped by its stump
  name. Each carries that outfit's form stamp baked once
  into a **shared prefix**: `system.outfit_name`/`system.outfit_version`,
  plus the whole outfit form: `skills.*`, `system.credo`,
  `system.model`, …. **Closed.**
- **conversation**: `SpawnUnderStump` from a *outfit*; inherits the outfit's
  rendered prefix via the fork watermark (cached once, shared by every
  conversation under it). The only **live** kind.
- **branch**, a fork of a conversation. Also a conversation, just one whose
  parent is another conversation rather than an outfit.

**Top-level aria vs branch.** A **top-level aria** is a conversation whose
parent is an outfit, a root of the conversation forest. A **branch** is a
conversation whose parent is another conversation. (Both are `kindConversation`
on disk; the distinction is lineage.)

**Cauterization:** the null root and outfit stumps are **closed**: you can't
append to or continue them; they're structure, not conversation. Forking or
sending "at" a cauterized trunk does *not* re-split it: it spawns a **fresh
child conversation** beneath it instead (`ForkAt` redirects through
`SpawnUnderRoot`/`SpawnUnderStump`). This is why "create" and "fork a
outfit" are the same mechanism.

## LT numbering

Every turn has a figwal **main-LT**, continuous along the trunk's node chain:

- `1` = genesis (the root tic; filtered from rendering/context)
- `2` = outfit birth (the form stamp message)
- `3+` = conversation turns

`figaro show` labels each **turn** by its **turn id**, and `send`/`fork`/
`attend`'s `:<turn>` address that: the shown number **is** the fork
coordinate.

### Three coordinates: `:turn`, `:turn.node` and `.LT`

| form | coordinate | what it means |
|---|---|---|
| `<id>:<n>` | **turn** | The exchange: your prompt and everything the agent did about it. What `show` prints, and what you normally want. |
| `<id>:<n>.<k>` | **node** | One thing inside that turn: a paragraph, a thought, a tool call. The address the pager draws under `Ctrl-O` (`12.3`), so **what you can point at you can branch at**. `-1` is the turn's opening question, which is the turn coordinate itself. |
| `<id>.<n>` | **LT** | The model's logical time: one step of its experience. What `show -v/-l` prints. |

The colon is the human coordinate; the dot after it is the reader's; the bare
dot is the model's. **Prefer the colon.** Most LTs sit mid-tool, and forking
there strands a `tool_invoke` without its result, so `.LT` is the *precise*
form rather than the safe one. Reach for it when you already hold an LT (from
`show -v`, a log line, a tool), or to branch somewhere no turn boundary exists.

All three work everywhere a coordinate does: `send`, `fork`, `attend`: because
one parser reads them. The daemon accepts the turn and the LT on the wire
(`at_turn` and `at_lt`) and does that translation itself; the NODE is resolved
by the client, for the reason below. Naming more than one at once is an error
rather than a precedence rule.

#### A node is finer than a fork point

A fork cuts the message log **between whole messages** — that is what the
branch and its parent share. A node is a content *block*: one assistant message
that says a paragraph and then calls a tool is **one message and two nodes**.

So a node coordinate resolves to the message boundary at or before it, and when
that is earlier than the node you named, the command **says so** and forks
there anyway:

```
$ figaro fork abc12345:19.7
node 7 cannot be cut at: forking before node 6 instead (a fork cuts whole
messages, and node 7 is not where one begins)
forked abc12345 at turn 19 node 7 (now a frozen fork point)
```

A node that *begins* its own message is exact and says nothing extra. Two more
rules fall out of the same fact:

- **`:19.0` keeps the turn's question and drops every answer to it** — a
  different place from `:19`, which drops the question too.
- **A fork never strands a tool call.** A tool node carries both its
  coordinates (the invoke and its result), so a cut that would land between
  them retreats past the whole call. Anthropic rejects a conversation whose
  `tool_use` has no `tool_result`, so this is the difference between a branch
  you can prompt and one you cannot.

The resolution reads the daemon's **own composed nodes**, over the same read
wire the pager and `show` use — not a second composer in the client. Two
composers that disagreed by one node would fork one node away from where you
pointed. `scripts/forknode-e2e.sh` proves the whole path against a real daemon
with no credentials and no tokens.

## Commands

- **`send <id>:<turn> -- …`** (or `<id>.<lt>`): fork the trunk so `<turn>` is
  **replaced**, then send to the new branch (and **rebind** this shell there;
  `--stay`/`--attend=false` to send but not move). Without a coordinate, plain
  append to the tail.
- **`fork [<id>[:<turn>[.<node>]|.<lt>]] [--stay] [-- <prompt>]`**: imperative
  branch. A `:<turn>` is an interior fork: everything through the end of turn
  `<turn>-1` is shared, the original suffix becomes the continuation, a fresh
  empty alternative diverges. `:<turn>.<node>` cuts INSIDE that turn, at the
  message the node begins (see above). `.<lt>` forks at that logical time
  exactly. No
  coordinate = tail fork. Forking your **own** bound aria rebinds you to the
  continuation (same trunk/mantra, the alternative is the new branch);
  forking any other aria, or `--stay`, leaves your session untouched.

  **With a prompt** it forks *and sends*, the way `new -- <prompt>` does. The
  prompt always lands on the **alternative**: the fresh empty branch, and
  the continuation is never written to. `--stay` then governs only the
  **shell**: without it, forking your own aria moves you to the alternative
  (that is where the prompt went); forking anyone else's aria never moves
  you, so `fork <other>:12 --stay -- "try it this way"` is a clean fan-out.
  Flags are `send`'s (same parser, same dispatch): `-r` raw, `-v` verbatim,
  `-o` verbose, `-l` listen, `-x`(+`-n`/`-y`) exec, `-f` forget. `-e` is
  **rejected** (a fork mints a persistent branch), and a send flag without a
  prompt is an error rather than a no-op. `-j` prints one line -
  `mode:"fork-send"`, `aria_id` = the branch. `fork --` with an empty body
  opens the composer.

  > Deliberate asymmetry: `send <id>:<turn> --stay` parks the branch and
  > sends to the *original* trunk. `send`'s subject is the message (*where
  > does this land?*); `fork`'s is the branch (*what did I just make?*).

  **An aria cannot fork itself from inside its own running turn.** Fork
  coordination rides the agent's single-threaded inbox, so the fork queues
  behind the very turn whose tool call is waiting on it: neither side can
  move. With `[authz] policy = "default"` this is refused up front with an
  error carrying the workaround; with the policy off it simply hangs.

  The workaround is to **detach** the fork so it lands after the turn closes:

  ```sh
  mkdir -p /var/tmp/$FIGARO_ARIA
  cat > /var/tmp/$FIGARO_ARIA/fork.sh <<'SH'
  #!/usr/bin/env bash
  set -u
  figaro fork --id "$ARIA" --stay -j > "/var/tmp/$ARIA/fork.json"
  SH
  chmod +x /var/tmp/$FIGARO_ARIA/fork.sh
  ARIA=$FIGARO_ARIA env -u FIGARO_ARIA -u FIGARO_NO_BIND \
      setsid nohup /var/tmp/$FIGARO_ARIA/fork.sh >/dev/null 2>&1 &
  ```

  Read `fork.json` on the **next** turn to learn the ids. Unsetting
  `FIGARO_ARIA`/`FIGARO_NO_BIND` matters: otherwise the detached child is
  attributed back to the calling aria and lands in the same trap. Forking a
  **different** aria mid-turn is fine: that aria's drain loop is free, and
  so is forking yourself while **idle**, which is exactly what the detached
  script does.

  > This restriction is a **guardrail, not a cure**. The real defect is that
  > trunk coordination blocks the actor loop at all; the fix is to store trunk
  > state in its own reducible xwal channel the way the form is stored.
  > See the note at `angelus.handlers.fork`.
- **`attend <id>` / `<id>:<turn>` / `:<turn>`** (alias **`at`**): bind this shell,
  like `cd`. CLI-native attendance: the pid↔trunk map (the angelus binding
  registry) is the binding authority; the figwal layer knows nothing of it. An
  `:<turn>` sets a **one-shot pending fork-point** consumed by the next bare
  prompt (`fig -- …` forks there and moves to the new branch); `:<turn>` alone
  re-pins the already-bound aria. **Terminal-only**: inside an aria's own bash
  tool `attend` refuses, because `FIGARO_ARIA` pins that shell to the aria
  that spawned it, permanently (see the figaro SKILL). An aria reaches another
  aria with an explicit `--id`, never by attending it.
- **`attend null`** (the literal `null`): **go home**: unbind the shell. New
  conversations then default to the live outfit. The word echoes the
  **kindNull** genesis root that sits above every outfit. There is **no
  `detach`** (removed): `attend null` is the unbind. `attend ~` is kept as
  a legacy alias (the tilde must be quoted in the shell). Attending a
  cauterized (null/outfit) aria is rejected with a nudge toward
  `attend null` / `ls -H` / `ls -g`.
- **`kill <id>`**: remove a trunk **and its whole subtree** (children
  included). Needs `--recursive`/`-r` when anything is drawn under it; a
  refusal writes nothing. See "Delete, and the two hierarchies" below.

## ls / list, attend is `cd`

`attend` is the `cd` of the forest; `ls`/`list` navigate relative to it.

**Scope:**

- **`figaro ls`**: current scope. **Attended** → your aria's fork tree
  (with `●` marking you); **detached** → home (all top-level arias).
- **`figaro ls <id>`**: scope to that aria's subtree.

**Views (don't unbind you):**

- **`-H`/`--home`**: the home view (all top-level arias + their branches)
  *without* unbinding; `●` stays on your real aria.
- **`-g`/`--global`**: home **plus** the null + versioned-outfit anchors,
  drawn above the conversations (the infrastructure trunks).

**Cap:**

- default = the first **10 ROWS** of the drawn tree, whole trees ordered by
  their most recent member. It is a row budget, not a "ten newest arias"
  filter, so a deep tree can spend the whole budget and the footer says how
  many are left. **`-a`/`--all`** removes the cap; **`-n N`** sets it. `-a`
  and `-n` are mutually exclusive.

**JSON:**

- **`--json`**, a pro/dev escape hatch: the global state of **all** arias
  incl. null + outfits, **always**. Rejects every other flag.

Columns: **ARIA** (mantra, or `aria <id>`, with tree glyphs + a
`●`this-shell / `▸`running / `○`idle marker), **ID** (opaque hex), **OUTFIT**,
**VER** (`live` or a short content-hash), **FORK** (`yes` for a branch, `-`
for a top-level aria: the fork POINT is an LT and the coordinate you would
type is a turn, so `figaro status <id>` prints the `parent:turn` you can
fork at rather than a number here that reads like one), AGE, MSGS, CTX, CWD.

The tree a listing draws follows the PRESENTATION edge (`present` in
`--json`), which is the topology edge until a promote moves it.

## promote

**`promote [<id>] [levels]`** raises an aria in the tree `figaro ls` draws:
it takes its parent's place, and the parent it displaced comes to sit under
it. Presentation only. No history moves, no id changes, your binding is
untouched, and the aria still reads exactly the turns it read before, so a
promote is O(1) in conversation length and cannot fail halfway.

```sh
figaro promote              the bound aria, one level
figaro promote <id>         another aria, one level
figaro promote <id> 10      up to 10 levels, stopping at the outfit
```

**Two hierarchies, and which one answers what.** The TOPOLOGY (`.from` on
disk) says where an aria's history comes from: forking, reading and
context always follow it, and nothing in this section can move it. The
PRESENTATION says where a row is drawn and what a delete warns about. They
start identical; a promote is the only thing that parts them.

Promotion stops at the outfit boundary. Only conversations nest, so an
outfit stump and the genesis root are never reparented under an aria: a
top-level conversation has nothing above it to promote into and is refused
("cannot promote into an outfit"), with a nudge toward making or editing an
outfit instead. `promote <id> 10` on an aria three levels deep climbs three
and reports three; it is not an error to ask for more than the tree has.

**`figaro normalize`** makes every aria own its history outright, so no
delete can owe anything at its boundary. It is the one operation here that
is not instant (it copies the prefix each promoted aria borrows).

Implementation: `runPromote` (`internal/cli/manage.go`), the `figaro.promote`
RPC, `PromoteResponse{FigaroID, Climbed, AtStump}`, the boundary check in
`XwalStore.Promote`, and the override state in the topology form. The listing
carries both edges: `parent` (history, what `status` prints as forked-from)
and `present` (where the row is drawn, what the vector follows).

## Delete, and the two hierarchies

`kill <id>` counts on one tree and cuts on the other, deliberately:

- **What it refuses** is counted on the DRAWN tree, so the warning matches
  what you see: `kill <id>` on an aria with rows under it refuses and names
  how many; `-r` takes them.
- **What it removes** is the HISTORY subtree, because that is what owns
  bytes on disk.
- An aria merely promoted under the target therefore survives. Its
  presentation edge is forgotten and it returns to where its history puts
  it.
- A survivor that read its history THROUGH the removed set absorbs that
  prefix first, and is then pinned where it was drawn, so a delete never
  teleports an untouched aria to the genesis root.

A refused delete writes nothing at all: the count is taken before any
repair, because a repair cannot be taken back.

## The trunk state IS a form

The presentation lives in the **topology form**: a form on the reserved
stump `@topology`, one per store and so 1:1 with the angelus that owns it.
It holds OVERRIDES, never a full tree, so a lost topology form degrades to
"draw every aria where its history puts it" rather than to a wrong tree.

- **A promote is ONE patch naming two edges**, so the pair lands together or
  not at all. The `trunks.json` it replaces rewrote the whole document per
  edit and could half-land.
- **Durability, versioning and the single writer are the form's**, not
  bespoke: reduce purely, append, fsync, publish. `Rev()` is the form
  version, which is what a listing's snapshot check reads.
- **It is never listed, never forked, never bound.** `listStumps` filters it
  out of the forest.
- **A legacy `trunks.json` is folded in on first open** and renamed
  `trunks.json.migrated`.
- **Retention is not built yet**: it keeps every record until figwal grows a
  compacting channel.

Design and the deviations from it:
[contributing/trunk-singleton-form.md](../contributing/trunk-singleton-form.md).
