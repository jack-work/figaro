package cli

// ---------------------------------------------------------------------------
// THE INVISIBLE STEER, in a real terminal.
//
// REPRODUCED before it was fixed, in a pty, and root-caused: a prompt sent to a
// BUSY figaro is classified by the DRAIN as a steer, and appendUserPrompt
// RETURNS BEFORE OpenInquiry when it is steering — so a steer broadcasts
// NOTHING at submit. It becomes visible only when the projection emits a
// steering node at the next ROUND BOUNDARY. Watched behind a `sleep 45`:
//
//	t+0    accepted, in the inbox, INVISIBLE on every surface
//	t+1.2s pager auto-entered, history renders fine, the message is nowhere
//	t+3s   Q -> "queued prompts / 1. SECONDMESSAGE please acknowledge"
//	t+45s  round boundary -> the steering node finally appears
//
// The text was accepted and reachable through a panel, and absent from the
// transcript for as long as the tool took. The user could not tell "accepted,
// waiting" from "dropped".
//
// This case is the A/B. It runs the WHOLE scenario against a binary named by
// absolute path, and asserts the echo is on screen within seconds of submit.
// Point FIGARO_PHASE3_CONTROL_BIN at a build from before this phase and the
// control arm runs too, asserting the bug is THERE — two arms, one scenario,
// so a pass cannot be an artefact of the fixture.
// ---------------------------------------------------------------------------

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// steerEcho is what one arm observed.
type steerEcho struct {
	atSubmit   string // pane B, ~2s after submit, tool still running
	afterRound string // pane B, after the turn settled
	toolRan    bool   // the tool was still running when we looked
}

// runSteerEcho drives the scenario with one binary and returns the captures.
func runSteerEcho(t *testing.T, bin string) steerEcho {
	t.Helper()
	env := smokeStore(t)

	// Pane A opens a turn whose first tool takes far longer than any round
	// boundary, so the window under test is unambiguous.
	a := newPane(t, env, bin, 100, 40)
	a.startTurn("run this exact bash command and nothing else: sleep 45 ; then reply DONE45")

	id := waitForAria(t, env, bin, 60*time.Second)
	if id == "" {
		t.Skip("no aria appeared; provider unavailable")
	}
	// Wait for the TOOL to be running: a steer that lands before the first tool
	// has nothing to be invisible behind.
	if !waitForRunningTool(a, 90*time.Second) {
		t.Skipf("no running tool appeared in pane A:\n%s", a.visible())
	}

	// Pane B is the submit under test: a second `figaro send` to a BUSY aria.
	b := newPane(t, env, bin, 100, 40)
	b.send(bin + " send --id " + id + " -- 'SECONDMESSAGE please acknowledge'")
	b.key("Enter")
	time.Sleep(4 * time.Second) // no round boundary can have happened yet

	out := steerEcho{atSubmit: b.scrollback(), toolRan: strings.Contains(a.visible(), "sleep 45")}
	b.waitIdle(150 * time.Second)
	out.afterRound = b.scrollback()
	return out
}

// waitForAria polls the isolated store until an aria exists, and returns its id.
func waitForAria(t *testing.T, env []string, bin string, d time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		cmd := exec.Command(bin, "list", "-j")
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		var rows []struct {
			ID    string `json:"id"`
			Kind  string `json:"kind"`
			State string `json:"state"`
		}
		if json.Unmarshal(out, &rows) != nil {
			continue
		}
		for _, r := range rows {
			if r.ID != "" {
				return r.ID
			}
		}
	}
	return ""
}

// waitForRunningTool polls pane A until the sleep is visibly in flight.
func waitForRunningTool(p *pane, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		if strings.Contains(p.visible(), "sleep 45") {
			return true
		}
	}
	return false
}

func TestSmoke_SubmittedPromptIsVisibleBeforeTheRoundBoundary(t *testing.T) {
	smokeEnabled(t)
	bin := smokeBinary(t)
	t.Logf("EXPERIMENT binary: %s (%s)", bin, md5Of(t, bin))

	got := runSteerEcho(t, bin)
	if !got.toolRan {
		t.Skip("the tool had already finished when the prompt was submitted; nothing to be invisible behind")
	}

	// NON-EMPTY FIRST. Two empty captures compare clean and pass; asserting on
	// a blank pane is how a broken fixture certifies a broken build.
	if strings.TrimSpace(got.atSubmit) == "" {
		t.Fatal("pane B captured nothing at submit — the fixture, not the build")
	}
	if n := echoLines(got.atSubmit, "SECONDMESSAGE please acknowledge"); n != 1 {
		t.Fatalf("the submitted prompt appears on %d body lines %ds after submit, want 1 — "+
			"this is the invisible steer:\n%s", n, 4, got.atSubmit)
	}
	if !strings.Contains(got.atSubmit, "↳ queued") {
		t.Errorf("the echo is unmarked; a reader cannot tell it from placed content:\n%s", got.atSubmit)
	}

	// AND IT RESOLVES: once the round boundary passes, the steer is a node at
	// its coordinate and the echo is gone. Once, not twice.
	if n := echoLines(got.afterRound, "SECONDMESSAGE please acknowledge"); n != 1 {
		t.Errorf("after the round boundary the prompt appears on %d body lines, want exactly 1 "+
			"(the echo must resolve INTO the node, not beside it):\n%s", n, tail(got.afterRound, 60))
	}
	if strings.Contains(lastFrame(got.afterRound), "↳ queued") {
		t.Errorf("the queued marker survived the round boundary:\n%s", tail(got.afterRound, 40))
	}
}

// The CONTROL arm: the same scenario against a pre-phase-3 build, asserting the
// bug is there. Two arms that produce identical output are more often one
// binary than one bug (trap 11), so this exists to prove the arms differ.
func TestSmoke_ControlSteerIsInvisibleUntilTheRoundBoundary(t *testing.T) {
	smokeEnabled(t)
	bin := os.Getenv("FIGARO_PHASE3_CONTROL_BIN")
	if bin == "" {
		t.Skip("set FIGARO_PHASE3_CONTROL_BIN to a pre-phase-3 build to run the control arm")
	}
	t.Logf("CONTROL binary: %s (%s)", bin, md5Of(t, bin))

	got := runSteerEcho(t, bin)
	if !got.toolRan {
		t.Skip("the tool had already finished when the prompt was submitted")
	}
	if strings.TrimSpace(got.atSubmit) == "" {
		t.Fatal("pane B captured nothing at submit — the fixture, not the build")
	}
	if n := echoLines(got.atSubmit, "SECONDMESSAGE please acknowledge"); n != 0 {
		t.Fatalf("the control arm SHOWED the prompt (%d body lines) — either the control "+
			"binary is not pre-phase-3, or both arms are the same build:\n%s", n, got.atSubmit)
	}
	// The bug, stated: it is only the transcript that is missing it. The prompt
	// really was accepted, and really does arrive at the round boundary.
	if n := echoLines(got.afterRound, "SECONDMESSAGE please acknowledge"); n < 1 {
		t.Errorf("the control arm never showed the prompt at all (%d body lines) — the "+
			"scenario did not run as described:\n%s", n, tail(got.afterRound, 60))
	}
}

// echoLines counts occurrences of tok as a rendered PROSE body line, allowing
// for the blockquote gutter both an echo and a placed steer are drawn under
// ("  │ SECONDMESSAGE …"). bodyLines' exact-equality rule is sound for a bare
// reply and unsound here: the control arm's first run reported "never showed
// the prompt at all" while the steer was plainly on screen behind its gutter.
func echoLines(capture, tok string) int {
	n := 0
	for _, ln := range strings.Split(capture, "\n") {
		ln = strings.TrimSpace(ln)
		ln = strings.TrimSpace(strings.TrimPrefix(ln, "│"))
		if ln == tok {
			n++
		}
	}
	return n
}

func md5Of(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command("md5sum", path).Output()
	if err != nil {
		return "md5 unavailable"
	}
	return strings.Fields(string(out))[0]
}

// tail is the last n lines of a capture, for failure messages.
func tail(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// lastFrame is the visible tail of a scrollback capture: what is on screen at
// the end, rather than everything that ever was. A marker that existed for one
// frame in the middle of a scrollback is not a marker that survived.
func lastFrame(s string) string { return tail(s, 40) }
