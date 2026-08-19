package cli

// ---------------------------------------------------------------------------
// BUG B in a real terminal: the frozen detached tail.
//
// The bug, as the user measured it: with a tool streaming, scroll ONE notch
// away from the live tail and the bottom block stops moving: detached at
// tick-10, still tick-10 eight seconds later; `G` jumps to tick-34.
//
// It is a two-halved property and a fix that gets only one half is not a fix:
//
//  1. the bottom block must ADVANCE while detached, and
//  2. THE SCREEN ABOVE MUST NOT MOVE while it advances.
//
// The careless fix: re-derive the whole window every frame while detached -
// passes (1) and fails (2): the tail window slides, and the reader's page
// scrolls under them. So this case hashes the rows above the viewport's last
// content row and requires them BYTE-IDENTICAL across the same interval.
//
// WHY IT CANNOT BE A UNIT TEST. It can, and there is one
// (TestDetachedTailAdvancesAndScreenHoldsStill). What that one cannot do is
// drive the real key path, the real frame pacer, the real daemon's streaming
// tool output and the real pty's scroll region: which is where every
// "certified green, broken in the user's shell" bug in this repo has lived.
//
// A/B IT: the numbers only mean something as a pair:
//
//	FIGARO_TMUX_SMOKE=1 FIGARO_SMOKE_BIN=/tmp/<id>/control \
//	  go test ./internal/cli -run TestSmoke_DetachedTail -v   # must FAIL
//	FIGARO_TMUX_SMOKE=1 FIGARO_SMOKE_BIN=/tmp/<id>/head \
//	  go test ./internal/cli -run TestSmoke_DetachedTail -v   # must PASS
//
// Trap 11: build both by ABSOLUTE PATH and quote each md5: `tmux
// new-session -e PATH=…` is silently ignored, so an A/B that relies on PATH
// runs the same binary twice and reports a fix as having no effect.
// ---------------------------------------------------------------------------

import (
	"crypto/md5"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tickRe finds the highest tick-NNN the streaming tool has printed. The tool
// prints one per second, so the number IS the clock: which is what makes
// "did the bottom advance" a measurement rather than an impression.
var tickRe = regexp.MustCompile(`tick-(\d+)`)

func highestTick(capture string) int {
	best := -1
	for _, m := range tickRe.FindAllStringSubmatch(capture, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > best {
			best = n
		}
	}
	return best
}

// liveBlockRow is the pane row the streaming tool block starts on: the
// boundary between "the screen above", which must hold still, and the live
// region, which must move. -1 when the block is not on screen at all, which is
// the vacuous measurement this case must never make.
func liveBlockRow(capture string) int {
	for i, ln := range strings.Split(strings.TrimRight(capture, "\n"), "\n") {
		if strings.Contains(ln, "bash for i in") {
			return i
		}
	}
	return -1
}

// contentHash fingerprints every row above the live block: the part of the
// screen that must be byte-identical across the interval. The footer is below
// it (it carries a clock), and so is every tick.
func contentHash(capture string, boundary int) (string, string) {
	rows := strings.Split(strings.TrimRight(capture, "\n"), "\n")
	if boundary < 0 || boundary > len(rows) {
		return "", ""
	}
	body := strings.Join(rows[:boundary], "\n")
	return fmt.Sprintf("%x", md5.Sum([]byte(body))), body
}

// useCopilotTerra points the ISOLATED config at copilot/gpt-5.6-terra. It
// writes only inside the smoke store's own config copy: the real config is
// read, never written (smokeStore's rule).
//
// The choice is deliberate rather than incidental: this case needs a model that
// will run one bash command verbatim and then stop, for a minute, cheaply.
func useCopilotTerra(t *testing.T, env []string) {
	t.Helper()
	dir := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "FIGARO_CONFIG_DIR="); ok {
			dir = v
		}
	}
	if dir == "" {
		t.Fatal("smoke store has no config dir")
	}
	if err := os.WriteFile(dir+"/config.toml", []byte("default_outfit = \"copilot\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lo, err := os.ReadFile(dir + "/outfits/copilot.toml")
	if err != nil {
		decline(t, "no copilot outfit in the copied config: %v", err)
	}
	patched := strings.ReplaceAll(string(lo), "gpt-5.6-sol", "gpt-5.6-terra")
	if err := os.WriteFile(dir+"/outfits/copilot.toml", []byte(patched), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSmoke_DetachedTailAdvancesAndScreenHoldsStill(t *testing.T) {
	smokeEnabled(t)
	smokeCase(t)
	env, bin := smokeStore(t), smokeBinary(t)
	useCopilotTerra(t, env)
	sum, _ := exec.Command("md5sum", bin).Output()
	t.Logf("binary under test: %s", strings.TrimSpace(string(sum)))

	p := newPane(t, env, bin, 100, 30)
	// Print the identity IN THE PANE too: the report quotes what the terminal
	// saw, not what the test process believed it launched.
	p.send("md5sum " + bin)
	p.key("Enter")

	// A tool that streams for a minute, one line per second. `figaro send`
	// auto-promotes to the pager when the live region overflows the viewport,
	// which this does within a few seconds.
	p.startTurn("run exactly this bash command and nothing else, then say TICKSDONE: " +
		"for i in $(seq 1 90); do echo tick-$i; sleep 1; done")

	// Wait for the ticks to be flowing, then open the pager by hand. (The
	// auto-promotion does not fire here: a tool block is clipped to its last
	// ten lines, so a streaming tool never overflows the viewport on its own.)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		if highestTick(p.visible()) > 3 {
			break
		}
	}
	if highestTick(p.visible()) < 0 {
		decline(t, "no ticks on screen; the model did not run the command:\n%s", p.visible())
	}
	p.key("C-t")
	time.Sleep(2 * time.Second)
	vis := p.visible()
	if pagerChrome(vis) == 0 {
		decline(t, "Ctrl-T did not open the pager:\n%s", vis)
	}

	// DETACH BY ONE NOTCH. One is the whole point: the tail stays visible, so
	// there is something to watch. Scroll far away and the ticks leave the
	// screen entirely, and comparing nothing to nothing PASSES.
	p.key("Up")
	time.Sleep(1500 * time.Millisecond)

	t0 := p.visible()
	tick0 := highestTick(t0)
	row0 := liveBlockRow(t0)
	if tick0 < 0 || row0 < 0 {
		t.Fatalf("detached one notch and the live block is not on screen at all; "+
			"this measurement would be vacuous:\n%s", t0)
	}
	if strings.Contains(t0, "live") {
		t.Fatalf("Up did not detach: the footer still says live:\n%s", t0)
	}
	hash0, body0 := contentHash(t0, row0)

	time.Sleep(10 * time.Second)

	t1 := p.visible()
	tick1 := highestTick(t1)
	row1 := liveBlockRow(t1)
	hash1, body1 := contentHash(t1, row0)
	if row1 != row0 {
		t.Errorf("THE SCREEN MOVED: the live block was on row %d, now row %d", row0, row1)
	}

	t.Logf("detached: tick %d -> %d over 10s", tick0, tick1)

	if tick1 <= tick0 {
		t.Errorf("THE DETACHED TAIL IS FROZEN: tick %d at t+0, still %d at t+10s.\n%s",
			tick0, tick1, t1)
	}
	if tick1-tick0 < 5 {
		t.Errorf("the detached tail advanced only %d ticks in 10s; it should track the stream",
			tick1-tick0)
	}
	if hash0 != hash1 {
		t.Errorf("THE SCREEN ABOVE MOVED while the tail advanced.\n--- t+0\n%s\n--- t+10s\n%s",
			body0, body1)
	}

	// And re-attaching must not jump: the reader was already seeing the truth.
	// (On the frozen build `G` leaps forward: that leap IS the bug's signature.)
	p.key("G")
	time.Sleep(1500 * time.Millisecond)
	tick2 := highestTick(p.visible())
	t.Logf("after G: tick %d (detached was %d)", tick2, tick1)
	if tick2-tick1 > 6 {
		t.Errorf("re-attaching jumped %d ticks ahead: the detached view was stale", tick2-tick1)
	}

	p.key("C-c")
	for i := 0; i < 20 && p.alive(); i++ {
		time.Sleep(500 * time.Millisecond)
	}
	_ = os.Stdout
}
