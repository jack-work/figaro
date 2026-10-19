package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/sdk/aria"
)

// A resize is the one event where the terminal reshapes its grid without the
// application: the grid changes before SIGWINCH is delivered, so the stream is
// split and tmux resizes between the halves, synchronized by marker files.

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
			const w1, h1 = 72, 40 // width only: no row moves

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
			client.Apply(aria.Page{Parts: committed}, aria.Notify)

			// Scroll into history so the viewport holds blank
			// separator rows.
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
			if blanks == 0 {
				t.Fatal("fixture no longer exercises its reason: no blank rows in the viewport before the resize")
			}

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

// tmuxReplayResize replays pre, resizes the pane, replays post: `ready` marks
// the first half written, `go` the resize complete.
func tmuxReplayResize(t *testing.T, dir, pre, post string, w0, h0, w1, h1 int) []string {
	t.Helper()
	socket := filepath.Join(dir, "tmux.sock")
	readyFile := filepath.Join(dir, "ready")
	goFile := filepath.Join(dir, "go")

	run := func(args ...string) (string, error) {
		cmd := exec.Command("tmux", append([]string{"-S", socket, "-f", "/dev/null"}, args...)...)
		cmd.Env = append(os.Environ(), "TMUX=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	// A returning shell prompt would land on the final row.
	script := "cat " + pre + "; touch " + readyFile +
		"; while [ ! -e " + goFile + " ]; do sleep 0.02; done; cat " + post + "; sleep 60"
	// With no user config, a detached session's window is exactly -x by -y:
	// the status row belongs to a client, and there is none.
	if out, err := run("new-session", "-d", "-x", itoaTest(w0), "-y", itoaTest(h0), "sh", "-c", script); err != nil {
		t.Skipf("tmux new-session failed (%v): %s", err, out)
	}
	// The pane's shell writes the ready marker into dir, on a skip as on a
	// pass. kill-server also leaves the socket inode behind.
	var panePID int
	defer func() {
		_, _ = run("kill-server")
		_ = os.Remove(socket)
		awaitPaneGone(t, panePID)
	}()
	panePID, _ = strconv.Atoi(strings.TrimSpace(mustOut(t, run, "display", "-p", "#{pane_pid}")))
	if out, err := run("set", "-g", "status", "off"); err != nil {
		t.Skipf("tmux set status off failed (%v): %s", err, out)
	}
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

	if out, err := run("resize-window", "-x", itoaTest(w1), "-y", itoaTest(h1)); err != nil {
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
