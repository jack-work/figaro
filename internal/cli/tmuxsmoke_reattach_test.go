package cli

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// figCmd runs the binary under test outside the pane, against the same store.
func figCmd(t *testing.T, env []string, bin string, args ...string) string {
	t.Helper()
	c := exec.Command(bin, args...)
	c.Env = env
	out, _ := c.CombinedOutput()
	return string(out)
}

// A client reattached mid-stream is served the settled prefix the server
// trusts by count, so an over-claimed boundary hands it a frame the live
// client never saw; the transcript must match `figaro show` with no desync.
func TestSmoke_ReattachMidStreamMatchesShow(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 60)

	p.startTurn("run exactly this bash command and nothing else, then say TICKSDONE: " +
		"for i in $(seq 1 90); do echo tick-$i; sleep 1; done")

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		if highestTick(p.visible()) > 3 {
			break
		}
	}
	live := highestTick(p.visible())
	if live < 0 {
		decline(t, "no ticks on screen; the model did not run the command:\n%s", p.visible())
	}

	// The seal re-materializes the turn from the log, so only a live sample can
	// see a node that stopped moving while the tool kept writing.
	if live >= 90 || !p.alive() {
		decline(t, "the tool finished before a live sample could be taken (tick-%d); nothing to observe", live)
	}
	time.Sleep(8 * time.Second)
	advanced := highestTick(p.visible())
	if advanced <= live && advanced < 90 {
		t.Fatalf("the live view stopped advancing: tick-%d, then tick-%d eight seconds later\n"+
			"the tool is still writing, so a node the composer declared settled is not\n%s",
			live, advanced, p.visible())
	}
	live = advanced

	// list -j includes anchors; the conversation is the one with messages.
	var arias []struct {
		ID       string `json:"id"`
		State    string `json:"state"`
		Messages int    `json:"message_count"`
	}
	raw := figCmd(t, env, bin, "list", "-j")
	if err := json.Unmarshal([]byte(raw), &arias); err != nil || len(arias) == 0 {
		decline(t, "cannot resolve the aria under test: %v\n%s", err, raw)
	}
	id := ""
	best := -1
	for _, a := range arias {
		if a.Messages > best {
			best, id = a.Messages, a.ID
		}
	}
	if id == "" {
		decline(t, "no aria with messages in the scratch store:\n%s", raw)
	}

	// A killed client is a reattach, not an interrupt: the turn must keep running.
	_ = exec.Command("pkill", "-9", "-f", bin+" send").Run()
	time.Sleep(2 * time.Second)

	p.send(bin + " listen " + id)
	p.key("Enter")
	time.Sleep(3 * time.Second)
	reattached := p.scrollback()
	if got := highestTick(reattached); got < live {
		t.Errorf("reattached view is BEHIND what the live client already showed: live saw tick-%d, reattached shows tick-%d\n"+
			"a settled prefix was served to a new reader without the frames that moved it", live, got)
	}

	p.waitIdle(120 * time.Second)
	final := p.scrollback()

	// A desync recovery would re-read the divergence away.
	if strings.Contains(strings.ToLower(final), "desync") {
		t.Errorf("a desync recovery fired during a clean reattach; it would mask exactly the divergence this test looks for:\n%s", final)
	}

	shown := figCmd(t, env, bin, "show", id)
	liveTick, showTick := highestTick(final), highestTick(shown)
	if showTick < 0 {
		decline(t, "show has no ticks; the tool never ran:\n%s", shown)
	}
	if liveTick != showTick {
		t.Errorf("live/committed divergence: the reattached transcript's highest tick is %d, fig show's is %d\n"+
			"the same turn is telling two stories, which the purity invariant forbids", liveTick, showTick)
	}
}
