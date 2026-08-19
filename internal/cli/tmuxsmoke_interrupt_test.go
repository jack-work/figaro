package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/figaro"
)

// CTRL-C MUST STOP THE TURN ON THE DAEMON, not merely close the client.
//
// READ THIS BEFORE TRUSTING IT: THIS TEST DOES NOT DISTINGUISH THE FIX FROM
// THE BUG. Canaried twice -- inputInterrupt reverted to `in.cancel(); return
// keyStop`, never sending figaro.interrupt -- and IT PASSED BOTH TIMES, once
// in 9.69s and again in 34.73s with a 25-second dwell proving the turn was
// still running immediately before Ctrl-C.
//
// SO THE TURN STOPS WHEN THE CLIENT DIES, BY SOME PATH THAT IS NOT
// figaro.interrupt. That is a finding, and it narrows the bug: sending the
// interrupt is still correct -- cli.go:367 promises it, H is otherwise the
// only key that reaches the daemon, and relying on client death to stop a
// turn is relying on a side effect -- but the claim that "the turn ran on with
// nobody watching" IS NOT SUPPORTED BY THIS TEST and should not be repeated
// until something demonstrates it.
//
// WHAT THIS TEST IS WORTH: it pins that Ctrl-C stops the turn, by whatever
// path, with a dwell that excludes a turn which was ending anyway. That is a
// real property and it was previously unguarded in either direction. What it
// is NOT is evidence for the fix, and it must not be cited as such.
//
// TO MAKE IT DISCRIMINATING, someone must find the other path and disable it,
// or assert on something only the interrupt produces -- a turn.done reason of
// "interrupted" rather than an error, say, read from the durable log after the
// pane is gone. That is owed.
//
// THIS IS THE TEST THAT WAS MISSING WHEN THE BUG SHIPPED. inputInterrupt used
// to call in.cancel() and return keyStop -- cancelling the CLIENT's context and
// quitting -- while never sending figaro.interrupt. A grep for MethodInterrupt
// across internal/cli found one hit, a method-name list in replay.go. The
// daemon was never told, so the turn ran on with nobody watching.
//
// EVERY UNIT TEST PASSED THROUGHOUT. They call Agent.Interrupt() directly,
// which exercises the half that works. TestSmoke_ExitKeysWork does send C-c
// mid-stream -- and asserts only that THE PROCESS EXITS, which it did, all
// along, by the broken path. A test that watches the client cannot see a bug
// whose whole shape is "the client leaves and the daemon does not stop".
//
// So this asserts the DAEMON's state, from outside the pane, after the client
// is gone. That is the only vantage from which the bug is visible.
func TestSmoke_CtrlCStopsTheTurnOnTheDaemon(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 40)

	// A turn long enough that "still running" and "stopped" are unmistakable.
	p.startTurn("run exactly this bash command and nothing else, then say SLEPT: sleep 90")

	// RESOLVE THE ARIA ONLY ONCE IT HAS MESSAGES -- see busiestAria.
	var id string
	resolveDeadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(resolveDeadline) {
		if id = busiestAria(t, env, bin); id != "" {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if id == "" {
		decline(t, "no conversation appeared in the store within 60s:\n%s", figCmd(t, env, bin, "list", "-j"))
	}

	// WAIT FOR THE DAEMON TO SAY IT IS RUNNING -- not for the pane to show a
	// word. The first version of this guard was
	//
	//	strings.Contains(p.visible(), "bash")
	//
	// and the PROMPT contains "bash", which the pane echoes the instant it is
	// typed. So the guard passed in two seconds, before the model had decided
	// anything, and the test then "verified" that a turn which had never
	// started was not running. It PASSED in 6.49s and asserted nothing. The
	// harness documents this exact trap at bodyLines(): "counting a token
	// across the whole capture is UNSOUND. The footer mantra echoes the
	// prompt, the prompt usually contains your token."
	//
	// The guard now asks the SAME SOURCE the assertion asks, which is the only
	// way the before and after states are comparable at all.
	startDeadline := time.Now().Add(60 * time.Second)
	started := false
	seen := map[string]int{}
	for time.Now().Before(startDeadline) {
		time.Sleep(2 * time.Second)
		st := ariaState(t, env, bin, id)
		seen[st]++
		if st == "running" || st == "active" {
			started = true
			break
		}
	}
	if !started {
		// REPORT WHAT WAS SEEN, not merely that the wait failed. A skip that
		// says "it never happened" without saying what DID happen is the same
		// dead end as a benchmark reporting a number without saying whether it
		// did the work: the next person re-runs it to learn what this run
		// already knew.
		decline(t, "the daemon never reported the aria active within 60s.\nstates observed for %s: %v\nall arias: %s\npane:\n%s",
			id, seen, figCmd(t, env, bin, "list", "-j"), p.visible())
	}
	if !p.alive() {
		decline(t, "the turn ended before it could be interrupted")
	}

	// PROVE THE TURN IS LONG BEFORE INTERRUPTING IT.
	//
	// WITHOUT THIS THE TEST PASSES ON THE BROKEN CODE, and it did: with
	// inputInterrupt reverted to `in.cancel(); return keyStop` -- never
	// sending figaro.interrupt -- this case still passed in 9.69s. The reason
	// is that "the aria stopped shortly after Ctrl-C" is satisfied by a turn
	// that was ABOUT TO STOP ANYWAY. If the model does not actually run the
	// long sleep, the turn ends on its own inside the poll window and the
	// assertion is true for the wrong reason.
	//
	// So: require the aria to be STILL ACTIVE after a dwell that is long
	// compared with the poll window. Then a stop shortly after Ctrl-C cannot
	// be coincidence, and on the broken path the turn keeps running for the
	// rest of the sleep.
	const dwell = 25 * time.Second
	time.Sleep(dwell)
	if st := ariaState(t, env, bin, id); st != "running" && st != "active" {
		decline(t, "the turn ended on its own after %s (state %q), so an interrupt could not be "+
			"distinguished from it finishing; the model probably did not run the long command:\n%s",
			dwell, st, p.visible())
	}

	p.key("C-c")

	// THE CLIENT LEAVING IS NOT THE ASSERTION. The old, broken code also made
	// the client leave. What must be true is that the DAEMON stopped the turn,
	// and the only way to see that is to ask the daemon after the pane is dead.
	stopped := false
	stopDeadline := time.Now().Add(25 * time.Second)
	var lastState string
	for time.Now().Before(stopDeadline) {
		time.Sleep(2 * time.Second)
		lastState = ariaState(t, env, bin, id)
		if lastState != "" && lastState != "running" && lastState != "active" {
			stopped = true
			break
		}
	}
	if !stopped {
		t.Fatalf("the aria is still %q ~25s after Ctrl-C, on a turn that was verifiably still\n"+
			"running %s earlier and sleeps 90s in total.\n"+
			"The client exited but the daemon was never told to stop. That is the shape of the\n"+
			"bug where inputInterrupt cancelled the CLIENT's context and returned keyStop without\n"+
			"ever sending figaro.interrupt -- and note that a test asserting the PROCESS EXITED\n"+
			"would have passed here.", lastState, dwell)
	}
}

// busiestAria picks the CONVERSATION with the most messages.
//
// IT MUST SKIP ANCHORS AND FORMS, and it must be called once the turn is
// under way. `list -j` lists the whole store -- anchors, forms and
// conversations alike -- and on a fresh smoke store the conversation has no
// messages until the turn starts, so a plain max-by-message-count resolves to
// an ANCHOR. That is not hypothetical: this test skipped for an hour with
//
//	states observed for null: map[anchor:30]
//
// which is thirty polls of the wrong node, faithfully reporting that an anchor
// is not running a turn. The skip message printing the observed states is the
// only reason it took one run rather than several to see.
func busiestAria(t *testing.T, env []string, bin string) string {
	t.Helper()
	var arias []struct {
		ID       string `json:"id"`
		State    string `json:"state"`
		Kind     string `json:"kind"`
		Messages int    `json:"message_count"`
	}
	raw := figCmd(t, env, bin, "list", "-j")
	if err := json.Unmarshal([]byte(raw), &arias); err != nil {
		return ""
	}
	best, id := 0, ""
	for _, a := range arias {
		if a.State == "anchor" || a.State == "form" || a.ID == "" {
			continue
		}
		if a.Messages > best {
			best, id = a.Messages, a.ID
		}
	}
	return id
}

func ariaState(t *testing.T, env []string, bin, id string) string {
	t.Helper()
	var arias []struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(figCmd(t, env, bin, "list", "-j")), &arias); err != nil {
		return ""
	}
	for _, a := range arias {
		if a.ID == id {
			return a.State
		}
	}
	return ""
}

// CTRL-C REACHES THE DAEMON AS AN INTERRUPT, PROVEN FROM THE DURABLE RECORD --
// AND ITS CANARY ANSWERED A QUESTION THAT HAD BEEN OPEN ALL DAY.
//
// SCOPE, WHICH THE NAME NOW CARRIES: this covers a turn WITH AN OPEN TOOL CALL.
// The mark it reads is a TOOL notice, so it says nothing about a prose-only
// interrupt -- the model mid-paragraph with no tool running, which is the case
// a user hits most often. That path does leave a durable mark of its own
// (repairTurnTail appends the partial assistant with StopReason StopAborted,
// turn_repair.go:130-141) and NOTHING ASSERTS IT. Named here rather than left
// to be discovered, because an unstated boundary is how a test comes to be
// quoted for more than it proves.
//
// WHAT IT ASSERTS. figaro.InterruptedToolNotice is written by repairTurnTail,
// which runs only under a.isInterrupted(), which only Agent.Interrupt sets,
// which only the figaro.interrupt RPC reaches. So the mark proves THE DAEMON
// WAS TOLD -- not merely that the aria stopped, which is all `list -j` state
// polling can see. That property was previously unguarded in either direction.
//
// THE CANARY WENT GREEN, AND THAT IS THE FINDING. inputInterrupt was reverted
// to its pre-2d5dd424 body (in.cancel(); return keyStop), rebuilt, re-run --
// and this case PASSED ANYWAY, in 30.12s, the same time to the centisecond as
// the fixed build. The mark is reachable without inputInterrupt sending
// anything, so THIS TEST DOES NOT DISTINGUISH 2d5dd424 FROM ITS PREDECESSOR
// and must never be cited as evidence for it.
//
// WHY, and it closes ~/notes/figaro/ctrl-c-open-question.md: stream.go's
// `case <-ctx.Done():` arm ALREADY SENDS fcli.Interrupt when the client's own
// context is cancelled -- which is exactly what the old in.cancel() did. THE
// DAEMON WAS ALWAYS TOLD, by a different line than anyone was looking at. The
// grep that founded the original diagnosis searched for MethodInterrupt, and
// this call site goes through the client method, so it was invisible to it.
// The fix is still right -- it makes the intent explicit, waits for turn.done
// rather than racing the exit, and gives a second press as an escape hatch --
// but "the turn ran on with nobody watching" was never true.
//
// SO WHAT THIS GUARDS IS A DISJUNCTION, and that is why it is kept: THE DAEMON
// LEARNS OF THE INTERRUPT, BY WHATEVER ROUTE, AND THE PROOF SURVIVES THE
// CLIENT'S DEATH. It cannot say WHICH mechanism satisfied it, and it must not
// be read as saying so. Remove either path and it stays green, correctly,
// because the user is still served; remove BOTH and it goes red. A disjunction
// is worth guarding exactly when two independent paths exist -- it is the only
// thing that notices the day the LAST of them is deleted, which is the kind of
// removal a refactor makes without meaning to.
func TestSmoke_CtrlCLeavesTheInterruptMarkWhenAToolIsInFlight(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 40)

	// A tool that runs far longer than the test, so an interrupt lands while
	// it is genuinely incomplete. An interrupt that arrives after every tool
	// finished leaves nothing to repair and therefore no mark: the assertion
	// would be false for a reason that is not the bug.
	p.startTurn("run exactly this bash command and nothing else, then say SLEPT: sleep 120")

	var id string
	resolveDeadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(resolveDeadline) {
		if id = busiestAria(t, env, bin); id != "" {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if id == "" {
		decline(t, "no conversation appeared in the store within 60s:\n%s", figCmd(t, env, bin, "list", "-j"))
	}

	startDeadline := time.Now().Add(60 * time.Second)
	started := false
	seen := map[string]int{}
	for time.Now().Before(startDeadline) {
		time.Sleep(2 * time.Second)
		st := ariaState(t, env, bin, id)
		seen[st]++
		if st == "running" || st == "active" {
			started = true
			break
		}
	}
	if !started {
		decline(t, "the daemon never reported the aria active within 60s.\nstates observed for %s: %v\nall arias: %s\npane:\n%s",
			id, seen, figCmd(t, env, bin, "list", "-j"), p.visible())
	}
	if !p.alive() {
		decline(t, "the turn ended before it could be interrupted")
	}

	// PROVE THE TURN IS LONG, as the case above does and for the same reason:
	// "the mark appeared shortly after Ctrl-C" is worthless if the turn was
	// ending anyway.
	const dwell = 20 * time.Second
	time.Sleep(dwell)
	if st := ariaState(t, env, bin, id); st != "running" && st != "active" {
		decline(t, "the turn ended on its own after %s (state %q); the model probably did not run "+
			"the long command:\n%s", dwell, st, p.visible())
	}

	// VACUITY GUARD. If the mark is ALREADY in the log, this test cannot
	// report anything about the keystroke: a stale mark from an earlier turn
	// would satisfy the assertion without the interrupt path running at all.
	// The harness documents the shape (bodyLines): a check whose success and
	// whose irrelevance look identical.
	if irContains(t, env, bin, id, figaro.InterruptedToolNotice) {
		decline(t, "the interrupt mark is already present BEFORE Ctrl-C; this run cannot attribute "+
			"it to the keystroke:\n%s", figCmd(t, env, bin, "show", id, "-a", "-j"))
	}

	p.key("C-c")

	// THE MARK MUST REACH THE DURABLE RECORD. Poll, because the repair append
	// happens after the cancel unwinds the tool.
	found := false
	markDeadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(markDeadline) {
		time.Sleep(2 * time.Second)
		if irContains(t, env, bin, id, figaro.InterruptedToolNotice) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no %q in the durable log ~40s after Ctrl-C, on a turn verifiably still running\n"+
			"%s earlier with a 120s tool in flight.\n"+
			"That mark is written by repairTurnTail, which runs only when the AGENT knows it was\n"+
			"interrupted -- so its absence says the daemon was never told, which is exactly the\n"+
			"bug a test watching the CLIENT cannot see.\n\nstate now: %q\n\nlog:\n%s",
			figaro.InterruptedToolNotice, dwell, ariaState(t, env, bin, id),
			figCmd(t, env, bin, "show", id, "-a", "-j"))
	}
}

// irContains asks the DURABLE record, through the same public surface a user
// would: `figaro show -a -j`, the wire IR verbatim. Reading the store directly
// would test a store the product does not use to answer this question.
func irContains(t *testing.T, env []string, bin, id, needle string) bool {
	t.Helper()
	return strings.Contains(figCmd(t, env, bin, "show", id, "-a", "-j"), needle)
}
