# Submit jitter: the queue row nobody could read

Merged into `deltas-and-forks`. The numbers below are the RERUN, on the
repaired gateway fixture; the first set published here was measured against a
fixture that answered before it had read the request, and is withdrawn.

## The seam

`interactiveInput.onIntrinsicChanged(intrinsicQueue)` in
`internal/cli/intrinsic_mirror.go`. A queue delta lands on the notify
goroutine, `readQueue` projects the mirror into rows, every row whose state is
`queued` or `committing` becomes a `queuedItem`, and
`livelogTurn.setTranscriptQueued` (in `livelog_bridge.go`) drops them into the
drawer and repaints. `setQueued` also auto-opens the pit when the list is
non-empty and auto-closes it when it drains, so one row costs a panel opening,
the transcript shrinking under it, and focus moving to the pit.

Against an IDLE aria, the row exists for the time the daemon takes to lift it:
Send publishes `queued`, the drain loop lifts it to `committing`, and the turn
appends it and marks it `committed`. That is one round trip through the inbox.
Measured here: 8 to 66 ms, which is one to four frames of a drawer opening and
closing again before the turn starts.

Against a BUSY aria the same row sits there for the length of the running turn,
seconds, and is the only answer to "where did my message go".

## The change

`queueDebut` gives a row that first appears while the runtime mirror is NOT
`thinking`/`tooling` a 150 ms grace, records the deadline per id, and draws it
only if it is still live when the deadline arrives (`time.AfterFunc` re-runs
the projection). A row that arrives while a turn is running is drawn at once.
A row that is lifted inside the grace is never drawn at all, and the wake-up
finds nothing and spends no frame.

`RuntimeState.Busy()` is new in `api/rpc/misc.go`: `accepted` and `committing`
are the interstitials of the very flicker being suppressed, so only `thinking`
and `tooling` count as work.

## The bench

`scripts/jitterbench.sh` drives the real binary in a pty on a private tmux
server, against `scripts/jitter-gateway.py`, which streams slowly enough to
have a thinking window. Isolated XDG dirs, own daemon, killed at exit. Nine
submits into an idle aria through the pager's `:send`, then one long turn and
three submits into it while it works. `scripts/jitterreport.py` reads the
marks; `pit.tsv` samples the screen every 14 ms beside them, so a fix that only
moved marks around could not pass.

One mark was added, `queue.draw{rows}`, written where the drawer's contents
change. It is in the instrumentation commit, and both sides of the comparison
carry it.

### The fixture defect, and the guards it bought

**The first table published here was withdrawn.** The gateway fixtures read
`Content-Length` only, and figaro streams chunked request bodies: every
request was recorded empty and the fixture answered while the client was still
writing, so turns died on a broken pipe and the scripts reported timings over
them anyway. The reviewer's fix (`scripts/http_fixture.py`, a shared
`request_body` that reads chunked framing) is upstream of this branch, and
everything below was measured after it.

A fixture that can lie once can lie again, so the run now refuses to report
unless it can prove two things:

- **Every prompt arrived exactly once, and every turn was answered.** Not one
  turn per send: that assertion failed on the first honest run, and it was the
  assertion that was wrong. Prompts sent into an aria that is already working
  are COALESCED into the next turn with whatever else is waiting, so the three
  busy sends are correctly one turn carrying three questions.
- **No request reached the gateway with an empty body.** That is the defect
  itself, asserted where it happened rather than inferred from a timing.

Either check failing exits non-zero before a number is printed.

## The numbers

Two runs each, same script, same machine, on the repaired fixture. Every run
validated: 12 prompts over 10 answered turns, 10 gateway requests all carrying
messages.

Frame jitter while thinking or tooling, as marks.md defines it:

| run | p50 | p95 | max | fps | quiet |
|---|---|---|---|---|---|
| before | 49.3 ms | 90.2 ms | 2622.8 ms | 21.4 | 12 |
| before2 | 48.1 ms | 90.1 ms | 2640.7 ms | 21.3 | 13 |
| after | 50.7 ms | 90.1 ms | 2624.2 ms | 21.3 | 12 |
| after2 | 48.1 ms | 90.1 ms | 2626.5 ms | 21.4 | 12 |

Unmoved, and it was never going to move: the flicker is one frame out of five
hundred and it happens at the submit boundary, not inside the stream. The max
is the gap across a tool round. Reporting it unchanged is the point.

What moved is the drawer, over the nine submits into an idle aria:

| | idle submits | drawer opened | drawer on screen | pit seen by the sampler |
|---|---|---|---|---|
| before | 9 | 9 of 9 | 8.8 ms median (8.6 in run 2) | 5 of 9, then 4 of 9 |
| after | 9 | 0 of 9 | none | none |

The row's own life is unchanged, which is the point of the mechanism: it lives
8.8 ms before and 8.8 ms after (8.7 and 8.1 in the second pair), and the grace
only decides whether anything is drawn in that time. Nothing is.

Busy submits are untouched, which is the other half of the requirement:

| | busy submits | drawer opened | drawer on screen, median |
|---|---|---|---|
| before | 3 | 3 of 3 | 3727.6 ms |
| before2 | 3 | 3 of 3 | 3727.4 ms |
| after | 3 | 3 of 3 | 3725.9 ms |
| after2 | 3 | 3 of 3 | 3727.5 ms |

The submit itself costs nothing either way: Enter to `runtime{thinking}` is 8.4
to 12.4 ms on both sides, and the median submit window is two frames on both.

The screen sampler is the witness that matters, and it agrees: before, it
caught the pit flashing in five of nine idle rounds (four of nine in the second
run), each episode about 11 ms, which is one or two frames of a drawer opening
and closing on nothing. After, it caught none, and all three busy episodes
survive at 1.2, 1.2 and 4.3 seconds.

## What is not covered

The inline `figaro send` surface draws the same queue as a trailer under the
incipit rather than as a pit. `scripts/jitterinline.sh` measured it and found
no idle-round trailer even before the change: a fresh `send` process is still
starting up while the row lives and dies, so there is nothing to suppress
there. The fix applies to it anyway, through the same projection.

The 150 ms grace is a constant. A daemon slow enough to take longer than that
to lift will show the row and then remove it, which is the old behaviour with a
longer fuse. Nothing measured here came close.
