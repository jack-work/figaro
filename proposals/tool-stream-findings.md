# Streaming tool arguments — research findings and proposal

Commission of 2026-08-04. **Research + proposal only; nothing here is
implemented.** The owner's UI sketch is preserved beside this file as
[tool-stream-ui-sketch.txt](tool-stream-ui-sketch.txt) (it was on tmpfs).

Everything below that says *measured* was measured, in a real pty or on the
real wire, against a copy of the owner's own store (aria `cf3fc17d`, 477 tool
nodes) on `feat/tool-arg-stream` @ main (0.18.8, c7615f7). The copy, the
captures and the scratch daemon were deleted afterwards.

---

## 1. Where streaming stops — the seam, with line numbers

The deltas exist. They reach the agent. They die one call short of the wire.

| stage | file | what it does with a partial tool input |
|---|---|---|
| provider (SDK/messages) | `internal/provider/anthropicsdk/stream.go:105-118` | `InputJSONDelta` → `bus.PushToolInvokeDelta(id, partial)` ✔ |
| provider (hand-rolled) | `internal/provider/anthropic/anthropic.go:1360-1372` | same ✔ |
| provider (copilot responses) | `internal/provider/copilot/responses.go:1041-1046` | same ✔ |
| provider (openai chat) | `internal/provider/openaichat/stream.go:133` | same ✔ |
| bus | `internal/figaro/turn.go:105` | `evToolArgs` ✔ |
| **agent drain** | **`internal/figaro/turn.go:511-512`** | **`a.argPartials[ev.id] += ev.partial` — accumulated, `force=false`** |
| projection | `internal/compose/compose.go:136-193` | `toolNode`: `Args` comes from `inv.Arguments` (whole, decoded); the partial is used **only** through the `PreviewArg` keyhole, and is written into **`n.Output`** |
| UI IR | `internal/livedoc/node.go:94-101` | `Args map[string]any` — no field can hold a partial input |
| wire diff | `internal/livelog/aria/server.go:335-348` | `summary`/`args` are `scalar`/`set`; only `markdown` and `output` are `streamed` (spliced) |
| client fold | `internal/livelog/aria/client.go:629-635` | `patch` understands `markdown` and `output`, nothing else |
| renderer | `internal/cli/nodes.go:548-616` | header = glyph + name + `Summary` (cap **80 columns**) + `[dur]`; args only under global verbose |

**One-sentence answer:** the deltas are already plumbed to
`Agent.argPartials`; what is missing is a *representation* — the UI IR has no
"tool input so far" field, so `compose` has nothing to put a partial into
except `Output`, and it only does that for one tool.

### Measured on the wire (`figaro send -v`, timestamps mine)

One `bash` call, whole turn:

```
 1.48  node0 set{name:bash, status:running, tool_call_id, type:tool}   ← no args, no summary
 1.66  node0 set{args:{…}, summary:"sleep 3; echo …"}                  ← ONE set, everything at once
 1.66  node0 set{started_at}
 4.66  node0 set{output:"…"}
 4.67  node0 set{finished_at}, set{status:ok}
 …     node1 patch{markdown:(1,0,"Ecco* — …")}                         ← prose DOES splice
```

Tool arguments arrive in exactly one `set`. Prose, in the same capture, arrives
as `patch` splices. That contrast is the whole bug in four lines.

### How long the blind window actually is

A `write` of 4 KB, same aria, same route:

```
 2.84  node0 set{name:write, status:running}       ← spinner, name, nothing else
       …  25.4 SECONDS OF NOTHING  …
28.22  node0 set{output:"1. Opera began in Flor…"} ← the PreviewArg keyhole, 3870 B at once
28.30  node0 set{args:{content,path}, summary}
28.30  node0 set{started_at}                       ← the timer starts HERE, at execution
```

OTel from the same call (`traces.jsonl`): `provider.tool_use.block_start`
12:46:50.634 → `first_input_delta` 12:46:51.146 (**+0.5 s, 9 bytes**) →
`block_stop` 12:47:16.094 (`input_bytes: 4201`). So figaro was receiving that
input for 25 seconds and showed none of it.

**A caveat I could not fully settle, stated as such.** `FIGARO_NODE_DEBUG`
recorded only **7 compose frames** during those 25 s. The emit throttle is 90 ms
(`stream_emit_interval_ms`, default, unset in the owner's config), so 7 frames
means figaro was woken ~7 times, not ~275 — i.e. on this route (copilot →
`api.enterprise.githubcopilot.com/v1/messages`, claude-opus-5) the
`input_json_delta` chunks themselves are coarse and back-loaded. Prose on the
same model streams smoothly (21 splices, median gap 468 ms, measured), so this
is specific to tool input. **What this changes:** streaming the argument will
not always paint character-by-character; on this route it may paint in a few
jumps. What it does not change: today it paints *never*, and for `bash` there
is no path at all. Settling it exactly needs a 3-line instrumented build that
counts `PushToolInvokeDelta` calls with timestamps — worth doing before tuning
any cadence, not before deciding the design.

---

## 2. The five problems, sorted by whether they are independent

| # | problem | independent? | where it is fixed |
|---|---|---|---|
| 1 | arguments do not stream | **needs the new field** | `livedoc.Node` + `server.delta` + `client.foldDelta` + `compose.toolNode` |
| 2 | no progress signal | rides on #1 (bytes so far) + a generation-phase timer | `compose`/`uiir` timings |
| 3 | unreadable expanded args | **fully independent** — pure renderer | `internal/cli/nodes.go` |
| 4 | duration pushed off the edge | **fully independent** — pure renderer | `internal/cli/nodes.go` |
| 5 | bash/write special-casing | resolved *by* #1 | `internal/tool/write.go`, `compose.toolNode` |

3 and 4 are one afternoon and touch nothing but the row builder. 1, 2 and 5 are
one change: give a partial input a name.

### #4, as an integer with a condition (measured)

The header is built as
`glyph(1) + " " + name + " " + truncCols(Summary, 80) + " " + "[dur]"`, and the
incipit hard-**clips** each row to the pane width (`clip`, `incipit.go:114/216/315`
— the renderer's stated contract is "each string is one physical line ≤ width").
So the duration disappears exactly when

```
2 + len(name) + 1 + min(80, summaryCols) + 1 + len(durText) > width
```

For `bash` at width 80 that is any command whose first line is ≥ 66 columns.
Over the owner's 477 tool nodes: **312 (65%) lose the duration at width 80**,
283 (59%) have the summary itself clipped. At ≥ 95 columns neither happens,
because `toolSummaryCap` already bounds the summary at 80. Summary first-line
width: median 98, p90 289, max 743 columns.

Two further facts the complaint did not name:

- **History has no duration at all.** `StartedAt`/`FinishedAt` live in the
  Projector's in-memory `timings` map (`internal/uiir/uiir.go:56-73`); the
  historical path (`compose.Turns`) passes no timings. Measured: **0 of 477**
  nodes carry `started_at` in `figaro show -j`, and 0 of 476 rendered header
  rows in a real pane showed a duration. So "always visible" also means
  *persist the timings*, not just *reserve the columns*.
- **The timer starts at execution, not at invocation.** `started_at` is stamped
  on `toolBegin` (`turn.go:1467`), so during the 25 s of argument generation
  there is nothing to show even in principle. #2 needs its own clock.

### #3, as a capture (real pane, 80 cols, Ctrl-O)

```
✓ bash cd /home/gluck && timeout 200 figaro send --id 694ffa77 -f -- "cf3fc17d.
  command=cd /home/gluck && timeout 200 figaro send --id 694ffa77 -f -- "cf3fc17
  d. Correction, and it is about me.

  I have been writing you essays while telling you to delete more than you
  …
  Ignore my word count; keep my rules." 2>&1 | tail -1
  timeout=240
  │ forgot 694ffa77 — use `figaro listen 694ffa77` to follow
```

`timeout=240` is typographically identical to a continuation line of
`command=`. One dim style for keys, values and wrapped remainders; no rule, no
blank line, no indent step. The owner's fix — label on its own line, value
beneath, labels styled apart — is right, and is the whole of #3.

### A defect nobody reported (found while measuring #4)

`renderToolNode` returns **one string containing embedded newlines** when the
summary is multi-line, because `runewidth.Truncate` counts `\n` as zero width:

```
width=80 collapsed: 1 row(s)
  [0] len=82 newlines=3 "✓ bash cd /home/gluck/notes && python3 - <<'PY'\nimport pathlib\nprint(1)\nPY [1m01s]"
```

One "row" that paints as four physical lines, past an 80-column clip that
thought it was 82 runes wide. That breaks the renderer's row contract and the
live region's height model. **126 of 477 (26%)** of the owner's tool summaries
contain a newline. This is a candidate for the very first commit: it is a
one-line fix (fold the summary to its first line, or replace newlines with `⏎`)
with a unit test that fails today.

---

## 3. What of the bash/write special-casing is real

The owner remembered special-casing added on the assumption that input would
stream. It exists, it is **not dead**, and it is exactly what a real streaming
input would replace:

- `WriteTool.PreviewArg() = "content"` (`internal/tool/write.go:46`) is the only
  implementation of the interface. `compose.toolNode` consumes it twice:
  1. **while running** — extract the partial `content` from `argPartials` via
     `internal/partialjson` and put it in **`n.Output`** (`compose.go:185-192`);
  2. **after it finishes** — *replace* the tool's real result ("Wrote N bytes to
     …") with the written body (`compose.go:167-176`).
  Load-bearing today: (1) is the only thing that streams during generation, and
  (2) is the only reason a `write` shows anything but a one-line receipt.
  Both are the *wrong channel* — file content is input, not output — and both
  become deletable the moment a partial input has a field of its own. The
  owner's instinct ("write's output can just be the summary now") is exactly
  the right sequencing: it is a **replacement**, not a patch.
- `partialjson` (a whole package, with tests) exists solely to serve that
  keyhole. Under the new design it stays, promoted: it is how a
  still-truncated argument becomes a displayable string.
- `composeBashCap = 200` (`compose.go:45`) and `nodeBashCapDefault = 10`
  (`nodes.go:20`) are named for bash but are generic output clamps applied to
  every tool. Keep.
- `Summarize()` on bash/write/edit/read (`internal/tool/*.go`) is per-tool but
  is the *sanctioned* seam — the renderer has "ZERO per-tool control flow" and
  reads only `n.Summary`. Keep; it is what fills the owner's new command line.

Nothing here is scaffolding around an unbuilt bridge; it is a keyhole cut
because the bridge was not built. Removing it *without* building the bridge
would lose the one streaming preview figaro has.

---

## 4. Proposed design

Against the sketch, with the two deviations named.

### 4.1 The wire: one new streamed field

```go
// livedoc.Node
Input      string `json:"input,omitempty"`       // the tool input as it arrives (raw JSON prefix)
InputBytes int    `json:"input_bytes,omitempty"` // how much has arrived (progress)
```

- `server.delta`: `streamed("input", …)` beside `markdown`/`output`; client
  `foldDelta` learns one more case. Both are one line each — the splice
  machinery, versioning and resync are already correct and already exercised by
  prose.
- `compose.toolNode` sets `Input` from `argPartials` for **every** tool, with no
  name lookup, and keeps setting `Args` (whole, decoded) at ready. `Input` is
  then the generation-phase truth and `Args` the post-decode truth; the renderer
  prefers `Args` when present, so the display does not flicker between two
  spellings of the same thing.
- **Deviation 1 (raw JSON, not per-field):** stream the raw prefix, not a
  decoded map. A partial JSON object cannot be decoded, and `partialjson` can
  already extract *one named string field* monotonically — that is what the
  renderer will call for the headline argument (`command`, `path`, `content`),
  chosen by a **generic** rule (the tool's `Summarize` seam, extended to name
  its headline arg) rather than by tool name in the renderer.

### 4.2 The header: name and duration only

```
✓ bash [1m1s]
  cd /home/gluck/dev/figaro-qua/main && git push origin main 2 && …
  │ To https://github.com/jack-work/figaro.git
```

Exactly the sketch. The duration then cannot be pushed anywhere: it is the
second token of a row whose length is `2 + len(name) + 1 + len(dur)` ≈ 15
columns, and the invariant is testable ("the header row is ≤ width and always
ends with the duration"). The command moves to its own line, ellipsised with `…`
at `width - indent`, and gets the newline fold it needs anyway.

- **While generating**, the same shape carries the progress signal:
  `⠋ write [12s · 3.1 KB]` with the command line filling in beneath as it
  arrives. That is #2, and it costs one new timing (invocation time, stamped at
  `evToolStart`) plus `InputBytes`.
- **Deviation 2 (persist the timings):** the sketch says the duration must
  always be visible; today it does not exist off the live path at all. I would
  carry `started_at`/`finished_at` into the IR (a tool_result's message
  timestamp already exists; the invocation's does not) so `figaro show` and the
  pager over history show durations too. This is the largest piece of the
  proposal and the one most worth arguing about — it touches the fig IR, not
  just the UI IR. If it is rejected, #4 still gets fixed by the layout, and
  history simply keeps showing no duration.

### 4.3 The expanded view: labels above values

```
✓ bash [5ms]
  uname -a; echo ---; uptime
  ─ command
    uname -a; echo ---; uptime
  ─ started    2026-08-04 12:30:30.358 EDT
  ─ finished   2026-08-04 12:30:30.363 EDT
  │ Linux gluck 7.1.4-1-cachyos …
```

Labels dim + a leading marker, values undimmed and indented one step further,
so a wrapped value can never be read as a new key (the exact failure in §2).
Long values wrap at `width - indent`; `bash`'s value is a candidate for syntax
highlighting, which I would put **last** (it needs a highlighter dependency and
a colour budget; the readability win is already banked by the layout).

### 4.4 Expansion is per-node, and includes the arguments

Today `Enter` in the pager expands a node (`toggleSelectedNodes` →
`fullOutput`) but the argument block is gated on the **global** `verbose`
(Ctrl-O), because `renderNode` passes `verbose` — not `expanded` — into
`renderToolNode` (`nodes.go:74-77`). The owner asks for one gesture that
expands both. That is a one-line change plus a decision about what Ctrl-O then
means (proposal: Ctrl-O stays the "expand everything" master switch; Enter is
the per-node one; both feed the same parameter).

---

## 5. Staged plan — smallest useful thing first

Each stage is independently shippable and independently arguable.

1. **The newline row bug** (§2, last). One-line fix, unit test that fails today.
   No design decisions. Fixes a real paint hazard nobody had noticed.
2. **The header layout** — name + duration on the header row, command on its own
   line, ellipsised. Pure renderer; kills #4 outright and half of #3. Unit test:
   "the duration is present at every width from 20 to 200."
3. **The expanded view** — labels above values, styled apart; `Enter` expands
   arguments as well as output. Pure renderer; finishes #3.
4. **`Input` on the wire** — the new streamed field, end to end, with the
   generation-phase timer and byte count. Kills #1 and #2. This is where the
   tests get interesting: a fake provider that emits a known chunk sequence, and
   an assertion that the rendered command grows monotonically.
5. **Retire the keyhole** — delete `PreviewArg`, both branches in
   `compose.toolNode`, and let `write` show its real receipt ("Wrote N bytes")
   with the body visible as the streamed *input* instead. Only safe after 4.
6. *(optional, argue first)* persist tool timings into the IR so history shows
   durations; *(optional, last)* bash syntax highlighting.

Stages 1–3 need no wire change and could land while 4 is still being argued
about.

---

## 6. Instruments used, and one that lied

- `figaro send -v` (verbatim wire) with shell timestamps — the decisive
  instrument for "does it stream". Reproduce: `figaro send --id <a> -v -- "…" |
  while IFS= read -r l; do printf '%s %s\n' "$(date +%s.%N)" "$l"; done`.
- `FIGARO_NODE_DEBUG=<dir>` — one line per compose frame; counts emits.
- `traces.jsonl` in the isolated state dir — `provider.tool_use.block_start`,
  `first_input_delta`, `block_stop` gave the provider-side timing that the
  aria wire cannot see.
- A throwaway `internal/compose` test to prove the preview keyhole works on a
  growing prefix (it does), and one in `internal/cli` to photograph the header
  row (it has newlines in it).
- tmux on a private socket, `figaro listen` on an imported real aria (zero
  tokens) for the pager captures.
- **The instrument that lied:** `scripts/import-arias.sh` (branch
  `feat/dev-shell-aria-import`, not on main) fails against the current store —
  it looks for `.trunk` markers, and the live store now writes `.node`. It
  reports `no aria matching '<id>'`, which reads like "no such aria". I used the
  `.#snapshot` preset instead (with `FIGARO_STATE_DIR` overridden onto
  `/var/tmp`, since the dev root is tmpfs). Worth telling `ce1b6b07`.

---

## Deferred, awaiting the owner's review

Raised at the end of a handoff and never properly reviewed, so they are written
down here rather than left in a channel. Neither is started.

**1. The header layout (problem #4 of the original commission).** The duration
is still appended after an 80-column summary and is still clipped away at
narrow widths: measured over aria `cf3fc17d`, **312 of 477 tool headers (65%)
lose it at width 80**. The condition, exactly:

```
2 + len(name) + 1 + min(80, summaryCols) + 1 + len(durText) > width
```

The owner's sketch fixes it by construction — `✓ bash [1m1s]` with the command
on its own line — which makes the header a fixed ~15 columns and the duration
unloseable. Partly overtaken by events: the argument block now carries the
command on its own line already, so what remains is (a) dropping the summary
from the header when the argument block is drawn, and (b) an invariant test
that the duration is present at every width from 20 to 200.

**2. Bash syntax highlighting inside the argument block.** Wanted in the
sketch, deliberately last: it needs a highlighter dependency and a colour
budget, and the readability win was already banked by the label/value layout.

**3. Persisting tool timings into the fig IR.** The larger of the three, and
the one that touches the IR rather than the UI. `StartedAt`/`FinishedAt` live
only in the Projector's in-memory map, so **0 of 477** historical tool nodes
carry a duration at all — `figaro show` and the pager over history have none to
draw. Fixing it means giving the invocation a timestamp in the IR, which is a
storage change, not a rendering one. Declining it is a perfectly good answer;
the layout fix stands either way.

**4. The duration counts EXECUTION only, so a 30-second write reads `[0ms]`.**
Seen live: `✓ write [0ms]` after half a minute of the file streaming in.
`started_at` is stamped at `toolBegin` (`turn.go:1467`), when the tool starts
running — but for a large write nearly all the wall time is the model *writing
the arguments*, which is over by then. Stamping it at `evToolStart` (the block
opening) would make the number "how long this call has taken", generation
included, which is what a reader watching a spinner is asking. It also
conflates two phases in one number, which is why it is a decision rather than a
fix. Two lines either way.

**5. `figaro replay` re-wraps a tape recorded at a different width.** A tape
carries the geometry it was recorded at; replaying into a wider pane re-wraps
the recorded rows and can drop a character at a wrap seam. The renderer itself
is lossless (proved in-process at 104/100/80, decorated rows included) and live
runs at the pane's own width are clean. Pre-existing, not caused by the
streaming work, and not fixed.
