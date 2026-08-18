package cli

import (
	"encoding/json"
	"testing"
	"time"
)

// CTRL-C MUST STOP THE TURN ON THE DAEMON, not merely close the client.
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
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 40)

	// A turn long enough that "still running" and "stopped" are unmistakable.
	p.startTurn("run exactly this bash command and nothing else, then say SLEPT: sleep 90")

	id := busiestAria(t, env, bin)
	if id == "" {
		t.Skip("cannot resolve the aria under test")
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
	for time.Now().Before(startDeadline) {
		time.Sleep(2 * time.Second)
		if st := ariaState(t, env, bin, id); st == "running" || st == "active" {
			started = true
			break
		}
	}
	if !started {
		t.Skipf("the daemon never reported the aria running; this measurement would be vacuous:\n%s", p.visible())
	}
	if !p.alive() {
		t.Skip("the turn ended before it could be interrupted")
	}

	p.key("C-c")

	// THE CLIENT LEAVING IS NOT THE ASSERTION. The old, broken code also made
	// the client leave. What must be true is that the DAEMON stopped the turn,
	// and the only way to see that is to ask the daemon after the pane is dead.
	stopped := false
	stopDeadline := time.Now().Add(30 * time.Second)
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
		t.Fatalf("the aria is still %q ~30s after Ctrl-C, on a turn that sleeps 90s.\n"+
			"The client exited but the daemon was never told to stop. That is the shape of the\n"+
			"bug where inputInterrupt cancelled the CLIENT's context and returned keyStop without\n"+
			"ever sending figaro.interrupt -- and note that a test asserting the PROCESS EXITED\n"+
			"would have passed here.", lastState)
	}
}

// busiestAria picks the conversation with the most messages: the one this
// pane is driving. `list -j` is the global escape hatch and takes no filters.
func busiestAria(t *testing.T, env []string, bin string) string {
	t.Helper()
	var arias []struct {
		ID       string `json:"id"`
		Messages int    `json:"message_count"`
	}
	raw := figCmd(t, env, bin, "list", "-j")
	if err := json.Unmarshal([]byte(raw), &arias); err != nil {
		return ""
	}
	best, id := -1, ""
	for _, a := range arias {
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
