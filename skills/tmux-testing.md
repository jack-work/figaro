---
name: tmux-testing
description: How to test figaro's terminal UI honestly — driving the real binary in a real pty with tmux, the traps that produced confident wrong answers, and the standing rules. Use when changing anything that paints (incipit, the transcript pager, the composer, footers, freeze/scrollback), when a test passes but a human reports a bug, or before believing any claim about what the screen shows.
---

# Testing figaro's terminal UI

> **Every test double that diverged from production diverged by being tidier
> than reality.**

That sentence was written after a single night in which **eight** green tests
certified broken code, and two bugs shipped anyway — a process that would not
exit, and a duplicated status bar — both found by a user in his own shell,
neither visible to the suite.

This skill is how not to repeat it.

## The rule that generates all the others

**A model of a terminal only knows the bugs its author imagined.**

Unit tests over `compose()` output tell you what figaro *decided* to paint. They
cannot see a row scrolling into history before the next repaint, a process that
never exits, a keystroke consumed as a motion, or a footer printed twice. If the
property you care about lives in the terminal, **test it in a terminal**.

## Run the smoke suite

```sh
FIGARO_TMUX_SMOKE=1 go test ./internal/cli/ -run TestSmoke -v
```

`internal/cli/tmuxsmoke_test.go` is the harness; `tmuxsmoke_cases_test.go` is the
suite. Skipped by default so `go test ./...` stays fast and hermetic. Each case
names the shipped bug it exists to catch — read those before adding one.

**It uses a real provider on purpose.** A fake provider would be one more double
free to drift. Costs tokens; a pass means something.

## Ten traps, each of which produced a confident wrong answer

**1. `tmux new-session -y N` gives pane height N−1.** The status bar takes a row,
and turning it off afterwards does **not** give the row back to a detached
session — nor does `resize-window`. Measured: `-y 30` → 29 either way; `-y 31` →
30. So **ask for `h+1`**, then *read back* `#{pane_height}` and report **that**.
An entire thread of "h=1 loses the reply" — three investigators, many trials —
was measured at pane height **zero**, a state no user can reach.

**2. Counting a token across the capture is unsound.** The footer mantra echoes
the prompt, the prompt usually contains your token, and an 80-column shell echo
wraps so its tail fragment matches too. Every trial inflates by two, which once
made a live bug look fixed. Only the rendered body line is sound:
`grep -cE '^[[:space:]]*<TOKEN>[[:space:]]*$'`.

**3. Gate every ABSENCE on pager chrome.** Grep for `? help`, `! status`, or an
`N–M/T` range. A long turn **auto-promotes** to the pager, where earlier content
sits above the tail window — so it isn't in your capture. *An absence inside a
pager is not an absence.* This produced two false reports and cost two agents
real time. Use a pane tall enough not to promote (`y=100` for tool-heavy turns)
and assert `pagerChrome == 0` **before** trusting any count.

**4. Capture scrollback, not the pane.** `capture-pane -p -S -`. Frames that
existed for milliseconds are preserved there verbatim. This is how the
submit-time footer stanza was photographed after fast polling failed to catch
it — the right instrument was a *short pane*, not a faster loop.

**5. Type one character per read.** `send-keys -l "whole string"` arrives as a
single read; a human types one byte at a time. Composer tests fed whole strings
and passed while a byte-vs-rune bug mojibaked every non-ASCII character. **If
the thing under test is input, type slowly.**

**6. Test the path of someone who does not know the affordance exists.** Three
investigators tested the composer by pressing its trigger key first, and all
three pronounced it sound. Nobody typed *without* it — which is exactly where
silent, partial, meaning-changing input loss lived. *Every expert test agreed
with every other expert test and all of them were incomplete.*

**7. Poll until stable; never sleep a fixed guess.** A model's first token can
take five seconds or fifty. `waitIdle` returns when the pane stops changing.

**8. A naive duplicate-line check misses real duplication.** The body-duplication
bug placed its two copies ~25 lines apart, separated by re-rendered thinking and
tool output. Compare *sequences*, not adjacency.

**9. Verify the model complied before blaming the renderer.** A wrapped
paragraph defeats a `line == token` regex. A model-set mantra containing your
token inflates counts. Both produced false positives.

**10. Clean up the daemon, not just the session.** `kill-server` leaves the
scratch daemon running. Seventeen agents each left one: **230 orphaned
processes**, 1.2 GB of tmpfs, and a memory-pressure alert with processes already
stalling. The harness's `close()` does both — if you drive tmux by hand, you
must too.

## Timing recipe for steering bugs

A steer must land **after a tool has completed** or there is nothing to
misorder. A steer fired before the first tool shows nothing wrong — and that
single difference produced two contradictory bug reports.

```
t=0   figaro new -- "run 4 readonly bash commands with sleep 8 between each, then say DONE"
t=14  fire the steer   <- the whole trick
t=72  capture-pane -p -S -
```

Assert: `pagerChrome == 0`; screen `✓ bash` count **equals**
`show --json | [.parts[].nodes[]|select(.type=="tool")] | length`; `↳ input == 1`;
and **the node ORDER on screen matches `fig show`** — incipit hoisting a steer
above the tools while `show` places it correctly is a live-vs-committed
divergence, which the purity invariant forbids.

## Before you believe a test

- Did it ever fail? **Canary it** — revert the fix and quote the failure. An
  assertion that has never failed is not evidence.
- Does the double call the real function? The paging double treated `limit` as a
  *count* while production used a *byte budget*, and never called `Paginate`.
  ~30 tests green while the pager rendered one node for an 800-node aria.
- Does the fixture still exercise its own path? One test had silently stopped
  scrolling and could no longer fail for its stated reason.
- Is it pinning current behaviour or intended behaviour? Two tests *asserted the
  bug* — one expected a tool present in the IR to render nowhere; a golden file
  literally encoded a duplicated voice marker.
