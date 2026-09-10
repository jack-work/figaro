# The journal: measurement, and a walk of every emit site

Written on `feat/journal` after 1235aa32, answering the three questions the
handoff left open. Sections 1 and 2 are findings; section 3 is what changed.

## 1. The number

The gap the fix closes is exactly the provider's time-to-first-token, and the
instrument that says so is `TestSteerVisibilityGap`
(`internal/figaro/steer_visibility_test.go`). It runs the real agent against a
mock provider that stalls its second round by a settable amount, and times two
events a reader can see:

	t_clear    the queue intrinsic says the steer left the drawer
	t_visible  an aria frame carries the steer's text

| arm | injected TTFT | t_clear | t_visible | gap |
|---|---|---|---|---|
| 8ee80d7c (before) | 3s | 1ms | 3.001s | **3.000s** |
| 8ee80d7c (before) | 3s | 1ms | 3.002s | **3.001s** |
| 8ee80d7c (before) | 1s | 1ms | 1.001s | **1.000s** |
| feat/journal (after) | 3s | 1ms | 1ms | **0ms** |
| feat/journal (after) | 1s | 1ms | 1ms | **0ms** |

The gap TRACKS the injected stall rather than being a constant of the fixture,
which is the point of making it settable (`FIGARO_STEER_TTFT=1s`). Before: the
steer is durable and invisible for one TTFT. After: one frame, unmeasurable at
millisecond resolution.

Two fixture traps cost a run each and are recorded in the test:

- **A steer submitted back to back with the opener is not a steer.** The inbox
  coalesces the contiguous run into the inquiry; the second message reported
  `merged` and nothing was ever steered. It must go in while the turn is
  thinking, which is why round one holds itself open.
- **The opening prompt commits a millisecond in**, so timing the first queue
  row that says `committed` times the wrong message. The test learns the
  steer's item id from the patch carrying its text.

### The pty arm

`scripts/steer-visibility-pty.sh` drives the real binary against the real
provider. On the AFTER build it agrees with the number above:

	t_clear = 29713ms   t_visible = 29713ms   gap = 0ms (one 200ms poll)

and the final pane shows `↳ input · SENTINEL` under the finished tool.

**The BEFORE arm never produced a number, in three attempts, because the steer
never reached the daemon at all.** `:send -- SENTINEL` typed into the live view
at t=12s left no queue row, no steering node, and no trace in the capture; the
aria's own store afterwards holds four messages (inquiry, tool call, result,
`DONE`) and no steer. The same keystrokes on the AFTER binary queued the message
every time, and nothing in 1235aa32 can plausibly explain that, so read it as an
unexplained result rather than a property of the fix. Two things worth knowing:
`TestSmoke_QueuedMessageIsHeldUntilItRoundTrips` drives exactly this path and
should be failing if it is a live bug; and the runs that lost the message were
in the pager while the run that kept it was inline, which is where I would look
first.

## 2. The emit sites

Eighteen call sites, of which fifteen are broadcasts. The finding that matters
is not redundancy.

### aria.Server.Update DROPS a frame when no unit is open

```go
func (s *Server) Update(prefix, suffix []livedoc.Node, stable int) {
	s.mu.Lock()
	if s.open == nil { s.mu.Unlock(); return }
```

So "every append announces" holds only while a live unit is open, and the steer
path deliberately closes one first:

	emitCommit()           -> Close(), s.open = nil
	appendPromptEvents()   -> append -> journal publishes -> Update DROPPED
	startAssistantUnit()   -> OpenTurn(), s.open set again
	emitDelta(...)         <- THIS is the frame the reader sees

The two `emitDelta` calls this commit added after `startAssistantUnit` are
therefore not belt-and-braces beside the journal: they are the fix. The journal
is the general law, and in the one path that motivated it the law is silent.
The same is true of the FIRST append of a turn (`endTurn` closed the unit, so
the inquiry's own append announces nothing; `OpenInquiry` carries it instead).

Nothing in the suite would notice if either `emitDelta` were deleted:
`journal_test.go` tests the journal type in isolation, and the canary quoted in
the commit message removes the journal's publish, not these lines. **A test that
pins the steer's visibility end to end is the gap in the tree.**

Options, in the order I would take them: teach `Update` to fold a frame that
arrives with no open unit onto the turn's committed nodes; or have the journal
open the unit itself; or leave the code as it is and write the missing test.
Only the third is safe to do without a design call.

### Redundancy, site by site

An emit right after an append is now a second compose of the same node list.
The cost is a compose and nothing else: `Update` computes deltas against the
prior frame and returns before `deliver` when they are empty, so a duplicate
frame cannot reach the wire.

| site | verdict |
|---|---|
| `agent.go:278` `ariaSrv.Commit(t)` | construction, no append. Keep. |
| `agent.go:1183` `emitCommit()` | freezes the live unit. Not an emit of the same thing. Keep. |
| `turn.go:290` `OpenInquiry` | carries the question, which the journal's frame does not. Keep, and it is the only announcement the opening append gets (see above). |
| `turn.go:346, 607, 635, 654, 710, 726, 737` | all `if len(repaired) > 0 { emitDelta }` after `repairTurnTail`, which appends through the journal and changes nothing after. **Redundant.** All in error/interrupt paths, so the saving is zero; the risk of removing them is the `s.open == nil` drop above. Left alone deliberately. |
| `turn.go:623` | after the assistant tail append. The drain loop already forced an `emitLive` at `evFigaro`, so this was a second compose BEFORE the journal and is a third now. **Redundant.** |
| `turn.go:748` | after `appendMsg(resultTic)`, once per tool round. **Redundant except for metrics** — see below. |
| `turn.go:782, 831` `emitCommit()` | close the unit before a steer splits it. Keep. |
| `turn.go:804, 839` | the steer emits. NOT redundant: they are the only frames that survive (see above). Keep. |
| `turn.go:1480` (`emitLive`) | the streaming clock. Must stay. |

### The one behavioural regression: metrics ride a suppressed frame

Every aria-server broadcast is stamped with `sessionMetrics()` by the
subscription in `NewAgent`. Two hot paths now refresh metrics BETWEEN the
append and the emit:

	appendMsg(resultTic)   -> journal publishes  (frame carries STALE metrics)
	a.refreshMetrics()
	a.emitDelta(...)       -> deltas empty       (frame SUPPRESSED)

and the same shape at `evFigaro`. The nodes are identical, so the second frame
is dropped, and the refreshed context count rides nothing. It catches up on the
next frame that changes a node, which in an ordinary turn is milliseconds away
-- so this is a footer that lags by one event, not a wrong number. Named here
because it is a real consequence of moving the announcement earlier, and
because the fix is small: refresh metrics inside the journal's publish, which
is the same argument `appendUserPrompt` already makes for putting
`refreshMetrics` ahead of `OpenInquiry`.

## 3. Comments trimmed

Cut the measured-symptom paragraphs where a named test carries the same
explanation, keeping one line and the test's name:

- `intrinsic.go` (`publishRuntime`): the leaf-vs-branch law stays; the shipped-bug
  paragraph goes. `TestRuntimeIntrinsicPublishesEveryKeyIncludingProtectedOnes`.
- `inbox.go` (`markCommitted`): `TestACoalescedRunLeavesTheQueueEntirely`.
- `session_status.go` (`beginTurn`): `TestABeginTurnDoesNotClobberAnAuthoritativeState`.

Two on the handoff's cut list were KEPT, because the rule of thumb says so --
deleting them would let someone reintroduce the bug with no test failing:

- `intrinsic_mirror.go`, the epoch note. `TestQueueProjectionCarriesTheEpoch`
  covers `readQueue`, NOT the `in.queueEpoch = epoch` assignment, which is
  written in one place and read in another and so can be dropped without
  breaking a build or a test. The comment now says that.
- `transcript.go` (`showQueuedAuto`), refreshing-is-not-closing. Nothing covers
  it; `transcript_resize_paint_test.go` only opens and closes the pit. Shortened
  and marked as uncovered.

The test now ASSERTS rather than only logging: the gap must be under half the
injected stall. Canaried at 8ee80d7c, where it fails with the measured 3.001s.

## 4. Still outstanding (Gluck's list, untouched)

- a shorter notice TTL: `config.NoticeTTL` has never been read by anything
- a `notifications` intrinsic form
- delete `inflight` from `/runtime`: it only updates on turn transitions and
  duplicates `/queue`'s `len`
