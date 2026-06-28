# figaro consumption of xwal.Trunks — execution plan

Worktree `feat/trunks` (off main `62e37b5`). figwal pinned via a **local
`replace` → ~/dev/figwal** (dev only; drop + pin a real tag before merge/nix).
figwal substrate is complete and fuzz-validated:
- `xwal.Trunks` (disk-as-truth trunk forest; `.trunk` per-node markers)
- native nested `map` reducer (built-in)
- always-materialize forks (per-trunk write isolation), fork-redirect
- `Trunks.SpawnChild` (N-ary child of a ceremonial parent, no continuation)
- `Trunks.Append` (tail→append, interior→fork-new-trunk), `ForkTail`, `Head`,
  `AppendChannel`, `List`, `Nodes`, `anchorOf`

## The mapping (figaro policy over xwal.Trunks mechanics)

| figaro concept | xwal.Trunks |
|---|---|
| null root ("arias") | the genesis **root trunk** (`CreateTrunks`) |
| loadout node (per name@version) | `SpawnChild(null)` → trunk; figaro writes loadout IR birth tic + chalkboard stamp; kind=loadout |
| conversation | `SpawnChild(loadout)` → trunk; kind=conversation |
| branch a conversation | `ForkTail(conv)` (tail) / `Append(conv, lt)` (interior) |
| send a turn | `Append(conv, 0, msg)` (tail append to IR) |
| aria id (the stable handle) | **trunk id** |
| node id | per-fork node (plumbing; hidden) |

**The contract flip (this is the rename / bind-to-trunk):** today `Fork(id) →
(cont, alt)` mints TWO new ids and freezes the parent. In the trunk model the
trunk id is **stable** — the continuation keeps the id; `ForkTail(id) → altTrunk`
returns only the new alternative. So `Backend.Fork` becomes `Fork(id) → altID`
(id unchanged). This ripples to the angelus fork handler + `Client.Fork` + the
`fig fork` CLI, and to bindings (pid→trunk, head resolved dynamically). Loadouts/
null are **closed ceremonial trunks** (no live head; `anchorOf` for SpawnChild) —
reject attend/send (the `restoreByID` kind/frozen guard already does this).

**Chalkboard:** keep figaro's reducer (`chalkboardReduce`, registered as
`jsonmerge` in `storeConfig`) — figaro's dotted-key format stays; xwal.Trunks uses
`cfg.Registry`. (Consolidating onto xwal's native `map` reducer is a separable
follow-up — it's a chalkboard-format change touching the provider/encoder.)

**figaro policy persistence:** kinds + loadout dedup (name@version → trunk id) are
figaro-only metadata. Store in a small side-file (e.g. `policy.json`: trunk id →
{kind, loadout, version}) — annotates trunks, does NOT duplicate the tree (that's
figwal's `.trunk`/dirs). Rebuildable from chalkboard `system.loadout_name/version`
if ever lost.

## Build order (each increment building + tested)

1. **`internal/store` swap** — replace `XwalStore` (its own forest) with a thin
   `xwal.Trunks` delegate + the policy side-file. `XwalBackend` keeps its
   memoized per-aria handle cache + `store.Backend` impl, now over Trunks.
   - `Backend.Fork(id) (cont, alt)` → decide: keep 2-return with cont==id, or
     change to `(alt)`. Recommend `Fork(id) (altID, err)` + `ForkAt(id, lt)
     (altID, err)` (cont is always id). Update the interface.
   - Open(id) = store.Log over the trunk head's IR; ChalkboardState/Apply via
     Trunks.AppendChannel + Head StateAt; Node/Nodes/List from Trunks + policy.
2. **agent / figaro** — IR append via Trunks.Append(trunk,0,…); chalkboard via
   the backend; genesis/birth-role unchanged.
3. **angelus** — create = SpawnChild(loadout-from-null); fork handler = stable-id
   ForkTail; bind pid→trunk; attend/resolve trunk→head; list over trunks. Keep
   ceremonial-trunk guards.
4. **rpc + cli** — `ForkResponse` (no parent-freeze semantics; cont==id);
   `fig fork`/`send`/`list` per the trunk model. **Coordinate with the aria-read
   agent**: their work is in `internal/livelog/*` + `cli/livelog_bridge.go`/
   `stream.go` (separate from rpc methods/angelus/store) — avoid those files.
5. **migration** — back-up-and-start-fresh (the index format changed).
6. **finalize figwal pin** — push figwal + tag (v0.6.0), drop the `replace`,
   refresh flake vendorHash, nix build.

## Deferred (unchanged)
re-split-below (interior fork into a frozen ancestor); native-map chalkboard
consolidation; reducible-header cosmetic; pluggable trunk-id minter; truly-empty
channel materialization (figaro should seed translations or handle on first write).
