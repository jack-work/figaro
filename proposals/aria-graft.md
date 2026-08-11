# Grafting an aria between stores, a design, not code

`figaro export` / `figaro import` move an aria by **content**: the destination
mints its own node ids, fork bases, LTs and trunk id, so an import can never
collide and its failure mode is a refusal. That is the right default and it is
what shipped.

It gives up three things:

- **exact identity**: node ids and LTs are the destination's, so
  `<id>.<lt>` coordinates written down elsewhere no longer resolve;
- **branches**, a fork tree is several trunks over a shared prefix, and
  replay reconstructs one line;
- **the provider translation caches**, which is not merely a performance
  matter: they hold Anthropic **thinking signatures**, and the documented
  cache-miss fallback drops thinking blocks rather than emit unsigned ones.
  So the first turn after an import replays a little thinner than the
  original.

A **graft**: copying the bytes and repairing the identities: buys all three
back. This is what it would take.

## What the store actually is

Verified against a live store, not inferred:

- One figwal trunk store per `FIGARO_STATE_DIR/arias`, **not** a directory
  per aria. `xwal.json` declares the channels: `ir` (main), `chalkboard`
  (reducible/jsonmerge), `turn-wal` and `translations-v2/<provider>` (opaque),
  plus the legacy `translations/<provider>`.
- Every channel mirrors the **same node tree**, and that tree is **flat**:
  `ir/n0`, `ir/n1`, … `ir/n542`, plus outfit stumps named
  `<outfit>@<content-hash>`. Nesting is expressed by a marker, not by
  directory depth.
- Each node dir holds `<NNNNN>.jsonl` segments plus two markers:
  `.node`: `from=<parent dir name>`, `kind=null|outfit|conversation`,
  `trunk=<aria id>`, and `.fork`: `base=<index into the parent's log>`.
- `_meta/<aria>.json` is the list sidecar. `_live/` holds transient sidecars.

Four facts make a graft tractable, and all four were checked:

1. **The genesis root is a universal constant.** `96 B`, one line,
   `{"role":"genesis","timestamp":0}`, `md5=e1723643dd6a`: identical in two
   unrelated stores, and it carries no store-specific data, so it is identical
   in every figaro store. A `.fork base=` into the root therefore transfers
   between **arbitrary** stores, not just ones sharing a snapshot. This is the
   fact that makes a general graft possible at all.
2. **Node names are referenced in exactly one place**: `.node from=`. Nothing
   inside a payload names a node. So renaming is a bounded rewrite.
3. **Outfit stumps are content-addressed** (`name@hash`), so they merge by
   *identity*: a stump already present is reused, never copied, never renamed.
   Only conversations need new names.
4. **A trunk id appears inside a payload**, not only in `.node`: the
   chalkboard carries `"aria_id"`. A trunk rename is therefore a marker
   rewrite *and* a chalkboard patch: the same re-stamp `fork` already does.

## The work

**1 · Closure.** From the target node, walk `from=` to the root, collecting
ancestors. Optionally collect descendants (`--with-branches`), which is what
makes a graft worth having over a replay.

**2 · Dedupe.** Any stump whose `name@hash` already exists is reused. Its
subtree is not copied and its name never changes.

**3 · Rename.** Allocate fresh `nN` for each incoming conversation node;
rewrite `from=` in any child whose parent moved. Bounded by fact (2).

**4 · Fork bases stay.** They are indices into a parent's log, and the parent
is either the reused stump (identical by content-addressing) or copied whole.
The root is universal by fact (1). Nothing to recompute: but *verify*: a base
beyond the parent's length is the corruption this whole design exists to avoid,
so assert it rather than assume it.

**5 · Trunk ids.** On collision, mint a new one, rewrite `.node trunk=`, rename
`_meta/<id>.json`, and append a chalkboard patch re-stamping `aria_id`.

**6 · Channels.** Intersect against both `xwal.json` files. A missing
`translations*` channel is droppable: it is a derivable cache. A missing `ir`
or `chalkboard` is a refusal.

**7 · Atomicity.** Stage inside the destination store, fsync, rename into
place. The daemon must be down: it holds the store flock, caches an in-memory
trunk index, and would happily allocate a node name the graft has already
claimed.

**8 · Postconditions**, and this is the part that must not be skipped, because
every failure mode here is *silent*, a store that opens, lists and renders
subtly wrong:

- every `from=` resolves to a node that exists;
- every `.fork base` ≤ its parent's length;
- every trunk id unique;
- every kept aria answers `figaro show` and appears in `figaro ls`;
- the untouched arias' segment bytes are unchanged (checksum before/after).

## Cost, and why it was not built first

Roughly 400–600 lines with tests, most of it in the renaming pass and the
postconditions. The replay path is 150–250 and cannot corrupt anything.

The asymmetry is the argument: an import that goes wrong refuses, and a graft
that goes wrong produces a store that looks fine. Build the loud one first,
use it for everything it covers, and add `figaro import --exact` when someone
actually needs branches or the wire caches preserved.

## Prior art in the tree

`scripts/import-arias.sh` (branch `feat/dev-shell-aria-import`) copies arias
out of the real store into a dev shell's. It is a **copy-then-prune** that
**replaces** its destination, and its own comment explains the refusal: merging
two figwal stores means renumbering node dirs and trunk ids across every
channel. That is precisely the pass described above.

It also has a live bug worth fixing whenever it is touched: it greps for
`.trunk` markers, and the store now writes `.node`, so it reports
`no aria matching '<id>'`: which reads like "no such aria" rather than
"I am looking for the wrong file".
