package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/figaro"
)

// Ctrl-C stops the turn on the daemon, by whatever path: the client's own
// context cancellation also sends the interrupt, so this does not distinguish
// inputInterrupt's explicit send from that. The dwell excludes a turn that
// was ending anyway.
func TestSmoke_CtrlCStopsTheTurnOnTheDaemon(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 40)

	p.startTurn("run exactly this bash command and nothing else, then say SLEPT: sleep 90")

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

	// Ask the daemon, not the pane: the echoed prompt contains any word the
	// pane would be grepped for.
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

	// Still active after a dwell long compared with the poll window, or a stop
	// after Ctrl-C could be the turn ending anyway.
	const dwell = 25 * time.Second
	time.Sleep(dwell)
	if st := ariaState(t, env, bin, id); st != "running" && st != "active" {
		decline(t, "the turn ended on its own after %s (state %q), so an interrupt could not be "+
			"distinguished from it finishing; the model probably did not run the long command:\n%s",
			dwell, st, p.visible())
	}

	p.key("C-c")

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

// busiestAria skips anchors and forms, which `list -j` includes, and must be
// called once the turn has messages or it resolves to an anchor.
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

// Ctrl-C with a tool in flight leaves figaro.InterruptedToolNotice in the
// durable record, which only an interrupted agent writes. Either the explicit
// send or the client's context cancellation satisfies it; a prose-only
// interrupt leaves a different mark that nothing asserts.
func TestSmoke_CtrlCLeavesTheInterruptMarkWhenAToolIsInFlight(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 40)

	// An interrupt after every tool finished leaves nothing to repair and no mark.
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

	const dwell = 20 * time.Second
	time.Sleep(dwell)
	if st := ariaState(t, env, bin, id); st != "running" && st != "active" {
		decline(t, "the turn ended on its own after %s (state %q); the model probably did not run "+
			"the long command:\n%s", dwell, st, p.visible())
	}

	// A mark already present could not be attributed to the keystroke.
	if irContains(t, env, bin, id, figaro.InterruptedToolNotice) {
		decline(t, "the interrupt mark is already present BEFORE Ctrl-C; this run cannot attribute "+
			"it to the keystroke:\n%s", figCmd(t, env, bin, "show", id, "-a", "-j"))
	}

	p.key("C-c")

	// The repair append happens after the cancel unwinds the tool.
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

// irContains reads `figaro show -a -j`, the surface a user would, not the store.
func irContains(t *testing.T, env []string, bin, id, needle string) bool {
	t.Helper()
	return strings.Contains(figCmd(t, env, bin, "show", id, "-a", "-j"), needle)
}
