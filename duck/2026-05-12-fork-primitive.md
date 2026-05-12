# 2026-05-12 — Fork primitive for figaro

**Status:** brainstorm (substrate shipped, fork-proper not started). Folded into
work on the `worktree-fork-sentinel` branch through commit `4c09402`. Pending
phases noted inline.

## Goal

Add `figaro fork` (RPC + CLI). Forking an aria at IR index N produces two
children on disk (per figwal's fork model): an **old-future** child carrying
entries `[N, last]` and a **fresh** child appendable from N. The parent dir
becomes a read-only branch point. `figaro attend` on a branch point presents
an interactive menu of children, recursing until a leaf is selected.

## User's framing — preserved

- *"WAL stays append-only."* No `TruncateBack`. Dangling tool_use at the tail is
  repaired by appending an `system.interrupt` sentinel IR entry; the translator
  emits a synthetic provider-acceptable surrogate on the wire. The IR is the
  source of truth — mutation by deletion erodes replay/audit/fork semantics.
- *"Translation should also be a figwal log, though not one that can branch."*
  Translator caches use the same `Log[T]` interface as the IR. Forks ride along
  with the IR — translator logs aren't independently fork-able from the user's
  POV.
- *"Arias nested by id on disk."* Each fork is a subdirectory of its parent;
  the disk tree mirrors the conceptual fork tree.
- *"Selecting a read-only aria via attend should yield a recursive selection of
  its children."* The user navigates the tree to the desired leaf.
- *"One underlying WAL implementation, codec-pluggable."* No deep binary/jsonl
  branching. One `Log[T]` interface; FileLog (legacy) and FigwalLog (current).

## Resolved questions

1. **Old-future name.** Default to `path.Base(parentDir)` per figwal. Add an
   optional second name to `figwal.Log.Fork(atIdx, freshName, oldFutureName)`.
2. **Live turn.** Fork interrupts: cancels `turnCtx`, drains, then forks.
3. **Aria selection.** Default operates on attended aria; `--aria <id>`
   overrides.
4. **Post-fork binding.** Shell follows the fresh child (`abc/v2` after
   `figaro fork abc 6 v2`).
5. **No depth cap.**

## Pre-reqs in figwal — SHIPPED upstream

- ✅ `Log.Fork` accepts optional second name `oldFutureName` (default = parent
  basename). Commit: `c32c9b1` on `figwal`.
- 🟡 `Store.Rename(oldPath, newPath)` — deferred. Not on the fork critical path.
  Without a Store, document that Rename only updates the calling `*Log` and
  other holders go stale.

**Removed from scope:** `Log.TruncateBack`. The WAL stays append-only — see
sentinel section below.

## Interrupt sentinel — SHIPPED in `worktree-fork-sentinel`

The IR is append-only. When a tool_use lands without a matching tool_result
(agent interrupt, fault, exit), the dangling state stays on disk. Repair is
lazy.

- **New IR entry kind:** `system.interrupt` carrying ContentInterrupt blocks
  (`{tool_call_id, tool_name, reason, text}` per dangling tool_use_id).
  Append-only, durable, visible in `figaro aria` / `jq`.
- **Tail check at write time:** before appending a new user-role tic, the
  agent inspects the tail. If it is an unmatched assistant tool_use turn,
  append a sentinel first, then the new user entry.
- **Boot-time check:** same logic runs once when the agent opens an aria, as
  belt-and-suspenders.
- **Translator handling:** per-provider translators map the sentinel to
  whatever the provider requires to keep the conversation valid. For Anthropic
  this is a synthetic `tool_result` with `is_error: true` per dangling
  tool_use_id, emitted only into the wire bytes — never back into the IR.
- **Replaces** the old `repair.go` suffix-truncate path.

Commits: `20f7b28` `a1516a4` `fff5439` `02b5d78` on the worktree branch.

## Substrate migration — SHIPPED in `worktree-fork-sentinel`

`internal/store` now lives on figwal. Two backing implementations of `Log[T]`:

- `FileLog` — legacy NDJSON file (one `aria.jsonl` per aria, one
  `translations/<provider>.jsonl` per translator). Read-side only; new arias
  never get this shape.
- `FigwalLog` — figwal segments. `arias/<id>/aria/<segment>.jsonl` for the IR,
  `arias/<id>/translations/<provider>/<segment>.jsonl` for translators.

Selection: legacy file on disk pins legacy; otherwise figwal. The env gate
`FIGARO_USE_FIGWAL` existed for one phase and has been removed; figwal is
default.

Interface tightening: `Stream.Truncate` removed (no callers after sentinel
work). Whole interface renamed `Stream[T]` → `Log[T]`.

Commits: `38b2c2a` `6fcf983` `fac87a6` `926acc1` `4c09402` on the worktree
branch.

## Disk layout — what fork *will* produce (NOT shipped)

```
arias/abc12345/                 → arias/abc12345/                  RO branch point
  aria/<segments>                 aria/<segments>                  prefix [1..N-1]
  chalkboard.json                 chalkboard.json                  rolled back to N-1
  meta.json                       meta.json                        recomputed
  translations/anthropic/         translations/anthropic/          prefix
                                  abc12345/                        old-future child
                                    .fork
                                    aria/<segments>                suffix [N..]
                                    chalkboard.json
                                    meta.json
                                    translations/anthropic/
                                  v2/                              fresh fork
                                    .fork
                                    (empty until first append)
```

Note: disk shape now uses figwal's `aria/` segment dir (not the legacy
`aria.jsonl` file) per the migration.

## Hierarchical aria IDs — NOT shipped

Aria IDs become slash-separated paths. Concrete edits:

- `internal/rpc/aria_id.go`: widen charset to `[A-Za-z0-9_/-]`; reject `..`,
  empty path components, leading/trailing `/`. Cap remains 64 chars total
  (path included).
- Socket-name derivation: replace `/` with `__` so
  `figaros/abc12345__v2.sock` is filesystem-safe. New helper alongside the ID
  validator.
- Backend paths: already use `path.Join(base, "arias", id)`. Stop assuming a
  single component anywhere we splat.
- Registry maps (`pidToFigaro`, `figaros`): keys are strings; slash-keys work
  as-is.
- Persistent bindings on disk: ID strings round-trip; no schema change.
- `Backend.List` walks the tree depth-first, returns leaf IDs (and branch
  points, depending on the consumer).

This is the next standalone phase — it lands without any fork-RPC behavior
change.

## Three-stream fork — NOT shipped

`figaro.fork` (agent socket, not angelus) handler:

1. Send `interrupt` to current `turnCtx`. Wait for the turn to drain.
2. Resolve `FigaroLT = AtLT` to per-translator local indices via the FK
   side-index maintained by FigwalLog.
3. For each open figwal log (aria + every translator), call
   `Cached.Fork(atIdx, freshName, oldFutureName)`. Per-log atomicity is
   figwal's `.fork-pending` sentinel.
4. Compute the parent's rolled-back chalkboard: replay IR patches `[1..N-1]`
   into a fresh `chalkboard.Open` snapshot; atomic-write to parent
   `chalkboard.json`. Copy parent's pre-fork chalkboard into the old-future
   child's dir. Fresh child gets the same rolled-back snapshot.
5. Recompute `meta.json` for each child (cumulative tokens / message count /
   last-active up to that child's slice).
6. Return `ChildID` (full path). Agent calls `os.Exit(0)`; angelus reaps.
7. Angelus rebinds the originating PID to the fresh child (per resolved
   question 4).

If steps 3-5 partially fail, the per-log `.fork-pending` files block re-open.
Operator inspects; no automatic rollback in v1.

## RPC additions — NOT shipped

**Agent socket:**

```go
const MethodFork = "figaro.fork"

type ForkRequest struct {
    AtLT          uint64
    FreshName     string
    OldFutureName string  // optional; "" → path.Base(parent)
}
type ForkResponse struct {
    ChildID       string  // full hierarchical path of fresh child
}
```

**Angelus socket:** extend `pid.bind` response.

```go
type BindResponse struct {
    Bound    bool
    Children []ChildInfo  // populated when Bound == false
}
type ChildInfo struct {
    Name           string  // path component, not full ID
    LastModified   time.Time
    MessageCount   int
    Label          string  // from meta.json, optional
}
```

## CLI additions — NOT shipped

- `figaro fork <at_lt> <fresh_name> [<old_future_name>]` — operates on
  attended aria.
- `figaro fork --aria <id> <at_lt> <fresh_name> [<old_future_name>]` —
  explicit aria.
- `figaro attend <id>` — when angelus returns `Bound: false`, render an
  indented menu (number + name + last-modified + message count), prompt
  selection, recurse on the chosen child, bind the leaf.
- `figaro list` — render as a tree (depth-first walk of `arias/`); branch
  points get a trailing `*`. Existing flat output stays available behind
  `--flat`.

## Phases (commit boundaries)

1. **figwal:** `Log.Fork` second name param. ✅ `c32c9b1`.
2. **figaro:** interrupt sentinel. ✅ `20f7b28` `a1516a4` `fff5439` `02b5d78`.
3. **figaro:** figwal `Log[T]` migration (Stream → Log rename included).
   ✅ `38b2c2a` `6fcf983` `fac87a6` `926acc1` `4c09402`.
4. **figaro:** hierarchical aria IDs (validator, socket name, recursive
   `Backend.List`, tree rendering). Land standalone, no behavior change yet.
5. **figaro:** `figaro.fork` agent RPC + CLI for the IR log only. Hand-test on
   a throwaway aria.
6. **figaro:** extend fork to coordinate translator logs.
7. **figaro:** chalkboard rollback + per-child meta.json recompute.
8. **figaro:** `pid.bind` branch-point response + CLI selection loop.
9. **figaro:** `figaro list` tree rendering + branch-point annotation.

Each phase commits independently and runs the slice loop from the `figaro-dev`
skill.

## Out of scope (v1)

- Automatic rollback of partial fork failures. Operator-driven recovery via
  `.fork-pending` sentinels.
- Merging forks back together.
- Concurrent fork on multiple arias.
- Renaming an aria after creation (deferred to `Store.Rename` if/when
  prioritized).
- Cross-aria reads.

## Risks

- **Chalkboard rollback** is the trickiest piece. Patches in `aria.jsonl` are
  full snapshots vs deltas; need to confirm before phase 7. Gate this behind a
  test that writes N entries with patches and asserts the rolled-back state
  matches a fresh replay.
- **Translator/IR alignment.** If a translator's FigaroLT FK is sparse (gaps),
  the fork-point alignment needs care. Either fork the translator at the
  smallest LT whose FigaroLT >= N, or just forward-walk after fork.
- **Active socket subscribers.** Existing notification streams to the parent
  agent see EOF on agent exit. Document this; clients reconnect to the chosen
  child.
