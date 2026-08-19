# Ctrl-C: what was fixed, what was overstated, and what is still unknown

By aria 091d162e, 2026-08-18. Written because I reported a diagnosis to
Gluck more confidently than the evidence supported, and the correction
should outlive the chat.

## THE DEFECT, WHICH IS REAL AND FIXED (2d5dd424)

`inputInterrupt` (internal/cli/stream.go) ended:

    in.cancelTranscriptSearch()
    in.cancelSelectionCopy()
    in.cancel()      // the CLIENT's own context
    return keyStop   // quit the process

It never sent `figaro.interrupt`. A grep for MethodInterrupt across
internal/cli returns ONE hit, a method-name list in replay.go. Meanwhile
cli.go:367 promises "Ctrl-C  Interrupt the turn (sends figaro.interrupt)".
Only H (inputHangUp) and X reach the daemon.

FIXED: Ctrl-C now sends the interrupt and lets turn.done close the client
(that path already exists in incipit), with a second press as an escape
hatch and a failed RPC reporting and leaving rather than hanging.

## WHAT I OVERSTATED

I told Gluck "the turn ran on with nobody watching". THAT IS NOT
SUPPORTED. The pty case
(TestSmoke_CtrlCStopsTheTurnOnTheDaemon) asserts the DAEMON's state after
the pane is gone, and it PASSES ON THE BROKEN CODE -- canaried twice,
9.69s and 34.73s, the second with a 25-second dwell proving the aria was
still active immediately before Ctrl-C.

SO THE TURN STOPS WHEN THE CLIENT DIES, BY SOME PATH THAT IS NOT
figaro.interrupt.

The fix is still right -- the docs promise it, H is otherwise the only key
that reaches the daemon, and relying on client death is relying on a side
effect rather than a contract -- but the user-visible harm I described is
unproven.

## WHAT IS STILL UNKNOWN, AND THE LEADS ALREADY EXCLUDED

WHAT ENDS THE TURN WHEN THE CLIENT DIES? Excluded by inspection:
  - the CLI never hosts an agent (`grep figaro.NewAgent internal/cli cmd`
    is empty); the daemon does
  - reapDeadPIDs only UNBINDS a dead pid; it does not stop turns
  - emitLive never errors on subscriber loss, so the fanout failing
    cannot propagate into roundErr
  - idle eviction checks `info.State != "idle"` first, so a running turn
    should not be reclaimed

A LEAD NOT YET FOLLOWED, and it would change the conclusion again: the
test reads `state` from `figaro list -j`, which reports "dormant" for any
aria NOT IN THE LIVE REGISTRY. If losing its last bound pid removes the
agent from that registry, the test would read "dormant" while the turn
CONTINUES -- meaning the instrument measures "the agent left the
registry", not "the turn stopped", and my original diagnosis would be
restored. THIS IS UNVERIFIED. It is the first thing to check.

## WHAT WOULD SETTLE IT

Assert on something only the interrupt produces: a turn.done reason of
"interrupted" (not "error:...", not absent), read from the durable log
after the pane is gone. That distinguishes "the turn was stopped by the
RPC" from "the turn stopped somehow" and from "the agent left the
registry", which the current state poll cannot.

## THE SHAPE OF THE MISTAKE, for the record

Three versions of this test were wrong before this one, each in a way
this campaign has already catalogued:
  1. guard was `strings.Contains(p.visible(), "bash")` -- and the PROMPT
     contains "bash", which the pane echoes on keypress. Passed in 6.49s
     having asserted that a turn which never started was not running.
  2. the aria was resolved BEFORE the turn began, so busiestAria picked an
     ANCHOR: "states observed for null: map[anchor:30]".
  3. no dwell, so "stopped shortly after Ctrl-C" was satisfied by a turn
     that was ending anyway.
The fourth still cannot tell the fix from the bug. Each failure was
caught by a guard or a canary rather than by reasoning, which is the only
reason the overstatement was found at all.

## THE FIRST LEAD, CHASED AND EXCLUDED BY INSPECTION (aria 7e151902, 2026-08-18)

Recorded beside the paragraph above rather than over it: the lead named
there as "the first thing to check" does not survive reading the code.

THE LEAD: `figaro list -j` reports "dormant" for any aria not in the live
registry, so if losing its last bound pid removed the agent from that
registry, the pty test would read "dormant" while the turn CONTINUED --
which would restore 091d162e's original diagnosis.

WHAT THE CODE SAYS. "dormant" is stamped in protocol.go:1528, for
conversation ids NOT already `seen` -- and `seen` is filled from
Registry.List(), which walks `r.figaros`. So the question is exactly:
CAN A RUNNING AGENT LEAVE `r.figaros`?

  reapDeadPIDs (angelus.go:602) calls Registry.Unbind(pid), and
  unbindLocked (registry.go:216) deletes from pidToFigaro, pidToLT and
  figaroPIDs ONLY. It never touches r.figaros. A pid death cannot
  de-register an agent.

  The two paths that DO delete from r.figaros are Registry.Kill and
  Registry.Hibernate, and both reclamation callers gate on idleness:
  hibernateIdleArias and capLiveArias skip any info.State != "idle", and
  Hibernate itself (registry.go:121) refuses a non-idle agent TWICE --
  once before taking the retiring flag and once after, with the comment
  "a turn that opened while we were taking the flag wins".

  The remaining agent.Kill() sites (protocol.go:733, 742, 2128, 2134) are
  the error paths of create and restore, not a disconnect handler.

SO: no mechanism was found by which a busy agent leaves the live registry
when its client dies, and the instrument's reading of "dormant" therefore
does mean "the agent is gone", not "the agent is merely unbound".

WHAT THAT DOES AND DOES NOT SETTLE. It removes the lead that would have
restored the original diagnosis; it does NOT explain what ends the turn
when the client dies, which remains open with every previously excluded
candidate still excluded. It also does not make the pty test able to tell
the fix from the bug -- that still requires the experiment already named
here: assert a turn.done reason of "interrupted", read from the DURABLE
LOG after the pane is gone. Nothing weaker distinguishes "stopped by the
RPC" from "stopped somehow".

THIS IS INSPECTION, NOT MEASUREMENT, and it is offered as such. It
excludes a mechanism; it cannot prove the absence of one I did not think
to grep for. The durable-log assertion is still the thing that would
settle it, and it is worth more than another read of the registry.

---

## CORRECTION, 2026-08-18 20:00: THE LEAD IS EXCLUDED

Added beside the paragraph above rather than over it, per this campaign's
rule that a correction must live where the wrong version was read.

I wrote that the first thing to check was whether `figaro list -j`
reports "dormant" for an agent that has merely LEFT THE LIVE REGISTRY
while its turn continues -- which, if true, would have meant the test
measured "the agent left the registry" and my original diagnosis stood.

IT IS NOT TRUE. Excluded by inspection (aria 7e151902): Registry.Unbind
never touches r.figaros, and BOTH reclaim paths refuse a non-idle agent.
So an agent running a turn cannot leave the live registry, and "dormant"
in that listing DOES mean the turn is gone.

CONSEQUENCE: the pty case is measuring what it claims to measure, and the
finding stands in its stronger form -- THE TURN REALLY DOES STOP WHEN THE
CLIENT DIES, by a path that is not figaro.interrupt. My overstatement to
Gluck ("the turn ran on with nobody watching") is therefore wrong rather
than merely unproven, and the fix at 2d5dd424 is justified on contract
grounds only: cli.go:367 promises the RPC, H was otherwise the only key
that reached the daemon, and depending on client death is depending on a
side effect.

WHAT REMAINS UNKNOWN is unchanged and is now the ONLY open question here:
WHAT path stops the turn when the client dies. Excluded so far: the CLI
never hosts an agent; reapDeadPIDs only unbinds; emitLive never errors on
subscriber loss; idle eviction refuses a running agent; and now the
registry-departure theory. Something else ends it, and nothing in the
tree yet says what.

## ANSWERED, 2026-08-18, BY A CANARY THAT REFUSED TO GO RED (aria 7e151902)

The question this note ends on — WHAT ENDS THE TURN WHEN THE CLIENT DIES —
is answered, and the answer falsifies the premise the whole note was
built on.

THE EXPERIMENT. A new pty case reads the DURABLE log after the pane is
gone and asserts `figaro.InterruptedToolNotice` ("interrupted: tool
execution did not complete"), which repairTurnTail writes only under
`a.isInterrupted()` — a flag only `Agent.Interrupt` sets, and only the
`figaro.interrupt` RPC reaches. It passed in 30.12s.

THEN THE CANARY: `inputInterrupt` reverted to its pre-2d5dd424 body
(`in.cancel(); return keyStop`), rebuilt (CANARY BUILD: OK), re-run.

    fixed build    PASS 30.12s
    reverted build PASS 30.12s

IT STAYED GREEN, at the same time to the centisecond. So the mark is
produced WITHOUT inputInterrupt sending anything, and this instrument
cannot tell the fix from the bug either — the same verdict the state poll
earned, reached by a better route.

THE MECHANISM, FOUND BY FOLLOWING THAT: `internal/cli/stream.go`, the
`case <-ctx.Done():` arm of the streaming loop, ALREADY CALLS
`fcli.Interrupt(intCtx)` when the CLIENT's own context is cancelled — and
cancelling the client's context is exactly what the old `in.cancel()` did.

    THE DAEMON WAS ALWAYS TOLD. By a different line than anyone looked at.

WHY THE ORIGINAL GREP MISSED IT: it searched for `MethodInterrupt` across
internal/cli, and this call site goes through the CLIENT METHOD
(`fcli.Interrupt`), which does not contain that identifier. The one hit it
found — a method-name list in replay.go — was true and irrelevant. A grep
for the constant cannot see a call that names the wrapper.

WHAT THIS DOES TO THE RECORD:
  - "the turn ran on with nobody watching" is now FALSE, not merely
    unproven. 091d162e's retraction did not go far enough, through no
    fault of its reasoning: five leads were excluded by inspection and the
    sixth was never a candidate because the grep had ruled the family out.
  - 2d5dd424 IS STILL RIGHT, on narrower grounds. It makes the intent
    explicit instead of relying on a side effect of context cancellation;
    it waits for turn.done rather than racing the exit; and it adds a
    second-press escape hatch for a daemon that will not answer. Those are
    real, and none of them is "the daemon was never told".
  - THE PTY CASE IS STILL WORTH KEEPING, for what it does prove: that
    Ctrl-C reaches the daemon AS AN INTERRUPT, visible in the durable
    record after the client is gone. Its name now carries its scope
    (a tool in flight) and its comment carries this canary result.

WHAT IS STILL UNGUARDED, named rather than left to be found: the
PROSE-ONLY interrupt. The mark asserted here is a TOOL notice. A turn
interrupted mid-paragraph with no tool running leaves a different mark —
repairTurnTail appends the partial assistant with StopReason StopAborted
(turn_repair.go:130-141) — and nothing asserts it.

THE SHAPE OF THE MISTAKE, one level up from the note's own version of it:
a diagnosis was founded on a grep for an IDENTIFIER, and the code reached
the same behaviour through a WRAPPER. Every subsequent correction refined
the conclusion without ever re-examining that first search. The canary
found in one run what six hours of careful reasoning could not, because it
asked the code instead of asking the reader.
