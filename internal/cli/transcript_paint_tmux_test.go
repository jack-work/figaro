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
// Replay into a real terminal.
//
// vtScreen is a model, and a model only knows the bugs its author thought of.
// This test replays the pager's actual escape stream into tmux — a real,
// conservative VT implementation — and compares what it draws against what the
// pager believes it drew.
//
// It has already earned its place. The suffix-update path originally emitted
// the tail and THEN erased to end of line; because the pager runs with autowrap
// off, writing the last column leaves the cursor ON it, so that erase wiped the
// character just written. The model at the time let the cursor run past the
// margin and saw nothing wrong. tmux disagreed, and was right.
//
// Skipped when tmux is unavailable or under -short.
// ---------------------------------------------------------------------------

func TestTranscriptPaint_RealTerminalReplay(t *testing.T) {
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

			dir := t.TempDir()
			stream := filepath.Join(dir, "frames.bin")
			want, bytes := recordScrollSession(t, stream)
			got := tmuxReplay(t, dir, stream, 100, 40)
			if len(got) != len(want) {
				t.Fatalf("tmux drew %d rows, want %d", len(got), len(want))
			}
			for r := range want {
				if got[r] != want[r] {
					t.Fatalf("row %d differs after replaying %d bytes\n want: %q\n  got: %q",
						r, bytes, want[r], got[r])
				}
			}
			t.Logf("%d bytes replayed, %d rows identical", bytes, len(want))
		})
	}
}

// recordScrollSession writes a scripted pager session's escape stream to path
// and returns the screen the pager believes it left, plus the byte count.
func recordScrollSession(t *testing.T, path string) ([]string, int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	committed := make([]aria.TurnPart, 12)
	for i := range committed {
		committed[i] = aria.TurnPart{Turn: aria.Turn{ID: uint64(i + 1), Sealed: true, Nodes: heavyNodes(i+1, 15)}}
	}
	client.Apply(aria.Page{Parts: committed})
	tr := newTranscript(f, 100, 40, &ariaView{settings: &renderSettings{}}, client, "aria0001", time.Unix(0, 0))
	tr.enter()
	for range 37 { // long climb: every step is a one-row shift
		tr.scrollBy(-1)
	}
	for range 9 { // back down in twos
		tr.scrollBy(2)
	}
	tr.scrollBy(-13) // a jump too big to shift: exercises the fallback
	tr.matchQuery = "transcript"
	tr.render() // reverse-video highlights
	tr.scrollBy(-1)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]string, len(tr.prev))
	for i, row := range tr.prev {
		rows[i] = strings.TrimRight(visibleText(row), " ")
	}
	return rows, int(info.Size())
}

// visibleText strips ANSI escapes, leaving the characters a viewer sees.
func visibleText(row string) string {
	var b strings.Builder
	for i := 0; i < len(row); {
		if row[i] == '\x1b' {
			i = skipANSI(row, i)
			continue
		}
		b.WriteByte(row[i])
		i++
	}
	return b.String()
}

// tmuxReplay cats path inside a tmux pane of the given size and returns what
// the pane shows. The pane is one row taller than requested and the status line
// is switched off, which is how tmux ends up with exactly h usable rows.
func tmuxReplay(t *testing.T, dir, path string, w, h int) []string {
	t.Helper()
	socket := filepath.Join(dir, "tmux.sock")
	run := func(args ...string) (string, error) {
		cmd := exec.Command("tmux", append([]string{"-S", socket}, args...)...)
		cmd.Env = append(os.Environ(), "TMUX=") // never inherit an outer session
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	defer run("kill-server")

	// `sleep` keeps the pane alive after the replay: once the shell prompt
	// returns it lands on top of the final row and corrupts the comparison.
	if out, err := run("new-session", "-d", "-x", itoaTest(w), "-y", itoaTest(h+1),
		"sh", "-c", "cat "+path+"; sleep 60"); err != nil {
		t.Skipf("tmux new-session failed (%v): %s", err, out)
	}
	if out, err := run("set", "-g", "status", "off"); err != nil {
		t.Skipf("tmux set status off failed (%v): %s", err, out)
	}

	// Poll until the pane stops changing: replay is asynchronous.
	var last string
	stable := 0
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(150 * time.Millisecond)
		out, err := run("capture-pane", "-p")
		if err != nil {
			t.Skipf("tmux capture-pane failed (%v): %s", err, out)
		}
		if out == last && strings.TrimSpace(out) != "" {
			if stable++; stable == 2 {
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
	for len(rows) < h {
		rows = append(rows, "")
	}
	return rows[:h]
}
