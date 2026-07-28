# PROPOSAL — CHERUBINO: errors bleeding into the status bar

Branch `fix/status-bleed`, forked from `paint/base` at 6280ddc.
Author: CHERUBINO (aria `2cbde7f0`). Parent: ALMAVIVA (`77bd3322`).

**Status: BLOCKED on a product decision. No fix is committed.** The bug is
photographed, the root cause is named, and a canaried regression test is in
place and currently failing on purpose. The three candidate fixes are visibly
different behaviours, so the choice belongs to the user, not to me.

---

## 1. The bug

The user's words: *"Errors where the text bleeds into the status bar."*

Confirmed. While the transcript pager owns the terminal, several code paths write
straight to `os.Stderr`. That text lands **on the pager's own grid, on top of the
footer**, and it silently desynchronises the painter from the terminal.

Captured grid, pane 100×24, rows 21–24 (escapes shown):

```
21  \e[2m──────────── aria 8566c903 · 883–903/903+ live ───\e[0m     <- footer rule
22  \e[2mfigaro work · thinking ⠼ · … · ? help · ! status\e[0m       <- footer status
23  error: anthropicsdk 401: token unchanged after invalidate        <- THE BLEED (no SGR at all)
24  \e[2mfigaro work · error ✗ · … · ? help · ! status\e[0m          <- a SECOND status row
```

Two independent proofs that row 23 did not come from the renderer:

- **It carries no escape sequences whatsoever.** Every row `footerRows` emits is
  wrapped in `\x1b[2m … \x1b[0m`. An unstyled row among styled ones was written
  straight to the terminal.
- **The grid scrolled by two rows without the painter knowing.** The footer is
  painted at `screen[t.h-2]`/`screen[t.h-1]` — rows 23 and 24 of a 24-row pane —
  yet it is sitting at 21/22. `fmt.Fprintln(os.Stderr, "\n"+d.Reason)` writes a
  leading *and* a trailing newline, and on the bottom row each one scrolls the
  whole alt grid up. `t.prev` still describes the old positions, so every later
  frame diffs against a lie. Hence the **duplicated status row** at 24.

A second, more vivid capture has the error text half-overwriting the *rule* row
and stranding its tail: `error: anthropicsdk 401: token unchanged after invalidate        6–906/906+ live ───`.

Captures: `/tmp/paint-cherubino/shot/` (`shot.txt`, `shot.esc`, `t358b-1..14.*`).

---

## 2. Which of the brief's two bugs this is

The brief required distinguishing **(a)** a body row reaching the screen without
`clipToWidth` / smuggling a newline, from **(b)** something writing outside the
frame buffer.

**It is (b), decisively.** Evidence for (b) is above. Evidence *against* (a):
across 13 frames at widths 100, 72, 120 and 64 (mine plus ALMAVIVA's sweep) the
widest row equalled the viewport width **exactly** and never exceeded it.
Invariant #1 is holding. I found no support for (a) at all and am not proposing
any change on that account.

---

## 3. Root cause, in one paragraph

While the transcript pager is active it owns the **alternate screen**, which has
**no scrollback** (measured: `alternate_on=1`, `history_size=0`; `capture-pane -p
-S -` returns exactly the visible rows). `livelogTurn.finishTurn`
(`internal/cli/livelog_bridge.go:561`) returns **early** when `t.tr.active` — it
renders and returns without leaving the pager. The error path in
`internal/cli/stream.go:161-170` calls `lt.finishTurn(d.Reason)` and then writes
the hint with `fmt.Fprint(os.Stderr, …)`, on the belief — stated in its own
comment — that the live region has been torn down and the text will "land on
clean scrollback below it, not over the footer". That belief is **true for the
inline view and false for the pager**: nothing was torn down, there is no
scrollback, and the write lands on the grid at the cursor, which the painter
parks on the status row (`screen[t.h-1]`) at the end of every frame. The leading
`"\n"` then scrolls the grid under the painter, so `t.prev` stops describing the
terminal and the corruption persists into subsequent frames.

**Same root cause, second site:** `internal/cli/stream.go:358` writes
`"\ninterrupting..."` *before* any `abandon`/`leave`, so a plain **Ctrl-C mid-turn
reproduces the bleed with no error involved at all**.

---

## 4. Site-by-site, verified rather than assumed

ALMAVIVA handed me a ranked list and told me to verify each. I did:

| site | text | verdict | how |
|---|---|---|---|
| `stream.go:169` | `"\n"+d.Reason` | **BLEEDS** | real pty capture, twice |
| `stream.go:358` | `"\ninterrupting..."` | **BLEEDS** | real pty capture; pager up (`alt=1`), 2 status rows for the whole ~2 s window |
| `stream.go:167` | `"\n"+providerSetupHint()` | **presumed, NOT captured** | same `if isErr` block, one branch away from :169; identical mechanism. Honest gap. |
| `stream.go:368` | `"interrupted"` | **probably safe in practice** | observed only after `alt=0`. No leading `\n`. Racy; not captured bleeding. |
| `stream.go:346` | `"follow: figaro listen …"` | **SAFE — negative control** | `abandon()` → `leaveTranscript()` first (`livelog_bridge.go:495`). Pager up chrome=1; Ctrl-D → `alt=0`, clean grid. |

The negative control matters: it shows the instrument reports CLEAN when the code
is correct, so its CONTAMINATED verdicts mean something.

---

## 5. Repro (zero tokens, ~4 seconds)

The cheapest provocation is a deliberately invalid API key: the turn fails with a
401 **before a single token is generated**. This is better than a bogus model or a
blanked credential — it corrupts no config, and it lands on the `"\n"+d.Reason`
branch rather than `providerSetupHint`.

```sh
source scripts/paintpane.sh
pp_init cherubino                 # NOT pp_seed — see below; this bug needs no history
# pane of exactly 24 usable rows, aria identity scrubbed
# then, inside it, with ANTHROPIC_API_KEY=sk-ant-api03-deliberately-invalid:
#   figaro send -l -- 'say OK'        (-l opens the pager AT STARTUP)
```

**This repro needs no seeded store and no credentials at all.** It opens a fresh
aria and the turn dies on a 401 from the environment key. My original captures
used `--id 8566c903` against a seeded store, but that was incidental — the
regression test uses a fresh aria and passes/fails identically. That matters
because `pp_seed` has since been **disarmed** for leaking the master's real
credentials and conversation history into reboot-surviving `/var/tmp`; I removed
my copies (`config/providers`, `config/hush`, and the 124 MB `state/arias`) and
verified nothing group- or world-readable survived. Nothing in this bug's repro
regressed as a result.

Or just run the regression test, which does all of the above:

```sh
FIGARO_TMUX_SMOKE=1 go test ./internal/cli/ -run TestSmoke_ErrorDoesNotBleedIntoStatusBar -v
```

The Ctrl-C variant (`:358`) needs a turn that is genuinely running, which costs
one small real turn (measured: **242 tokens**): prompt a single `sleep 40` bash
call, wait ~14 s, press Ctrl-C, and capture **within ~3 s** — the pager exits
after the interrupt round-trip and, there being no scrollback, the evidence is
destroyed with it. Rapid successive captures at 0.2 s are required.

---

## 6. The regression test, and its canary

`TestSmoke_ErrorDoesNotBleedIntoStatusBar` in
`internal/cli/tmuxsmoke_cases_test.go`, plus two harness helpers in
`tmuxsmoke_test.go` (`statusRows`, `pane.rawVisible`).

It is **fix-shape-agnostic on purpose**: it asserts the invariant, not an
implementation. If the pager is not on the grid it returns without complaint
(that is fix (i)); if the pager *is* on the grid, then no row may carry text with
no SGR at all, and the status row may not appear twice.

**Canaried in both directions** — an assertion that has never failed is not
evidence, and one that never passes is not a test:

| arm | `internal/cli/stream.go` | result |
|---|---|---|
| shipped code | unmodified | **FAIL** in 3.69 s |
| probe fix (i) | `lt.leaveTranscript()` before each write | **PASS** in 3.68 s |

Quoted failure on the shipped code:

```
--- FAIL: TestSmoke_ErrorDoesNotBleedIntoStatusBar (3.69s)
    tmuxsmoke_cases_test.go:256: error text reached the grid with NO escape
    sequences at all, so it bypassed the frame buffer entirely:
    "error: anthropicsdk 401: token unchanged after invalidate"
```

An earlier draft of the test **skipped** under fix (i) instead of passing, so it
could not tell "fixed" from "the pager never came up". I rewrote it; a skip that
looks like a pass is how a test stops being evidence.

One honesty note recorded in the test itself: the duplicated-status-row check is
**timing-dependent** (it needs a repaint after the stray write) and shows up most
runs but not all, so it is asserted one-sided and is explicitly *not* the
load-bearing assertion. The escape-sequence check is a property of the bytes and
does not race.

`go build ./... && go vet ./... && go test ./...` are **green**: the smoke suite
is skipped unless `FIGARO_TMUX_SMOKE=1`, so the failing case does not redden the
default build. That is also the risk — see §8.

---

## 7. THE DECISION FOR THE USER (this is the BLOCKED question)

All three options remove the bleed. They differ in what the user *sees* when a
turn errors while he is reading the pager, and that is a taste question about his
own tool.

**Option (i) — leave the pager, then print.** Add `lt.leaveTranscript()` before
each write (2 lines).
*Probed and working; test passes; visually clean.* The error lands on the normal
screen under the flushed transcript tail, exactly as the existing comment always
intended:
```
──────────
> input
  say OK
──────────

error: anthropicsdk 401: token unchanged after invalidate
```
*Cost:* the pager is **yanked away** — an explicit view change the user did not
ask for. `leaveTranscript()` also calls `flushTail()`, dumping the tail to
scrollback (bounded to the last turn when entered cold). For Ctrl-C this is
arguably right, since Ctrl-C ends the session anyway. For an error mid-reading it
is the most disruptive of the three.

**Option (ii) — route the hint through the frame buffer** as a styled status/error
row, so the pager stays and shows the error in its own chrome.
*Least disruptive, most in keeping with the painter's invariants.* Costs real
work: an error channel into `transcript`/`statusLine`, plus a decision about how
a multi-line `providerSetupHint()` (several lines of setup instructions) is shown
in a one-row footer — truncate, or open the `!` panel.

**Option (iii) — suppress while the pager is up** and surface the error in the
`!` status panel.
*Cheapest correct-by-construction option.* Risk: an error the user never
explicitly opens a panel to read is an error he may never see — and "silence is
the failure mode we are here to kill" is the stated reason these writes exist at
all (`angelus_client.go:193`).

My reading, offered as a recommendation and not acted on: **(ii) for the error
paths, (i) for Ctrl-C.** Ctrl-C is already an exit gesture so leaving the pager
costs nothing there, while an error arriving mid-read is exactly when the user
wants to keep his place. But this is precisely the "trade-off between two
defensible renderings" the brief says to hand up, so I have not implemented
either.

---

## 8. Risk, and what was NOT done

**Risk of what I did commit** (a test and two test helpers, no production code):

- The smoke suite is now **red when enabled** (`FIGARO_TMUX_SMOKE=1`) until the
  bug is fixed. Deliberate, and flagged in a comment at the top of the case. If
  the user would rather the suite stay green, the case should be `t.Skip`ped with
  a reference to this document rather than deleted — but a skipped test is not
  evidence, which is why I did not do it pre-emptively.
- The test consumes no tokens but does start a scratch daemon and a tmux server;
  it uses the existing `newPane`/`smokeStore`/`close` machinery, so teardown
  (both halves — server *and* daemon) is already handled.

**Not done, and deliberately:**

- **No fix.** §7 is why.
- **`stream.go:167` not captured.** Same block, one branch from a site I captured
  twice, so I am confident by inspection — but I did not photograph it and I am
  not claiming I did.
- **No audit of `die()` or provider-layer writes** beyond the `internal/cli`
  grep. `angelus_client.go:199/208` are startup-only (before any pager) and
  `aria.go`'s prints are `show`/`state` code paths, not the live path. Other
  packages were not swept.
- **No deterministic (non-tmux) test.** The bug is "a write bypasses the frame
  buffer", which is a process-level property of stdout; an in-process test over
  `compose()` cannot see it. The tmux-testing skill's rule applies: if the
  property lives in the terminal, test it in a terminal. A cheaper unit test
  would be possible *after* option (ii) is chosen, since routing through the
  frame buffer makes it observable in the frame.
- **The Ctrl-C window is a race.** My `:358` capture is real but had to be caught
  with 0.2 s polling inside a ~3 s window; the test does not cover `:358` for
  that reason. If the user picks a fix, a `:358` case should be added with the
  rapid-capture pattern.

**Adjacent finding, handed to BASILIO/BARTOLO and worth the user's attention:**
this bug also produces a **duplicated status row**, because the stray write
scrolls the grid under the painter and `t.prev` stops describing the terminal. A
duplicated footer is on the list of bugs that have already shipped from a
*different* cause. If anyone is chasing footer duplication from the painter side,
this is a second, independent source of it.

---

*Voi che sapete che cosa è amor — but the choice of rendering is not mine to make.*
