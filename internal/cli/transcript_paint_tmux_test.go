package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/sdk/aria"
)

// The pager's escape stream replayed into tmux must draw what the pager
// believes it drew. With autowrap off, writing the last column leaves the
// cursor on it, so an erase-to-end-of-line after the write wipes that cell.

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

// recordScrollSession returns the screen the pager believes it left and the byte count.
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
	client.Apply(aria.Page{Parts: committed}, aria.Notify)
	tr := newTranscript(f, 100, 40, &ariaView{settings: &renderSettings{}}, client, "aria0001", time.Unix(0, 0))
	tr.enter()
	for range 37 {
		tr.scrollBy(-1)
	}
	for range 9 {
		tr.scrollBy(2)
	}
	tr.scrollBy(-13) // too big to shift: the full-repaint fallback
	tr.matchQuery = "transcript"
	tr.render()
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

// tmuxReplay replays a frame stream into a private tmux pane of exactly w by h.
func tmuxReplay(t *testing.T, dir, path string, w, h int) []string {
	t.Helper()
	socket := filepath.Join(dir, "tmux.sock")
	run := func(args ...string) (string, error) {
		cmd := exec.Command("tmux", append([]string{"-S", socket, "-f", "/dev/null"}, args...)...)
		cmd.Env = append(os.Environ(), "TMUX=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	defer run("kill-server")

	// A returning shell prompt would land on the final row.
	if out, err := run("new-session", "-d", "-x", itoaTest(w), "-y", itoaTest(h),
		"sh", "-c", "cat "+path+"; sleep 60"); err != nil {
		t.Skipf("tmux new-session failed (%v): %s", err, out)
	}
	if out, err := run("set", "-g", "status", "off"); err != nil {
		t.Skipf("tmux set status off failed (%v): %s", err, out)
	}
	if out, err := run("display", "-p", "#{pane_height}"); err != nil || strings.TrimSpace(out) != itoaTest(h) {
		t.Skipf("pane height is %q, wanted %d: cannot assert against a geometry we did not get", strings.TrimSpace(out), h)
	}

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
