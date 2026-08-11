package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// ---------------------------------------------------------------------------
// THE RESIZE THAT A REAL TERMINAL PERFORMS.
//
// transcript_paint_tmux_test.go already replays the pager's escape stream into
// tmux and compares what tmux draws against what the pager believes it drew.
// That test is the reason the suffix-update erase-order bug was found, and its
// premise is exactly right. The one thing it never did was span a RESIZE, and
// a resize is the only event where the terminal changes its own grid without the
// application being involved. So the painter's belief and the screen can part
// company there, and no test in the repo could see it.
//
// The bug (user's words): "The gaps in between nodes are populated with text
// that shouldn't be there from some other line. It can only be cleared by moving
// the viewport such that the corrupt region is no longer visible, and then
// moving back to that area."
//
// This test differs from the VT case (transcript_resize_paint_test.go) in the
// one way that matters: THERE, I model what a terminal does to its grid on a
// width change; HERE, tmux does it, and I model nothing. If the two disagree,
// believe this one, a model only knows the bugs its author imagined.
//
// The escape stream is split in two and the resize happens BETWEEN the halves,
// in the real order a user produces: the terminal's grid changes first (it has
// already happened by the time SIGWINCH is delivered), and only then does the
// application repaint. Two marker files synchronize the replay with the resize,
// because a fixed sleep is a guess and this is the one instant that must be
// ordered correctly.
// ---------------------------------------------------------------------------

func TestTranscriptPaint_RealTerminalResize(t *testing.T) {
	if testing.Short() {
		t.Skip("replays through tmux; skipped under -short")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	for _, regions := range []bool{true, false} {
		name := "scroll-regions"
		if !regions {
			name = "full-repaint-fallback"
		}
		t.Run(name, func(t *testing.T) {
			defer func(v bool) { transcriptScrollRegions = v }(transcriptScrollRegions)
			transcriptScrollRegions = regions

			const w0, h0 = 100, 40
			const w1, h1 = 72, 40 // WIDTH ONLY: no row moves, so nothing but the
			// paint diff can explain a difference. Measured
			// as sufficient in a real pane (skills/figaro/contributing/paint-repro.md §8.1).

			dir := t.TempDir()
			preFile := filepath.Join(dir, "pre.bin")
			postFile := filepath.Join(dir, "post.bin")

			pre, err := os.Create(preFile)
			if err != nil {
				t.Fatal(err)
			}
			client := aria.NewClient()
			client.SetClosedLimit(transcriptTailLimit)
			committed := make([]aria.TurnPart, 14)
			for i := range committed {
				committed[i] = aria.TurnPart{Turn: aria.Turn{
					ID: uint64(i + 1), Sealed: true, Nodes: heavyNodes(i+1, 14),
				}}
			}
			client.Apply(aria.Page{Parts: committed})

			// --- first half: come up at w0xh0 and scroll into history, so the
			// viewport is full of message separators (a blank row, then a rule).
			tr := newTranscript(pre, w0, h0, &ariaView{settings: &renderSettings{}}, client, "aria0001", time.Unix(0, 0))
			tr.enter()
			for range 30 {
				tr.scrollBy(-1)
			}
			blanks := 0
			for _, row := range tr.prev {
				if visibleText(row) == "" {
					blanks++
				}
			}
			if err := pre.Close(); err != nil {
				t.Fatal(err)
			}
			// If the viewport holds no blank rows the test has stopped
			// exercising its own stated reason and can no longer fail for it.
			if blanks == 0 {
				t.Fatal("fixture no longer exercises its reason: no blank rows in the viewport before the resize")
			}

			// --- second half: the application is told about the new size and
			// repaints. Everything it emits from here lands after tmux has
			// already reshaped the grid.
			post, err := os.Create(postFile)
			if err != nil {
				t.Fatal(err)
			}
			tr.out = post
			tr.resize(w1, h1)
			if err := post.Close(); err != nil {
				t.Fatal(err)
			}

			want := make([]string, len(tr.prev))
			for i, row := range tr.prev {
				want[i] = strings.TrimRight(visibleText(row), " ")
			}
			t.Logf("%d intentionally-blank rows before the resize; comparing %d rows at %dx%d",
				blanks, len(want), w1, h1)

			got := tmuxReplayResize(t, dir, preFile, postFile, w0, h0, w1, h1)

			bad := 0
			for r := range want {
				if r >= len(got) || got[r] != want[r] {
					bad++
					if bad <= 4 {
						shown := ""
						if r < len(got) {
							shown = got[r]
						}
						kind := "STALE TEXT"
						if want[r] != "" {
							kind = "wrong content"
						}
						t.Errorf("row %d [%s]\n  t.prev claims: %q\n  tmux shows:    %q", r, kind, want[r], shown)
					}
				}
			}
			if bad > 0 {
				t.Fatalf("%d of %d rows disagree with t.prev after a real %dx%d -> %dx%d resize",
					bad, len(want), w0, h0, w1, h1)
			}
		})
	}
}

// tmuxReplayResize replays pre, lets tmux resize the pane for real, replays
// post, and returns what the pane shows.
//
// Synchronization is by marker file, not by sleeping: the whole point is that
// the resize lands strictly between the two halves. `ready` says the first half
// has been written; `go` says the resize is complete and the second half may
// proceed.
func tmuxReplayResize(t *testing.T, dir, pre, post string, w0, h0, w1, h1 int) []string {
	t.Helper()
	socket := filepath.Join(dir, "tmux.sock")
	readyFile := filepath.Join(dir, "ready")
	goFile := filepath.Join(dir, "go")

	run := func(args ...string) (string, error) {
		cmd := exec.Command("tmux", append([]string{"-S", socket}, args...)...)
		cmd.Env = append(os.Environ(), "TMUX=") // never inherit an outer session
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	// kill-server leaves the socket INODE behind, so a later `[ -S sock ]` would
	// call a dead server alive. Remove the file too: the artifact outliving the
	// process is the same family of trap as a scratch daemon outliving its pane.
	defer func() {
		_, _ = run("kill-server")
		_ = os.Remove(socket)
	}()

	// `sleep` keeps the pane alive after the replay: once the shell prompt
	// returns it lands on top of the final row and corrupts the comparison.
	script := "cat " + pre + "; touch " + readyFile +
		"; while [ ! -e " + goFile + " ]; do sleep 0.02; done; cat " + post + "; sleep 60"
	// -y h+1: tmux subtracts the status row AT CREATION and turning the status
	// bar off afterwards does not give it back to a detached session.
	if out, err := run("new-session", "-d", "-x", itoaTest(w0), "-y", itoaTest(h0+1), "sh", "-c", script); err != nil {
		t.Skipf("tmux new-session failed (%v): %s", err, out)
	}
	if out, err := run("set", "-g", "status", "off"); err != nil {
		t.Skipf("tmux set status off failed (%v): %s", err, out)
	}
	// Read the height back and assert it, rather than trusting the +1. An entire
	// investigation was once conducted at pane height zero.
	if got := strings.TrimSpace(mustOut(t, run, "display", "-p", "#{pane_height}")); got != itoaTest(h0) {
		t.Skipf("pane height is %s, wanted %d: cannot assert against a geometry we did not get", got, h0)
	}

	waitFor := func(path string, what string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(path); err == nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Skipf("timed out waiting for %s", what)
	}
	waitFor(readyFile, "the pre-resize stream to finish replaying")

	// THE REAL RESIZE. tmux reshapes its own grid here; nothing figaro wrote is
	// involved, and no model of terminal behaviour is being trusted.
	if out, err := run("resize-window", "-x", itoaTest(w1), "-y", itoaTest(h1+1)); err != nil {
		t.Skipf("tmux resize-window failed (%v): %s", err, out)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		wOK := strings.TrimSpace(mustOut(t, run, "display", "-p", "#{pane_width}")) == itoaTest(w1)
		hOK := strings.TrimSpace(mustOut(t, run, "display", "-p", "#{pane_height}")) == itoaTest(h1)
		if wOK && hOK {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if got := strings.TrimSpace(mustOut(t, run, "display", "-p", "#{pane_width}x#{pane_height}")); got != itoaTest(w1)+"x"+itoaTest(h1) {
		t.Skipf("pane is %s after resize, wanted %dx%d", got, w1, h1)
	}
	if err := os.WriteFile(goFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// Poll until the pane stops changing; replay is asynchronous.
	var last string
	stable := 0
	deadline = time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(150 * time.Millisecond)
		out, err := run("capture-pane", "-p")
		if err != nil {
			t.Skipf("tmux capture-pane failed (%v): %s", err, out)
		}
		if out == last && strings.TrimSpace(out) != "" {
			if stable++; stable == 3 {
				break
			}
		} else {
			stable = 0
		}
		last = out
	}
	if strings.TrimSpace(last) == "" {
		t.Skip("tmux pane never produced output")
	}
	rows := strings.Split(strings.TrimRight(last, "\n"), "\n")
	for i := range rows {
		rows[i] = strings.TrimRight(rows[i], " ")
	}
	for len(rows) < h1 {
		rows = append(rows, "")
	}
	return rows[:h1]
}

func mustOut(t *testing.T, run func(...string) (string, error), args ...string) string {
	t.Helper()
	out, err := run(args...)
	if err != nil {
		t.Skipf("tmux %v failed (%v): %s", args, err, out)
	}
	return out
}
