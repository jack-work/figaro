# Managing CLI UX Changes — A Field Guide

*A map of how the figaro CLI presents a turn on the screen, the moving
parts you need to think about when you change that presentation, and
the places where the rendering invariants live. Built specifically so
you can navigate a UX bug without re-deriving the architecture each
time.*

______________________________________________________________________

## 1. The shape of a turn

What the user sees when they type `q ...` or `figaro q ...`, top to
bottom:

```
─── b92a7662 · 11:35:02 ──────────────────────────────────────   ① top status banner

> your prompt here                                              ② echoed prompt

──────────────────────────────────────────────────────────────   ③ separator

[markdown response, paced rune-by-rune through largo]            ④ assistant text

⠋ ▶ bash · pwd && date                                          ⑤ tool header (single)
[live tool stdout]
✓ ▶ bash · pwd && date
─── (separator)

─── batch (3) ───────────────────────────                       ⑤′ tool header (batch)
  ⠹ bash · pwd && date            (1.2 KiB)
  ⠼ bash · ls -la                 (340 B)
  ✓ bash · uname -a               (1 line, 84 ms)
─── ─────────────────────────────────────

[markdown continues, more tool rounds, etc.]                     ⑥ subsequent rounds

─── b92a7662 · 11:35:08 · 5.9s ──────────────────────────────    ⑦ closing status banner
```

Each region has a different **rendering mode** and different
**ownership of the terminal cursor**. UX bugs almost always live at
the seams between these modes.

______________________________________________________________________

## 2. The moving parts (and where to find them)

### 2.1 Server side — what the wire delivers

The agent (the long-lived figaro process) emits JSON-RPC notifications
into a single ordered stream, one connection per attached client. The
methods are defined in **`internal/rpc/methods.go`** and their param
shapes in **`internal/rpc/rpc.go`**:

| Method | When | Carries |
|---|---|---|
| `stream.delta` | Provider SSE text delta arrives | `text`, `content_type` |
| `stream.thinking` | Reasoning delta (Anthropic extended thinking) | `text` |
| `stream.tool_batch_start` | A round contains > 1 tool_use blocks (omitted for size 1) | `size`, `tools[]` (id, name, args) |
| `stream.tool_start` | A tool begins executing | `tool_call_id`, `tool_name`, `arguments` |
| `stream.tool_output` | Tool stdout/stderr chunk | `tool_call_id`, `tool_name`, `chunk` |
| `stream.tool_end` | Tool returns | `tool_call_id`, `tool_name`, `result`, `is_error` |
| `stream.tool_batch_end` | All tools in a batch finished | `size` |
| `stream.message` | Provider's full assistant `message.Message` for the round | `logical_time`, `message` |
| `stream.error` | Advisory error from the agent | `message` |
| `stream.done` | The whole turn is finished | `reason` |

**Wire-order guarantee.** The agent serializes notifications through a
per-connection mutex on `Conn.Send` (see `internal/rpc/rpc.go` and the
`fanOut` machinery in `internal/figaro`). The client's `readLoop` is
single-goroutine and calls the user's `OnNotify` synchronously in the
order events arrive on the socket. **Every CLI rendering invariant
should rely on this ordering.** If you suspect out-of-order delivery,
you are almost certainly wrong about the source — the race is more
likely on the producer side (see `b82d093` for the historical fix
where a fan-out goroutine raced the `select` between deltas and figaro
messages).

**Round structure on the wire** (in order, for one assistant turn):

```
delta delta delta ... message
[ if tools: tool_batch_start? tool_start ... tool_output* tool_end ... tool_batch_end? ]
[ next round: delta ... message ... ]
done
```

Two important emission rules in `internal/figaro/turn.go`:

- `stream.message` is **the punctuation between SSE deltas and tools.**
  All deltas for round R land *before* the round-R message; the round-R
  message lands *before* the first `tool_start` of round R.
- `stream.tool_batch_start` is only emitted when a round has size > 1
  tool calls. Size-1 rounds skip the batch bracket entirely so the
  client renders them with the live single-tool spinner.

### 2.2 Client side — the rendering pipeline

All CLI rendering for a turn lives in **`mustPromptFigaro`** in
`cmd/figaro/main.go` (around line 1713). The function is the entire
client-side state machine for one turn. Its state, in order of
declaration:

```go
sw    *largo.Writer       // markdown renderer (glamour-based)
pace  *pacer.Pacer        // smooths bursty deltas → CPS-paced runes
rawOut io.Writer           // non-nil while largo is suspended
openTools int              // count of in-flight tool calls
batch *toolBatchState     // non-nil during a parallel batch
solo  *toolSoloState      // non-nil during a single-tool round
```

The dispatch happens inside `deliverEvent(method, params)`, which is
the only place that mutates rendering state. Each notification has a
case there. Read that switch as the canonical state machine.

**Key collaborators:**

| File | Role |
|---|---|
| `cmd/figaro/main.go::mustPromptFigaro` | Turn-level state machine, `deliverEvent` switch |
| `cmd/figaro/main.go::writeStatusLine` | ① / ⑦ banner |
| `cmd/figaro/main.go::writeSeparator` | ③ separator under echoed prompt |
| `cmd/figaro/main.go::toolDetail` | One-line summary for a tool header |
| `cmd/figaro/tool_solo.go::toolSoloState` | ⑤ single-tool spinner + freeze-on-output |
| `cmd/figaro/tool_batch.go::toolBatchState` | ⑤′ in-place batch row painter |
| `internal/pacer/pacer.go` | Steady-rate emitter wrapping largo |
| `largo` (external) | Markdown buffering + glamour render + Suspend/Resume |

### 2.3 The three rendering regions

The terminal is, at any moment, in **exactly one** of these modes:

1. **Markdown region** — `sw` (largo) owns the cursor. Bytes pushed
   through `pace.Push` or `sw.Write` are buffered, block-boundary
   detected, then re-rendered as styled markdown. Runs of plain text
   echo through immediately; on a blank-line boundary largo erases
   what it wrote and replaces it with the glamour-rendered version.
1. **Raw pass-through region** — entered by `rawOut := sw.Suspend()`.
   The cursor belongs to `rawOut`. Writes go straight to the terminal
   verbatim. ANSI escapes work directly. This is where tool stdout,
   tool spinners, and batch rows live.
1. **Naked stdout** — used only for the framing chrome
   (`writeStatusLine`, `writeSeparator`, the echoed prompt). These
   write directly to `os.Stdout` *before* largo is created or *after*
   it's done.

**Transitioning between modes is the source of every "weird redraw"
or "ghost line" UX bug.** Always go through `resumeIfSuspended()` and
`pace.Flush()` + `sw.Flush()` at the seams, in that order.

______________________________________________________________________

## 3. The invariants you must preserve

### 3.1 Ordering invariants (across modes)

Before suspending largo (entering raw mode):

1. `pace.Flush()` — drain queued runes into largo.
1. `sw.Flush()` — render any buffered markdown so it appears *above*
   the tool header, not below or interleaved.
1. `rawOut = sw.Suspend()` — only if `rawOut == nil`.

Before resuming largo (exiting raw mode):

1. Stop and freeze any in-flight spinner (`solo.Freeze()`).
1. `sw.Resume()`.
1. Set `rawOut = nil`.

The helper `resumeIfSuspended()` enforces this in one place — call it
on `done`, on `error`, after `tool_batch_end`, and any other time you
need markdown back.

### 3.2 Suspend pairing (avoid double-Suspend panic)

`largo.Suspend` panics if called twice without an intervening
`Resume`. The CLI handles that with the `openTools` counter:

- `tool_start`: only `Suspend` when `rawOut == nil`. Always `openTools++`.
- `tool_end`: `openTools--`. Only `resumeIfSuspended()` when
  `openTools == 0 && batch == nil`.
- Batch path: `tool_batch_start` enters raw mode once for the whole
  batch; per-tool starts/ends never touch suspend state inside a
  batch — they just update rows. `tool_batch_end` is what triggers
  the resume.

If you add a new event that touches the cursor, walk through every
combination: `solo`, `batch`, `solo→batch?` (impossible — agent never
mixes them in a round), `error mid-batch`, `done mid-tool`.

### 3.3 The pacer can hold runes after the wire has gone quiet

`pace.Push(text)` enqueues; the pacer drains at the configured
`TargetCPS`. After `stream.message` arrives the wire is silent for
the round, but the pacer may still have queued runes. **Always
`pace.Flush()` before any boundary that needs the assistant's
preamble visible** (tool start, end of turn, error, mode switch).
The `MethodMessage` handler exists exactly for this — to flush
between text and the tool header.

### 3.4 Animated rows must not race the cursor

Both `toolSoloState` and `toolBatchState` run a ticker goroutine
that rewrites lines in place via cursor-up + erase-line ANSI
sequences. The moment any *other* writer — tool stdout, more
markdown — touches the same region, the cursor math goes wrong.
Rules:

- Solo: **freeze the spinner the instant the first `tool_output`
  arrives.** Live tool output streams below a frozen header. The
  freezing is what `solo.Freeze()` does and it's idempotent.
- Batch: live tool output is **suppressed** during the batch
  (only buffered for an error post-mortem). Rows update via the
  ticker until each tool's `tool_end`.
- Both: methods that mutate state and the ticker share a `sync.Mutex`.
  If you add a new method, take the lock.

### 3.5 The terminal isn't always a TTY

`writeStatusLine`, `writeSeparator`, and the spinners check
`term.IsTerminal(...)` before emitting ANSI. Anything you add that
emits ANSI must do the same — or be gated behind the existing raw-mode
path, which is only entered for interactive sessions in practice.

______________________________________________________________________

## 4. The framing chrome (banner / echoed prompt / separator)

This is the simplest thing to change, and the most visible. It runs
*outside* largo, on naked `os.Stdout`. Toggles live in config:

- `loaded.StatusLine()` — gates the top + bottom banners (`writeStatusLine`).
- `loaded.EchoPrompt()` — gates the echoed `> prompt` + the separator.

The current order, set in `mustPromptFigaro`'s opening lines, is:

```go
if loaded.StatusLine() {
    writeStatusLine(os.Stdout, figaroID, startedAt, 0)
}
if loaded.EchoPrompt() {
    fmt.Println()
    fmt.Println("> " + prompt)
    fmt.Println()
    writeSeparator(os.Stdout)
} else if loaded.StatusLine() {
    fmt.Println()
}
```

If you change framing, also check the *closing* banner at the bottom
of `mustPromptFigaro` (around line 2029) so the visual symmetry holds.

______________________________________________________________________

## 5. Pacer mechanics (assistant text)

`internal/pacer/pacer.go` wraps any `io.Writer` (here: `sw`) with a
steady-rate output. Knobs:

- `TargetCPS` — characters per second. 0 disables pacing
  (synchronous pass-through).
- `FirstByteBypass` — until this much wall time has elapsed since the
  first byte of a turn, runes pass through immediately. Preserves
  TTFT (time-to-first-token) feel.

Configured via `loaded.StreamCPS()` and `loaded.StreamFirstByteBypassMs()`.
The pacer has its own goroutine; remember to `pace.Close()` (deferred
at construction) and `pace.Flush()` at boundaries.

If you ever want pacing *off* for a non-interactive run, set CPS to 0
through config rather than threading a bypass through code.

______________________________________________________________________

## 6. The literal flag (`-l` / `--literal`)

The `figaro aria` command bypasses largo entirely when `-l` is
passed. This is the model for "I want the raw payload, please":
write directly to `os.Stdout` and skip glamour rendering.

If you add a new read-only command that prints assistant content,
plumb the same flag the same way — don't reinvent.

______________________________________________________________________

## 7. Recipes — common changes and the files they touch

| Change | Files | Notes |
|---|---|---|
| Adjust banner format / glyphs | `cmd/figaro/main.go::writeStatusLine` | Keep the TTY/non-TTY branches in step. |
| Adjust separator / banner spacing | `mustPromptFigaro` opening + closing blocks | Symmetry top vs. bottom. |
| New tool header layout (single) | `cmd/figaro/tool_solo.go::formatHeader` | Don't change line count without revisiting cursor math. |
| New tool header layout (batch) | `cmd/figaro/tool_batch.go::formatRow` | Frame parameter is the spinner phase. Tests in `tool_batch_test.go`. |
| Show new info on tool header | `toolDetail` in main.go + the State's `name`/`detail` fields | Keep `detail` short; truncation is at 200 chars in solo, ~80 in batch. |
| Add a new notification | `internal/rpc/methods.go` + `rpc.go` (param type) + handler in `deliverEvent` + emitter in `internal/figaro` | Walk the suspend / openTools / batch state combinations. |
| Tweak pacer feel | `~/.config/figaro/config.toml` (`stream.cps`, `stream.first_byte_bypass_ms`) — no code change. |
| Change which terminal mode tool output paints in | `deliverEvent`'s `MethodToolOutput` and `MethodToolStart` cases | Currently raw-mode for both solo and the per-tool buffer in batch. |
| Add a new global flag (like `-l`) | The flag parser in `main.go` + the renderer that respects it | The bundled-short-flag expansion (`-alv` → `-a -l -v`) is in the parser. |
| Trace what the CLI is doing | `figOtel.Event(ctx, "cli.recv.X", attrs...)` is already inside every `deliverEvent` case. Add more attributes or new events as needed. |

______________________________________________________________________

## 8. Debugging UX bugs — a checklist

When something looks wrong, in order:

1. **Where on screen?** Identify the region (① through ⑦ above). The
   region tells you which file owns the bug.
1. **What was the wire order?** Pull the run from
   `~/.local/state/figaro/arias/<id>/aria.jsonl` and the otel trace.
   Each event in `deliverEvent` emits `cli.recv.<method>` with
   useful attributes (`size`, `tool`, `bytes`). If a method is
   *missing* from the trace, the bug is upstream of the CLI. If
   it's present but the screen disagrees, the bug is in the case
   handler.
1. **What mode was the renderer in?** Mentally walk `rawOut`,
   `openTools`, `batch`, `solo` through the sequence. The bug is
   almost always a missing `pace.Flush()`, a missing
   `resumeIfSuspended()`, a forgotten `solo.Freeze()`, or an
   `openTools` accounting slip.
1. **Is it a TTY-only bug?** Run with output piped (`q ... | cat`) and
   compare. If it only manifests on a TTY, the spinner / cursor math
   is the suspect.
1. **Does it reproduce against a re-played transcript?** The aria
   JSONL is a complete record. With sufficient effort you can build a
   driver that replays a recorded turn into a fresh `mustPromptFigaro`
   for repeatable bisection. (We don't have that harness yet — adding
   one would pay for itself.)

______________________________________________________________________

## 9. Known live concerns

(Pulled from `~/notes/figaro/some-issues.md` and recent debugging.)

- **Spinner not erased on tool completion in some races.** The
  symptom: a `─── ⠋ ▶ bash · ... ───` row stays on screen with the
  spinner glyph instead of flipping to `✓`. Hypothesis: the
  `tool_end` for that round is delayed (or interleaved with another
  round's events), and `solo.Done(...)` fires after the row has
  already been scrolled off, so the in-place rewrite goes to the
  wrong line. Worth instrumenting `cli.recv.tool_end` with the
  logical time and the row's `startedAt` so we can correlate.

- **Long pauses after a tool_use is committed but before the next
  notification.** The CLI doesn't currently know whether the agent's
  `stream.message` for round R is the *last* event before tools fire
  or whether more rounds may still come. A per-event otel emission
  on the *server* side, tagged with logical time, would close this
  loop.

______________________________________________________________________

## 10. Where to start when you bring me a UX bug

Hand me, in this order:

1. The figaro ID and approximate wall-clock of the bad turn (so I
   can pull the aria + traces).
1. A screenshot or paste of the actual on-screen output, with the
   ugly part marked.
1. What you *expected* to see instead.

With those three I can locate the region, walk the wire-order,
identify which `deliverEvent` case (or framing helper) misbehaved,
and propose the smallest change that fixes the seam without breaking
its neighbors.

*Ecco la mappa. Ora portiamo il bug.*
