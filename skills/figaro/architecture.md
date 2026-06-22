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
  funnel through its **inbox** (an event queue) so the single-owner chalkboard
  and log are never touched concurrently — e.g. a `figaro set` arriving
  mid-turn is serialized, not raced.

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

Per-aria key→JSON state. `State` is **single-owner, not concurrency-safe**
(mutated only via the agent's inbox). Two namespaces:

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

## Storage

State root `~/.local/state/figaro/arias/<id>/`: `aria/<NNN>.jsonl` (IR
segments), `chalkboard.json`, `derived/` + `meta.json` (derived stats),
`translations/<provider>/<NNN>.jsonl` (wire cache, FK'd to the IR by
`FigaroLT` — cache, not truth). See arias.md for reading these safely.
