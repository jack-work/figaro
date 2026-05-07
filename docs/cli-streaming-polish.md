# CLI Streaming Polish — Report & Recommendations

*Notes on smoothing the output rate and giving the response a steady,
character-by-character feel — informed by what other LLM CLIs/SDKs are
doing, applied to our specific CLI in `cmd/figaro/main.go` +
`largo`.*

---

## 1. The problem we're trying to solve

Right now the figaro CLI writes provider deltas straight into largo as
they arrive over JSON-RPC. That means:

- **Burstiness.** Anthropic SSE chunks vary wildly: sometimes a single
  byte arrives, sometimes 60+ bytes in one delta. The terminal feels
  uneven — long stalls punctuated by big bursts of text appearing all
  at once.
- **No "typing" feel.** When a chunk contains 30 characters they paint
  in a single render tick, which reads as a paste rather than as the
  model thinking out loud. The pleasant typewriter effect modern LLM
  UIs have is missing.
- **Erase-redraw flicker.** Largo's block boundary detection can rewrite
  a line mid-flow when the buffered raw bytes get replaced by glamour-
  rendered output. The bigger the burst we feed it at once, the more
  visible that erase-replace is.
- **TTFT vs. smoothness tension.** Buffering for smoothness must not
  delay the *first* visible character. The user perceives "is it
  alive?" within ~200 ms; that has to keep working.

## 2. What modern stacks do

A quick tour of established practice (from Vercel AI SDK, llm-ui,
Upstash, dev.to articles, and Bubbletea conventions):

### 2.1 Vercel `smoothStream`
A `TransformStream` wrapper for `streamText` with two knobs:
- `delayInMs` — pause between emitted units (default 10 ms).
- `chunking` — `"word"` (default), `"line"`, or a custom chunk-detector.

Buffers the provider stream, splits into "natural" units (whitespace-
delimited words by default, or whole lines), and releases each unit on
a tick. CJK languages get a code-point splitter. ~5 ms/char (~200
chars/sec) is the recommended sweet-spot in the Upstash piece.

**Key insight:** smoothing happens between the provider and the
renderer — it's a *transform* in the stream, not a renderer setting.

### 2.2 llm-ui (React)
Renders at the **native frame rate of the display** (typically 60 Hz,
optionally 240 Hz). Algorithm: keep an internal buffer of unrendered
characters, and on each `requestAnimationFrame` emit *N* characters,
where *N* is computed from a target rate (chars/sec) and the actual
elapsed frame time. This is the same idea as a video game's
`deltaTime` loop applied to text.

Two notable refinements:
- **Look-ahead pacing.** It looks at how much buffered text is queued.
  If the queue is growing (provider faster than display), it speeds up
  emission to keep latency bounded. If shrinking, it slows down so the
  buffer doesn't drain to zero (which would cause visible stalls).
- **Code-block awareness.** Inside fenced code, it emits whole tokens
  at once rather than character-by-character — code reads better that
  way.

### 2.3 Bubbletea-style approaches
`tea.Tick` produces a `Msg` on a fixed interval. The pattern is:

```go
type tickMsg time.Time

func paceTick() tea.Cmd {
    return tea.Tick(16*time.Millisecond, func(t time.Time) tea.Msg {
        return tickMsg(t)
    })
}
```

The `Update` function dequeues N runes from a buffer per tick, returns
the new model + a fresh `paceTick()` command. Clean, but Bubbletea
ownership of the screen would be a bigger rewrite for us.

### 2.4 240-FPS chat UIs (dev.to)
`requestAnimationFrame` batching. Buffer many provider chunks per
frame, render once per frame. Equivalent in Go: a short `time.Ticker`
(say 16 ms ≈ 60 Hz) reading from an unbounded queue.

### 2.5 What Claude Code, codex, etc. seem to do
None of them publish their pacing source, but observation suggests a
similar approach: a small fixed-rate emit loop (~100–200 chars/sec
visible rate) with a buffer that absorbs provider burstiness. Tool
output is exempt — it streams raw at full speed.

## 3. How this maps to our architecture

The relevant code is `mustPromptFigaro` in `cmd/figaro/main.go`. Today
the flow is:

```
JSON-RPC notification (stream.delta)
    → deliverEvent goroutine (jsonrpc readLoop)
        → sw.Write([]byte(p.Text))            // straight into largo
            → largo echoRaw + drain on \n\n
                → terminal
```

The natural place to insert pacing is between `deliverEvent` and
`sw.Write`: keep a buffered queue of incoming text, drain it on a
ticker into largo at a configured rate. Tool output bypasses this
(already does — it goes through `rawOut`, which is the suspended
pass-through writer; raw bytes should appear at full speed).

### 3.1 Sketch of the pacer

```go
// streamPacer drains incoming provider deltas into a writer at a
// bounded rate, with a target chars-per-second and a backlog-aware
// speedup so the on-screen text doesn't fall arbitrarily behind the
// provider stream.
type streamPacer struct {
    out      io.Writer       // largo.Writer
    in       chan rune        // unbounded via slice + cond, or large buffer
    targetCPS int              // e.g. 200
    maxLagMs  int              // soft cap on pending characters

    done chan struct{}
}

func (p *streamPacer) push(s string) {
    for _, r := range s {
        p.in <- r
    }
}

func (p *streamPacer) run() {
    // Target one rune per tick at low rates; chunk multiple at high
    // rates. Re-evaluate cps from queue length each tick.
    base := time.Second / time.Duration(p.targetCPS)
    t := time.NewTicker(base)
    defer t.Stop()
    var buf []rune
    for {
        select {
        case <-p.done:
            // Drain any remaining buffered runes synchronously.
            for r := range p.in { p.out.Write([]byte(string(r))) }
            return
        case <-t.C:
            // Pull as many as we can without blocking.
            for {
                select {
                case r := <-p.in:
                    buf = append(buf, r)
                default:
                    goto emit
                }
            }
        emit:
            if len(buf) == 0 { continue }
            // Adaptive: if the queue has grown large, emit more per tick.
            n := 1
            if len(buf) > 40 { n = 4 }
            if len(buf) > 200 { n = len(buf) / 50 }
            if n > len(buf) { n = len(buf) }
            p.out.Write([]byte(string(buf[:n])))
            buf = buf[n:]
        }
    }
}
```

Refinements:

- **Word boundaries (Vercel-style):** instead of N runes per tick,
  emit until the next whitespace or up to N characters. Reads more
  naturally, especially if the underlying tick rate is coarse.
- **Code-block awareness:** detect when we're inside a fenced block
  (we already pass text through largo which tracks `inFence`) and
  emit faster / line-at-a-time inside fences.
- **Don't pace the first chunk.** First-byte should appear immediately
  (TTFT). Subsequent chunks get paced.

### 3.2 Where to plumb it in

In `mustPromptFigaro`:

```go
pacer := newStreamPacer(sw, /*cps*/ 200)
go pacer.run()
defer pacer.close() // drains and unblocks

// inside deliverEvent:
case rpc.MethodDelta:
    var p rpc.DeltaParams
    if json.Unmarshal(params, &p) == nil {
        pacer.push(p.Text)   // was: sw.Write([]byte(p.Text))
    }
```

Critical interactions:

1. **Tool boundaries.** Before `MethodToolStart` calls `sw.Suspend()`,
   the pacer must drain — otherwise we'd lose paced bytes after the
   suspend region opens. Add `pacer.flush()` before flush+Suspend.
   Same for `MethodMessage` and `MethodDone`.
2. **Largo's erase-replace.** Pacing fewer bytes per tick *helps* this
   — smaller updates mean the rendered/raw swap is less visible. The
   block-boundary trigger still works because the `\n\n` will
   eventually arrive in the buffer.
3. **Interrupt path.** Ctrl-C cancels the context; the pacer should
   stop emitting and drain quickly so the "interrupted" message
   appears promptly. Cap drain at, say, 50 ms.

### 3.3 Configuration

Add to `~/.config/figaro/config.toml`:

```toml
[ui]
stream_cps = 200          # characters per second; 0 disables pacing
stream_max_lag_ms = 250   # adaptive speedup threshold
```

Defaults that match observed user expectations: 200 cps, 250 ms lag.
`stream_cps = 0` disables pacing entirely (current behavior — handy
for piping or for users who want everything immediate).

## 4. Character-by-character append guarantee

Today, a provider delta `"Hello world"` becomes one `sw.Write` of
11 bytes. With a pacer at 200 cps:

- Tick 0 (5 ms): emit `H`
- Tick 1: emit `e`
- … and so on.

Each rune lands as its own `sw.Write` to largo. From largo's
perspective nothing changes — `Write` already buffers and `echoRaw`
already writes immediately. The only difference is *how often* and
*how large* the writes are. **Largo doesn't need to change.**

One subtlety: the OS-level write of one byte at a time is more
syscalls. At 200 cps that's still under 1k syscalls/sec — negligible.
If we ever felt the cost, we'd batch within a tick (e.g., one rune per
tick still results in one `Write([]byte(string(r)))` call, which is
already batched at the syscall layer because Go's stdout is buffered
when not a terminal — though when it *is* a terminal, line-buffered).

## 5. Recommendations, prioritised

**Tier 1 (do these):**

1. **Stream pacer with fixed target CPS + adaptive speedup.** Default
   200 cps. Drain hooks before tool boundaries, message, done,
   interrupt. Configurable in TOML.
2. **First-token bypass.** Skip pacing for the first ~80 ms of a turn
   so TTFT stays sharp.
3. **Pacer exempt for tool output.** Tool stdout goes through `rawOut`
   already; keep it that way — no pacing on raw pass-through.

**Tier 2 (next):**

4. **Word-boundary chunking inside paragraphs, line-boundary inside
   code fences.** Mirrors Vercel's `chunking: "word" | "line"` —
   reads more naturally than rigid N-runes-per-tick.
5. **Status-line shows paced rate when verbose** (`figaro -- ...
   --debug-pacer`). Useful while dialling in defaults.

**Tier 3 (polish):**

6. **Cursor flicker / blinking indicator while waiting for first
   token.** Today there's silence between prompt submission and
   first delta; a `▍` cursor that blinks until first byte would feel
   more responsive — same trick Claude Code uses.
7. **Per-content-type pacing.** Thinking blocks already get a special
   blockquote prefix; consider emitting them faster (or all-at-once)
   since they're meta-commentary, not the main reply.
8. **Per-terminal-width chunking.** When the terminal is narrow
   (≤80 cols), slow the rate slightly so the eye can keep up with
   wrapping.

**Don't bother with (yet):**

- Full Bubbletea rewrite. Largo + a pacer gets us 90% of the polish
  for 5% of the effort. Bubbletea would be required only if we wanted
  full-screen TUI elements (panes, sidebars, mouse).
- Provider-side smoothing. Anthropic SSE cadence is what it is;
  smoothing belongs on the consumer.

## 6. Estimated work

- Pacer + plumb-in: ~150 lines, half a day, low risk.
- Config + drain hooks at boundaries: ~30 lines, negligible.
- Tests: drive the pacer with a synthetic delta sequence, assert that
  output emerges at ≤ target rate and that drain on interrupt is
  bounded. ~100 lines.

The whole Tier-1 patch is the size of one focused commit. Tier 2 is
another. Tier 3 we can sit on until we feel the lack.

---

*Bottom line:* introduce a small ticker-driven pacer between
`deliverEvent` and `largo`. It's the same pattern every modern LLM UI
converges on (Vercel, llm-ui, the dev.to 240-FPS piece, Bubbletea
idioms) — applied to our pipe. It makes burstiness disappear without
hurting TTFT, and it's surgical enough to land in one PR.
