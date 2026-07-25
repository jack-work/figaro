# Figaro architecture

The durable shape of the system. Conventions drift — when in doubt, read the
source named here and trust it over this file.

## Three roles, one binary

- **CLI** (`internal/cli`) — what the user runs. Connects to the angelus or a
  per-aria socket and renders the stream.
- **Angelus** (`internal/angelus`) — the long-running supervisor (single
  instance via flock). Owns the registry of arias, spawns per-aria agents,
  routes pid bindings, serves `figaro.list`/`create`/`kill`/`attach`. Survives
  shells. `figaro rest` stops it; the next command respawns it.
- **Agent** (`internal/figaro`) — one per aria (= one conversation). Owns the
  figLog (IR), the chalkboard, the tool registry, and the turn loop. Mutations
  funnel through its **inbox** (an event queue), so there is exactly one
  writer to the chalkboard and the log — e.g. a `figaro set` arriving mid-turn
  is serialized, not raced. Chalkboard *reads* need no inbox: snapshots are
  immutable and published atomically (see the chalkboard section).

## The IR — `internal/message`

The conversation's source of truth: an append-only log of `message.Message`
(stored via figwal, NDJSON segments). Provider wire formats are *derived* from
it and cached; the IR is canonical.

A `Message` has a `Role` (`user` | `assistant` | `tool_result` | `system` |
`system.interrupt`) and `[]Content`. Content `Type` is one of `text`,
`thinking`, `tool_invoke` (assistant calls), `tool_result`, `interrupt`,
`image`. Messages also carry `Patches` (chalkboard mutations riding on a tic),
optional `Usage`, `Model`/`Provider`, `StopReason`, and a monotonic
`LogicalTime` (LT). The IR is provider-agnostic: it holds **no** provider
secrets — notably no Anthropic thinking *signature* (that lives only in the
provider's wire cache; see Provider layer).

## The chalkboard — `internal/chalkboard`

Per-aria key→JSON state. Two namespaces:

- `system.*` — harness-reserved. Providers read these directly
  (`system.credo`, `system.model`, `system.cwd`, `system.cache_control`,
  `system.thinking_budget`, `system.thinking_effort`, …). **Hidden from the
  agent**: `chalkboard.Render` skips any `system.` key.
- everything else — surfaced to the agent. On the tic where a key changes,
  `Render` projects it as a `<system-reminder name="<key>">…</system-reminder>`
  text block (templated if a template exists, else the bare value). This is
  how the agent learns its `aria_id`, `mantra`, skills, etc. A boot patch
  stamps runtime fill-ins each first turn (`system.cwd`, `system.root`, and a
  non-system `aria_id` so the agent can address itself on the CLI).

### Representation — immutable tree, atomic publication

`Snapshot` is a **two-word handle on an immutable AVL tree** with structural
sharing (`tree.go`, lifted from `github.com/jack-work/pstate`), not a map:

```go
type Snapshot struct {
	root    *node   // immutable, shared between snapshots
	version uint64
}
```

- `Clone()` is the **identity function**. It used to deep-copy every value on
  every read (per RPC, per turn, inside `chalkboardString`).
- `Apply` **path-copies**: only the O(k·log n) nodes on the touched paths are
  new; every other subtree is shared with the receiver. A patch that changes
  nothing returns the receiver *pointer-identically*, and `State.Apply` then
  does not even mark the board dirty.
- `Diff` (`treediff.go`) is a merge-join by key with a **pointer-identity
  short-circuit**: `prev == next` proves a whole subtree is unchanged, so a
  one-key delta costs ~10 node comparisons on a 1024-key board. The
  short-circuit is a pure optimisation — the algorithm is correct without it,
  which matters because AVL rotations move nodes.
- Read through `Get`/`Has`/`Len`/`All` (lexical key order); construct from a
  map through `FromMap` (**the seam** — the only constructor); `AsPatch()` is
  the Set-only patch of every entry.
- `Value` (`value.go`) holds the caller's **exact bytes** (`raw`) plus a
  canonical form (compacted, object keys sorted recursively) used **only** for
  `Equal`. Nothing ever rewrites stored bytes. That is what keeps
  `chalkboard.json` byte-identical while making equality semantic: a value
  that changes only in key order, whitespace or escape spelling compares equal
  and fires **no** `<system-reminder>`. Numbers compare by literal token, so
  `1` and `1.0` are different edits.
- `MarshalJSON`/`UnmarshalJSON` emit and read the **flat object** form
  (`{"key": value, …}`, keys lexical) — the on-disk `chalkboard.json`, the
  `figaro.chalkboard` RPC response and `store.chalkboardReduce` all depend on
  it. Values are handed to `encoding/json`, never concatenated raw: `encoding/
  json` compacts a raw message and rewrites `<`, `>`, `&` as `\u003c`,
  `\u003e`, `\u0026`, and that post-processing is part of the bytes on disk.

`State` (`state.go`) is **one writer, many readers**. The writer is the agent's
drain loop (`act` → `applyControlPatch` → `State.Apply`); readers are the
`figaro.chalkboard` handler, `Agent.ApplyLoadout` and
`Agent.chalkboardString`/`chalkboardInt`, all on RPC goroutines. `State`
publishes `{snapshot, dirty}` as one immutable value through an
`atomic.Pointer`, so readers are **lock-free** and always see a complete
board. (Before that, `State.snapshot` was a plain field read with no
happens-before edge — a genuine data race, 11 reports per `-race` run.) One
writer means the update path stores unconditionally; no CAS loop. `Save`
clears the dirty flag with a single non-looping CAS.

Loadouts (`internal/outfit`) assemble the boot chalkboard from `config.toml`'s
`default_loadout` chain. `fileName`/`dirName` tables load file bodies as
content envelopes (`{frontmatter|content, filePath}`) — skills come in this
way (`skills.<base>`), so the agent sees a skill's frontmatter and reads its
body on demand. Bundled first-party skills merge under the user's by name.

## The wire protocol — `internal/rpc`

Per-aria request methods: `figaro.qua` (prompt; the reply streams back as
notifications), `figaro.context`, `figaro.interrupt`, `figaro.set`,
`figaro.loadout`, `figaro.chalkboard`, `figaro.read` (catch-up + follow).
Angelus: `figaro.create`/`kill`/`list`/`attach`, `pid.bind`/`resolve`/`unbind`.

The reply is a **server-authoritative live-render stream** of notifications:

- `log.snapshot {role, nodes}` — the live unit's full node list (unit start /
  resync).
- `node.open` — append a node.
- `node.patch {index, field, at, del, ins}` — splice a node's streamed string
  field (prose markdown, tool output).
- `node.set {index, status, name, args}` — update a tool node's scalars.
- `log.commit` — freeze the live unit; the next is new.
- `turn.done` — the turn went idle.

There is no client-side unit index; the server drives positions.

## Live-render node model — `internal/livedoc` + `internal/cli`

A live unit (one turn) is an **append-only, index-stable** `[]Node`. A `Node`
is `prose` | `thinking` | `tool` (tool carries `Name`/`Args`/`Status`
∈ `running|ok|error`/`Output`). `DiffNodes(prev,next)` emits `OpOpen` /
`OpPatch` (field splice) / `OpSet` (tool scalars); `ApplyOp` folds an op in.
`internal/compose` builds nodes from the IR; `internal/render` renders prose
via glamour (`render.Prose`).

The CLI painter (`internal/cli/live.go`, `nodes.go`) flushes finalized rows to
**native terminal scrollback** and re-renders only the live tail in place.
Hard-won invariants — break these and the cursor desyncs (duplicated/erased
rows):

1. **One physical line per row.** Every rendered row passes through
   `clipToWidth`, which clips to the viewport width AND flattens control
   chars (newline/tab/CR) to spaces. A multi-line tool command must not smuggle
   a newline into a row.
2. **Flush watermark is a NODE index** (`flushedNodes`), not a row count.
   Flushed nodes are frozen in scrollback and never re-rendered — so a
   verbosity toggle (Ctrl-O) only ever repaints the still-live tail, never
   reaches back into immutable scrollback. `flushedRows` separately tracks
   viewport-overflow rows flushed off the top of the first live node.
3. **The live region never exceeds the viewport** (overflow flushed off the
   top, reflow-safe) — relative cursor moves clamp at viewport edges, so a
   taller-than-viewport live region desyncs.
4. `commit()` descends with real newlines (CUD clamps at the bottom instead of
   scrolling). The bookend (status rule) is appended to the live tail every
   repaint, never flushed.
5. The VT test harness (`internal/cli/vt_test.go`, `newVTH` = finite scrolling
   viewport) is the source of truth for painter correctness. Transient
   glitches self-heal on the next op — assert the screen **after every frame**,
   not just the final one.

Presentation is a pure client concern: a single `verbose` toggle (Ctrl-O, or
Ctrl-T as alias) expands tool inputs; thinking renders muted by default. The
wire always carries full data.

## Provider layer — `internal/provider/anthropicsdk`

Translates IR ↔ Anthropic wire and caches the per-aria wire bytes
(`store.Log[[]json.RawMessage]`, keyed by figaro LT).

- **Cache the exact accumulated turn, never a lossy re-encode.** `drainStream`
  returns both the figaro IR and the raw `anthropic.Message`; `Send` caches
  `acc.ToParam()` — the SDK's response→request projection, which preserves
  thinking-block **signatures** and `redacted_thinking` verbatim. Re-encoding
  from the IR would drop the signature (the IR has no home for it) and a
  replayed unsigned thinking block is a 400. The cache-miss fallback drops
  thinking blocks rather than emit unsigned ones.
- **Extended thinking** (`assemble.go::applyThinking`). Two model families:
  adaptive (Opus 4.6/4.7/4.8, Sonnet 4.6) take `{type:"adaptive"}` +
  `output_config:{effort}` and ignore a token budget; older models take
  `{type:"enabled", budget_tokens}`. Crucially, set `display:"summarized"` —
  the Claude-Code/OAuth default is `"omitted"` (signature only, empty thinking
  text). Knobs: `system.thinking_effort` (low|medium|high|xhigh|max; default
  high) and `system.thinking_budget`.
- **Automatic prompt caching** (`resolveCacheControl` / `markCacheBreakpoints`)
  — see cache-control.md.
- **Auth** (`auth.go`) — OAuth via hush; Claude-Code identity headers + beta
  flags. `anthropic-beta` does not need `interleaved-thinking` for adaptive
  models.

## Tools: bash & backgrounding

The bash tool (`internal/tool/bash.go`, `exec_local.go`) runs each command via
`bash -c` in its **own process group** (`Setpgid`). It waits up to `yieldMs`
(default 10s); if the command is still running it **auto-backgrounds as a
tracked session** (not killed) — follow up with the **`process`** tool. Args:
`background:true` (background immediately), `timeout` (hard-kill deadline,
default 30m), `pty`, `yieldMs`. `killProcessGroup` SIGKILLs the whole group
only on **timeout or cancel**, never on normal completion.

Completion is signalled by the stdout/stderr **pipe reaching EOF**, not the
foreground exit — and that has consequences for `&`:

- A bare `cmd &` child **inherits** the stdout/stderr pipe, so the command
  keeps "running" until that child finishes and backgrounds as a session. Its
  work is captured, but **not done when the call returns** — poll via the
  `process` tool, or use `cmd & wait`, or just run serially. (An agent that
  fires parallel `git clone … &` and immediately `ls` will see incomplete
  results — it must wait.)
- A **redirected** `cmd >/dev/null 2>&1 &` releases the pipe, so the call
  returns immediately and that child **orphans**: it keeps running to
  completion but is untracked — no captured output, no supervision, invisible
  to the `process` tool. Fine for true fire-and-forget (e.g. a quick
  `figaro set … &`); don't rely on it for work you need to observe.

Rule of thumb: don't background with bare `&` and assume completion. Use
`background:true` + the `process` tool, `& wait`, or serial commands.

## Storage

State root `~/.local/state/figaro/arias/`: parallel XWAL trees in `ir/`,
`chalkboard/`, and `translations/<provider>/`, plus `_meta/<id>.json`
for list/status metadata. See arias.md for reading these safely.
