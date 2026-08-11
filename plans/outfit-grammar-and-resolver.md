# The outfit grammar split, and the resolver subsystem

Gluck's ruling of 2026-08-11 (annoyances 1 and 2 of `/tmp/form-annoyances.md`,
then refined in his second message). This is the brief of record for the
annoyance queue; `plans/forms-and-roles-v2.md` still binds everywhere it is
not touched here.

---

## 0. The defect that started it

`fig form outfit test`, while attending a form, stored

```json
{"layers": ["test"]}
```

verbatim on the board. Nothing was resolved and nothing failed, though no
outfit named `test` exists. Two seams met:

1. `layers` is a DIRECTIVE smuggled inside a patch. `outfit.ParsePatch` turns
   the bare name `test` into `{"layers":["test"]}`, and the server is expected
   to expand it: `ofit.Materialize`: on every path that accepts a patch.
2. The hub write path (`writeForHub`, added in M1 so a dormant figaro and an
   agentless form can take a `set`) applies patches VERBATIM. It is the one
   write path that never materialized, and attending an unbound form routes
   through exactly it. Documented at the time as a known seam; this is the
   bill.

The fix Gluck ruled is not "materialize in the hub path too". It is that a
patch is DATA, all the way down, and outfits are a separate axis.

## 1. The grammar (Gluck, verbatim in substance)

- **`-O/--outfit` is outfit NAMES only.** A comma-separated list. No `k=v`,
  no JSON literal, no `layers`. `-O sonn5,focus`.
- **`-S/--set` is the patch.** The old inline grammar minus names: `k=v`
  pairs and whole JSON-object literals, comma-separated, later terms winning.
  `-S ttl=1h,{"mantra":"cool"}`.
- **`-D/--delete` removes.** A comma-separated list of key paths.
  `-D system.tags,mantra`. Removal has no representation inside `--set`,
  which is why it is its own flag rather than a `--patch` envelope.
- **`-O` and `-S`/`-D` compose, wherever either is accepted.** Precedence is
  fixed and stated: **outfits fold first, then `--set`, then `--delete`.** So
  `-O sonn5 -S system.model=x` means "sonn5, but that model", and never the
  reverse.
- `-P/--patch` is NOT introduced. A whole-patch envelope would be a third
  grammar for what `-S` plus `-D` already say, and each grammar is a test
  matrix. [decision: flag if a machine-readable `{set,remove}` envelope is
  wanted for scripted callers; it is a two-line addition on top of this.]
- Shorts: `-h` is cmdkit's only globally reserved short. `-P`, `-S`, `-D`
  are unused by every command in the table. Available.

### The form family's own verbs

- `fig form set <key> <value>`: the ergonomic single path (revived).
- `fig form set k=v,k2=v2`: one argument in the `--set` grammar, JSON
  literals included. Same code path as `-S`.
- `fig form delete a.b,c`: comma-separated paths. `unset` stays as an alias
  (it is the existing top-level spelling and muscle memory).
- `fig form show [<id>]`: mirrors `figaro show`; the bare `fig form` keeps
  printing the snapshot, and `show` is the explicit spelling that can later
  grow `figaro show`'s limiting mechanics.
- `fig form help <topic>`: third position, not a stateful action.

`state` remains a strict drop-in alias of `form` across all of it (v1
mistake #11's canary covers the new subwords).

## 2. Where `layers` survives

**Exactly one place: the unmarshal that builds a patch from an outfit FILE.**
An outfit file declaring `layers: [a, b]` still pulls its parents in: that is
the closure, and it is what makes outfits composable.

Nowhere else. Not on the wire, not in a patch, not on a board. A `layers` key
typed by a human into `-S` is ordinary data and is stored as such. This is
what lets non-outfit-reading patches cross the form without the closure
machinery ever waking up.

## 3. Materialization moves out of the core

Outfitting is **figaro API**, not part of the single-writer action-reduction
core. The closure is expanded BEFORE the patch enters any queue.

**Wire.** Every request that accepts dressing gains an explicit outfit field
beside its patch:

```go
Outfits []string `json:"outfits,omitempty"`
```

on `CreateRequest`, `ForkRequest`, `FormCreateRequest`, `FormBindRequest`,
`SetRequest`, `CastRequest`, and `FormInput` (the prompt's dressing). The
patch field keeps its meaning and loses its magic.

**Choke point.** One helper at the daemon's API boundary, above the store's
single writer and above the agent's inbox, i.e. in the angelus handlers and
`hub.route` before dispatch:

```go
func (h *handlers) dress(outfits []string, patch *rpc.FormPatch) (form.Patch, error)
```

It resolves the names through the Resolver (§4), folds the result UNDER the
caller's own keys, and returns pure data. Every handler that wants dressing
calls it exactly once; nothing below it reads the filesystem.

**Deletions.**

- `figaro.Agent.Materialize`: gone. The agent's `Set` and the qua path stop
  touching outfits; the actor loop receives data only.
- `ofit.Materialize` calls scattered through `protocol.go` (create, fork,
  formCreate, formBind): replaced by the one `dress` call.
- `outfit.KeyLayers` as a patch directive: `ParsePatch` no longer emits it;
  `WithLayer` (which prepended `default` to a patch's directive) becomes a
  name-list operation.

**What this buys.** `writeForHub`'s verbatim application becomes correct by
construction rather than a documented seam: by the time a patch reaches it,
there is nothing left to expand. `fig form outfit test` fails loudly at the
API boundary with "no outfit named test", because the resolver is strict on
every name but the lenient reserved `default`.

**Compat.** CLI and daemon ship as one binary; a stale daemon reading
`outfits` would ignore it, and a stale CLI sending `layers` would store it
literally. Both are transient local mismatches. The build-identity stamp
already reports the daemon's commit; the mismatch is visible there. Recorded,
not defended.

## 4. The resolver subsystem

Gluck's requirements, plus recommendations he invited.

### Interface

```go
// Resolver turns outfit NAMES into materialized patches. It is the only
// thing in the daemon that reads outfit files.
type Resolver interface {
    // Fold materializes the named outfits in order, later names winning.
    // The reserved name "default" is lenient (absent folds to nothing);
    // every other name is strict.
    Fold(names []string) (form.Patch, error)
    // Closure draws one name's layer graph, for `state outfit --tree`.
    Closure(name string) (*Closure, error)
    // Names lists what is on disk.
    Names() ([]string, error)
    // Reload advances the epoch: snapshots and memos are dropped, taints
    // cleared. `fig outfit reload` calls it; nothing else needs to.
    Reload()
}
```

### Snapshots: consistency against a mid-edit

A resolution must not straddle an edit. On a cache miss the file's bytes are
read once and written to a **content-addressed snapshot store**
(`$STATE_DIR/outfits/snap/<sha256>.json`) with a per-epoch `name → hash` map.
Every read inside a resolution comes from the snapshot, so a closure of nine
files is nine files as they were at the moment the resolution began, even if
the tenth is being saved in an editor.

Content addressing is the recommendation on top of Gluck's ask: it dedups
identical files for free, it makes "which bytes dressed this aria" auditable
after the fact (a receipt: the birth patch is durable, the source no longer
has to be guessed), and GC is a mark-by-epoch sweep.

### Cache: short-lived, evicting, two tiers

1. **Node cache**: one file's parsed patch and declared layers, keyed
   `(name, snapshot-hash)`.
2. **Fold cache**: a materialized fold, keyed by the ordered name list within
   an epoch.

Eviction is idle-TTL (default 5 minutes) plus a byte budget with LRU. Large
outfits are the anticipated case, so bytes are what is budgeted, not entries,
and eviction drops memory only: the snapshot on disk makes re-reading cheap
and, crucially, still consistent.

### Lazy, warm in the background, never blocking startup

Nothing is read at daemon start. A background goroutine warms exactly one
thing: the closure of the configured default outfit, because `fig new` will
want it. Every other name is read on demand. The closure tree is built
iteratively as names are asked for, a node resolved once is a node whose
subtree is already proven.

### Cycles: lazy detection, cached verdicts, tainted nodes

No global topological sort, ever. Resolution is a DFS carrying an on-stack
set; a back edge is a cycle, reported by naming the loop (`a → b → a`). Then:

- every node ON the cycle is marked **tainted** in the cache with that error,
  so any later resolution that touches it fails immediately, without walking;
- every node that resolved cleanly is memoized as **verified acyclic** for
  the epoch, and is never walked again.

That memoized DFS *is* the incremental topological sort: the memo is the
order, built only over the part of the graph anyone asked about. Taints and
verdicts clear on `Reload()` (epoch bump), which is the only invalidation -
plus a `stat` on the root names of a fold, which is cheap and keeps the
common "I edited one outfit" case honest without a watcher.

### Benchmarks (commissioned instruments, `-count=6`)

- `Fold` cold (N-node closure, snapshot write included) vs warm.
- The tainted-cycle path: a second resolution touching a known cycle must be
  O(1).
- Concurrent `Fold` under `-race` (the existing `layers_test.go` concurrency
  test, moved onto the resolver).
- The standing pair `BenchmarkMaterializeWarm` / `NoLayers` retargeted, so
  the before/after slope is readable across the relocation.

## 5. Order of work

1. Grammar + wire + relocation (§1–§3), one commit per surface, real-binary
   verified. The existing `Outfitter` keeps serving `Fold` behind the new
   interface so the relocation lands without waiting on §4.
2. The resolver subsystem (§4) behind that interface, a swap, not a rewrite.
3. The remaining annoyances (`form show`, `form set`, `form delete`,
   `form help`, the unconsumed-`-O` defect) ride with step 1, since they are
   the same dispatcher.

## 6. The unconsumed flag, annoyance 3, explained

`fig form -O name=charles` printed the form and ignored the flag because
`state`'s Run is a hand-written subword dispatcher: `-O` is DECLARED on the
command (it belongs to `new` and `fork`), so cmdkit parsed it happily, and
the bare-form branch simply never read it. cmdkit's unconsumed discipline
covers dash-tokens it cannot PARSE (`unconsumedFlagError`); a flag that parses
and is then dropped on the floor by the Run function is invisible to it.

The fix is declarative rather than another hand-check: a `FlagDef` may name
the subwords it belongs to, and the router refuses the flag before Run when
the first positional is not one of them -

```go
{Long: "outfit", Short: "O", Subwords: []string{"new", "fork", "outfit"}, …}
```

- which turns "silently ignored" into "`-O` belongs to `form new`, `form
fork`, `form outfit`" and retires the hand-written `--list/--tree/--refresh`
guard in the same stroke. Commands without subwords are unaffected.
