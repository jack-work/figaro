package cli

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// SCENARIO 4: a client that arrives while the suffix is still moving.
//
// The composer hands the server a settled PREFIX by reference plus a stable
// count, and the server trusts that count instead of diffing. A reattaching
// client is the case where that trust is most dangerous: it is served from the
// same open frame the live client was streaming, so an over-claimed stable
// boundary can hand it a frame the live client never saw. Nothing in the unit
// suite can see this -- the server's node list stays correct either way.
//
// Three assertions, because they fail differently:
//   a) the reattached transcript's ticks equal `figaro show`'s ticks;
//   b) no desync recovery fired -- a desync that "recovers" would re-read the
//      divergence away and hide it;
//   c) the reattach happens MID-STREAM, while the boundary is being asserted
//      rather than settled.

// figCmd runs the binary under test outside the pane, against the same store.
func figCmd(t *testing.T, env []string, bin string, args ...string) string {
	t.Helper()
	c := exec.Command(bin, args...)
	c.Env = env
	out, _ := c.CombinedOutput()
	return string(out)
}

func TestSmoke_ReattachMidStreamMatchesShow(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	env, bin := smokeStore(t), smokeBinary(t)
	p := newPane(t, env, bin, 100, 60)

	p.startTurn("run exactly this bash command and nothing else, then say TICKSDONE: " +
		"for i in $(seq 1 90); do echo tick-$i; sleep 1; done")

	// Wait until the suffix is genuinely moving.
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

	// THE LIVE VIEW MUST ADVANCE.
	//
	// Comparing final states cannot catch a broken changed-set: the seal path
	// re-materializes the turn from the log, so a composer that emits nothing
	// at all still converges to a correct transcript. Proven, not assumed --
	// a mutant that claims every node is settled passed this test until this
	// assertion existed. What only a live sample can see is a node that
	// stopped moving while the tool kept writing.
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

	// The aria the daemon is running this turn for.
	// ls --json is the global escape hatch and takes no other flags; it lists
	// the whole store, anchors included, so pick the conversation that is
	// actually running this turn.
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

	// KILL THE CLIENT MID-STREAM. The daemon keeps running the turn; this is a
	// reattach, not an interrupt, so the turn must not be cancelled.
	_ = exec.Command("pkill", "-9", "-f", bin+" send").Run()
	time.Sleep(2 * time.Second)

	// REATTACH while the suffix is still moving.
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

	// (b) a desync recovery would re-read the divergence away.
	if strings.Contains(strings.ToLower(final), "desync") {
		t.Errorf("a desync recovery fired during a clean reattach; it would mask exactly the divergence this test looks for:\n%s", final)
	}

	// (a) live-vs-committed: what the reattached client rendered must be what
	// the durable log says.
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
