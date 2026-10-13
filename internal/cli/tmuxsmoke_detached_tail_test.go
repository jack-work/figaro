package cli

// Detached one notch from a streaming tool, the bottom block advances and the
// rows above it stay byte-identical. For an A/B, pass each binary by absolute
// path in FIGARO_SMOKE_BIN: `tmux new-session -e PATH=` is silently ignored.

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

// The tool prints one tick per second, so the highest tick is the clock.
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

// liveBlockRow is -1 when the block is off screen, which makes the measurement vacuous.
func liveBlockRow(capture string) int {
	for i, ln := range strings.Split(strings.TrimRight(capture, "\n"), "\n") {
		if strings.Contains(ln, "bash for i in") {
			return i
		}
	}
	return -1
}

func contentHash(capture string, boundary int) (string, string) {
	rows := strings.Split(strings.TrimRight(capture, "\n"), "\n")
	if boundary < 0 || boundary > len(rows) {
		return "", ""
	}
	body := strings.Join(rows[:boundary], "\n")
	return fmt.Sprintf("%x", md5.Sum([]byte(body))), body
}

// useCopilotTerra edits only the smoke store's config copy; the case needs a
// model that runs one bash command verbatim and then stops.
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
	p.send("md5sum " + bin)
	p.key("Enter")

	p.startTurn("run exactly this bash command and nothing else, then say TICKSDONE: " +
		"for i in $(seq 1 90); do echo tick-$i; sleep 1; done")

	// A tool block is clipped to its last ten lines, so a streaming tool never
	// promotes the pager on its own.
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

	// One notch keeps the tail visible; further and nothing is compared to nothing.
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
