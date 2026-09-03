# figaro: generic tool IR + transcript pivot + perf/pagination (v0.2.0)

Shipped 2026-07-15. Branch `feat/generic-tool-ir`, developed in the isolated
worktree `/home/gluck/dev/figaro-qua/tool-ir` (treebear pattern, off `main`
`7059f00` = tag `v0.1.7`). 18 commits, fast-forwarded into `main`, tagged
`v0.2.0`. Full `go test -race ./...` green; validated end-to-end in a real tmux
pty via `nix develop .#share-hush`.

## Why this happened (the arc)

It started as a small critique. The `write` tool had a change (`b942a36`) that
faked "streaming" by replaying its already-complete content line-by-line at tool
**execution** time. Rubber-ducking that exposed two deeper truths:

1. The write `content` is not tool *output*, it's part of the tool *arguments*,
   which the model genuinely streams token-by-token over the provider API
   (`input_json_delta` → `PushToolInvokeDelta` → `evToolArgs`). The runtime was
   **receiving that stream and dropping it** (no `case evToolArgs` in the drain
   loop), then faking it later. Wrong phase, wrong source.
2. The renderer had per-tool `switch n.Name` logic on the **client** (bash→command,
   read/write→path, a `logEmitTools` collapse list). Principle established: the
   client must be a dumb generic renderer of a typed IR description, **zero
   per-tool control flow anywhere on the client.**

That unlocked a bigger rethink of the whole live-render path:
- The half-dynamic "scrollback flush" painter (a prior fix for a duplication bug
  on turns taller than the viewport) was fragile. Better: when a turn overflows,
  **move it to the alt-screen transcript pager** (which can scroll/select), and
  keep the inline painter simple. The transcript, in turn, should paginate its
  history lazily ("like Twitter") and support native mouse scroll.
- Performance: every streamed chunk did a full `composeTurn` + socket broadcast.
  Coalesce to ~11fps; bound the output accumulator.

## Orchestration: figaro building figaro

The isolated, independently-testable units were built by **7 figaro arias** in
parallel treebear worktrees, spawned fire-and-forget via `figaro new -j`, each
given a precise self-contained spec, each validated under `nix develop` and
cherry-picked onto the branch as it landed. The entangled integration (the wire,
the drain loop, the transcript, all the caught breakages) I did by hand. The
pattern worked extremely well for disjoint pure packages; the entangled core does
not parallelize (shared files) and stayed single-threaded.

Aria units: `partialjson` (tolerant JSON extractor), `toolout` (governor),
`mouse` (SGR parser), store `ReadBefore`, generic-render (typed Summary + client
rewrite), arg-preview (evToolArgs wiring), ReadBefore RPC.

## The 18 commits (oldest → newest)

1. `2b2bfde` **Revert b942a36**, remove the fake execution-time "line-by-line"
   write stream; the real generation-time stream lands later, generically.
2. `f62d8ee` docs: the refactor plan (governing principle + workstreams).
3. `1da1f9b` **`internal/partialjson`**, `StringField(data, name)`: extract a
   top-level string field from *truncated* JSON, monotonically (with a fuzz test).
   The hard part that made real arg-streaming possible.
4. `d929ee1` **`internal/toolout`**, `Governor`: bounded last-N-line tail per
   key, deterministic (no timers/locks; caller drives cadence).
5. `946e15d` **`internal/livelog/render/mouse`**, SGR 1006 parser + enable/disable
   constants + split-read handling.
6. `2c7d1b6` **Revert 5112389**, remove the half-dynamic scrollback-flush painter
   + log-emit collapse; the transcript pivot replaces it.
7. `46711b5` **store `ReadBefore(figaroLT, n)`**, bounded keyset "paginate back"
   on the `Log` interface + all 3 backends.
8. `9bd4d7f` **generic render**, `livedoc.Node.Summary`; polymorphic optional
   `Tool.Summarize(args)` (bash→command, read/write/edit→path) via
   `tool.Summarizer` threaded into compose as `ToolSummary`; **deleted**
   `toolArgSummary`'s `switch n.Name` and the dead `liveNodeIndex` scaffolding.
9. `be2e027` **native mouse scroll** in the transcript + **flush-only-last-turn**
   on pager close (not the whole history).
10. `42ccd35` **auto-enter transcript** when a turn overflows the viewport
    (`openOverflows`, `minPagerHeight` floor for tiny panes).
11. `54a39a6` test: add `ReadBefore` to a `memLog` mock, a build breakage the
    keyset aria (scoped to `internal/store`) couldn't see. Caught by the suite.
12. `90f8b8f` **arg-preview**, handle `evToolArgs`, accumulate partial JSON,
    optional `Tool.PreviewArg()` ("content" for write); a running tool's body
    argument streams live via `partialjson`. The correct replacement for b94.
13. `cad38aa` chore: drop a stray `temp.md` an aria left behind.
14. `4c8b0e7` **aria: carry `Summary` over the wire**, the generic renderer
    computed Summary server-side, but the aria wire (server `diff`/`fullSet` +
    client `setField`) didn't serialize it, so tool headers rendered blank.
    **Caught by the e2e tmux test**, the single most valuable catch.
15. `82807c5` **coalesce live emits (~11fps)**, `emitLive` throttles streaming
    emits (force on structural events + final flush) instead of per-chunk
    `composeTurn`; `toolout.Governor` replaces the unbounded `a.partials`
    accumulator (fixing a latent memory-growth on huge tool dumps).
16. `63b0f5e` **ReadBefore RPC**, expose the keyset read through aria.Server +
    `figaro.read` (Before/Limit) + `Client.ReadBefore`.
17. `9843242` **lazy windowed pagination**, the pager opens on the recent window
    (Ctrl-T = `ReadBefore(recent, N)`, not `Read(0)`) and pages older on
    scroll-near-top, folding into the client (which sorts+dedups by LT), with
    **viewport anchoring** (offset shifts down by rows added above) so what you're
    reading stays put; fetch runs off-lock.
18. `1da2077` chore: gofmt after the `Summary` field realigned struct tags.

## Thought processes / gotchas worth remembering

- **The e2e is what caught the real bug.** All packages were green and the
  deterministic pipeline test passed, yet summaries were blank in tmux. Cause:
  the wire didn't carry the new field. Unit tests can't see a serialization gap
  between two correct layers; the full-stack tmux run can. Lesson: run it for real.
- **Stale daemon.** The isolated `share-hush` daemon persists across `nix develop`
  invocations (stable runtime dir), so `figaro new` connects to the OLD binary.
  Always `figaro stop` first so a fresh daemon spawns. This burned a whole
  debugging cycle chasing a "fix that didn't work", the fix was fine; the daemon
  was stale.
- **Coalescing design.** Chose a wall-clock throttle (`emitLive`, force on
  structural events + a final flush) over restructuring both drain-loop selects
  with tickers, simpler, lower-risk, same result. The client animates the spinner
  on its own tick, so throttling agent emits doesn't hurt smoothness.
- **Pagination anchoring.** The subtle bug in bidirectional viewers is the
  scroll-jump when you prepend older items. Fix: measure rendered rows before and
  after folding in the older window, shift the offset down by the delta. The
  client already sorts+dedups by LT, so I just apply `ReadBefore` results into it
  rather than maintaining a separate buffer.
- **Genericity.** The summary/preview per-tool knowledge lives polymorphically on
  the tools (optional `Summarize`/`PreviewArg` interfaces), dispatched via a
  registry-backed function injected into compose, so compose and the client are
  both tool-agnostic. No central `switch` anywhere.

## Testing

Per-unit `go test` (each aria), full `go test -race ./...`, deterministic
transcript pagination + windowing tests, and an end-to-end tmux harness: a
16-row pane + a multi-tool prompt overflows → auto-pager; `figaro show` on the
fresh daemon confirms generic summaries (`✓ bash <cmd>`, `✓ write <path>`) and no
duplication. Prereq: `figaro stop` the isolated daemon first.

## Deferred / known-minor

- `figaro show -a` renders a tool's args as a redundant `command=…` line beneath
  the summary header (verbose cosmetic).
- The governor's bounded tail matches compose's 200-line cap; full tool output
  still reaches the model via the returned Content, and the transcript/`fig show`.

---

# Batch 2: incipit / transcript / listen rework (2026-07-15)

Branch `feat/incipit-transcript`, worktree `/home/gluck/dev/figaro-qua/incipit`, off
`main` (v0.2.0). 6 commits, `go test -race` green, tmux+nix-develop e2e verified.
**Not merged, on the branch for review.**

## Why
The transcript pivot from v0.2.0 shipped with bad mode semantics: it was meant to
be a temporary "peek," but auto-enter-on-overflow made it a mode you were trapped
in, `q`/`Esc` exit was ad-hoc, a full-width inverse status bar was loud, resize
jumped the view, and Ctrl-C/Ctrl-D didn't work in the pager. Reworked into a clean
three-mode model.

## The model
- **incipit**, the undisturbed inline render (renamed from "inline"; Latin *incipit*,
  "it begins"). A QoL nicety, not the main event. Exits on turn-done when undisturbed.
- **transcript**, the single, primary alt-screen pager. Entered on overflow (reaches
  terminal height), Ctrl-T, `--listen`, or `figaro listen`. **Locked**: `q`/`Esc`/
  `Ctrl-T` are inert; exit is Ctrl-D (disconnect) or Ctrl-C (interrupt-if-running,
  else close). "Peek" is gone.
- **listen**, a flag that just means "don't close on turn-done." Set by Ctrl-L (which
  also enters transcript), `--listen`/`-l`, or `figaro listen <id>` (which opens
  directly in transcript). `figaro send` closes on turn-done unless listening.

## Work items (all done)
1. rename inline→incipit (type `ldrender.Incipit`, files, comments).
2. delete peek: one transcript, dropped pendingSeals / Resume-flush.
3. transcript locked (q/Esc/Ctrl-T inert).  4. Ctrl-T in incipit → transcript.
5. auto-enter on fill (`openOverflows`).  6. Ctrl-L → listen+transcript.
7. `--listen`/`-l` (auto-enters + stays).  8. Ctrl-C/Ctrl-D as BYTES (MakeRaw, no
   ISIG), identical in incipit + transcript, and portable (Windows delivers
   Ctrl-C as 0x03, not a signal).  9. one `leaveTranscript` teardown on all exits.
10. subtle DIM status line (`↕ transcript · ^D exit … 48–61/61 live`), was a
    full inverse bar.  11. content-anchored resize (record top LT + line offset,
    restore after re-wrap, no jump).  12. dropped `\x1b[2J` on resize (diff-only,
    no flicker).  13. `y` copies the aria id via **OSC 52** (terminal-native;
    defers to the search box when searching).  14. platform **`term.Client`**
    boundary (MakeRaw / Size / OnResize / Read / SetClipboard) with a unix impl,
    a Windows/Windows-Terminal impl (SetConsoleMode + VT-input + ConPTY resize
    event + Ctrl-C-as-byte) drops in behind this one interface; comments mark the
    boundary. A shared `interactiveInput` loop backs both send and listen (was
    duplicated).

## Testing
Deterministic: transcript q/Esc-inert + lazy-paging + follow tests. tmux+nix e2e
(`tmux_incipit.sh`): a 16-row pane + overflowing prompt → auto-transcript with the
dim status; `q`/`Esc` stay locked; Ctrl-D exits to the shell. (`figaro stop` first
so the isolated daemon runs the fresh binary.)

## Orchestration
One aria (`term.Client` + OSC-52) in parallel; I did the interlocked mode core.
The rest was inherently sequential (all in stream.go/listen.go/transcript.go/
livelog_bridge.go), so no further parallelism was warranted.

## Note
The separate `feat/aria-retention-list` worktree (reaper + tree-aware list) is a
DIFFERENT uncommitted branch awaiting review, unrelated to this batch.
