# Tool rendering & output refactor

Status: planning. Branch `feat/generic-tool-ir` (worktree `tool-ir`), off `main`.
First commit: revert of `b942a36` (the write line-by-line change) — see rationale below.

## Governing principle

**The client is a dumb, generic renderer of a typed tool description carried by the
figaro UI IR. It has ZERO per-tool control flow — no `switch n.Name`, no special
cases for bash/read/write/anything.** Every tool renders through exactly one path:
`[status glyph] name  <summary>` then a tail-bounded `<preview>`. All tool-specific
knowledge lives on the producer side (the runtime / the Tool implementations that
feed the IR), never in the renderer.

Current violations to remove:
- `internal/cli/nodes.go` `toolArgSummary` — `switch n.Name { case "bash": … case "read","write","edit": … }`. Client-side tool knowledge. Must go.
- `logEmitTools` / `renderToolHeader` / `stableForm` (from the flush work `5112389`) — also removed by the transcript pivot; the log-emit *concept* dies with it.

## The two streams a tool node shows (and where they come from)

A write's `content` is NOT tool output — it's part of the tool_use **arguments**,
which the model emits incrementally as `input_json_delta` over the provider API.
There are two genuinely-distinct streams, both of which should feed the tool node's
live preview generically:

1. **Arg stream (generation phase).** The model writes the args token-by-token.
   Provider fires `bus.PushToolInvokeDelta(id, partialJSON)`
   (`anthropicsdk/stream.go:117`) → `evToolArgs` (`turn.go:77`). **Currently DROPPED:**
   the drain-loop switch (`turn.go:278-287`) has cases for `evToolStart` / `evToolReady`
   but **no `evToolArgs`**; args only appear, fully decoded, at `evToolReady`.
2. **Output stream (execution phase).** bash stdout, a streamed read, process logs —
   via `onOutput` → `evToolChunk` → `a.partials[id]` → `compose.Nodes`.

`b942a36` (reverted) fabricated stream (1) inside stream (2): it replayed the
already-complete write content through `onOutput` at execution time, faking the
generation-time stream the runtime was throwing away. Wrong phase, wrong source,
and per-tool. Reverted.

## Workstream (the "output governor" + generic IR)

### A. Stream tool args live (fixes write correctly, generically)
- Handle `evToolArgs` in the drain loop: accumulate partial arg JSON per tool id.
- **Tolerant partial-JSON extractor:** pull the in-progress value of a string field
  (e.g. `content`) out of unterminated JSON (`{"path":"/f","content":"line1\nlin`).
  This is the real work and why it was originally shortcut. (Streaming/partial-JSON
  parsers exist for exactly this — it's how tool-use UIs render partial args.)
- Surface the streaming args in the IR tool node as a typed preview field. The
  renderer shows it generically — it never knows "write" means anything.
- `write.Execute` emits no display output (revert already removed the loop; the
  remaining single summary emit can stay or move to the generic path).

### B. Output governor (truncation + aggregate rate-limit)
Today every tool chunk does `a.partials[id] += chunk` then a full
`composeTurn` + `emitDelta` (`turn.go:295-306`) — O(nodes) per chunk, one socket
frame per chunk, no coalescing. Truncation is render-side only (`renderToolNode`
clamps to `bashCap` at paint time), so the socket ships the *entire* output to show
the last 10 lines.

One component between tool emission and `ariaSrv.Update`:
- **Bounded tail buffer per tool node** — fold chunks into a last-N-lines window;
  what reaches compose/socket is already the display limit. Safe because the live
  preview is display-only (the model's copy is the returned `Content` / committed
  `tool_result`, truncated separately). *Verify nothing downstream reads the live
  Output/preview as authoritative before ripping the full buffer.*
- **Aggregate coalescing** — flush accumulated state at a bounded cadence across ALL
  tools (natural hook: the ~11fps tick), not recompose-per-chunk. Kills the
  O(chunks × nodes) cost and prevents parallel tools from flooding.
Applies uniformly to arg-streams (A) and output-streams. No per-tool clamps.

### C. Typed tool description in the IR
The tool node carries render-ready, typed fields so the client renders with no
branching: `name`, `status`, a producer-computed `summary` line (replacing the
client's `toolArgSummary` switch — moves to the runtime or, cleaner, each `Tool`
declares its own display description), and the tail-bounded `preview` (arg-stream
or output-stream). The renderer is one uniform function over these.

## Removal ledger
- `b942a36` write line-by-line — **reverted** (commit 1 of this branch).
- `toolArgSummary` `switch n.Name` — remove; move formatting producer-side.
- Flush machinery (`5112389`: flushStable/printFresh/StableForm/LiveIndex/stableForm/
  logEmitTools/renderToolHeader/liveNodeIndex) — removed by the transcript pivot
  (see the transcript-refactor plan); the log-emit special case dies with it.

## Relationship to the transcript pivot
Separate concern (that one removes the half-dynamic painter and routes overflow to a
windowed transcript) but they converge in `nodes.go` tool rendering. Sequence them so
the generic renderer (C) is the single tool-render path both inline and transcript use.
