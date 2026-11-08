//go:build linux

package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	resizeShrinkAt = 298
	resizeGrowAt   = 304
	resizeLast     = 660
)

// The gen6 stage manager's case: a fork plate of gen5 t34 at 48x14, shrunk to
// 32x9 while the turn streams, grown back a few steps later.
func resizeScript() []string {
	var cmds []string
	step := func(from, to int) {
		for n := from; n <= to; n++ {
			cmds = append(cmds, fmt.Sprintf("step %d", n))
		}
	}
	step(0, resizeShrinkAt)
	cmds = append(cmds, "refuse resize 19 9", "refuse resize 32 4", "refuse resize 193 14",
		"refuse resize 48 57", "refuse resize x 9", "resize 32 9")
	step(resizeShrinkAt+1, resizeGrowAt)
	cmds = append(cmds, "resize 48 14")
	step(resizeGrowAt+1, resizeLast)
	return cmds
}

// A resize on the wire is a terminal resize at a step boundary: the redraw
// it answers with repaints every cell of the new grid, so nothing of the old
// layout survives, and the same script gives the same bytes on every run.
func TestALockstepResizeRedrawsTheWholeGridAtTheNewSize(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the binary in a pty and tmux; skipped under -short")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	path, err := filepath.Abs(filepath.Join("testdata", "tapes", "fork-almaviva.tape"))
	if err != nil {
		t.Fatal(err)
	}
	bin := lockstepBinary(t)
	cmds := resizeScript()
	a := lockstepScript(t, bin, path, cmds, []string{"GOMAXPROCS=1"}, 0)
	b := lockstepScript(t, bin, path, cmds, []string{"GOMAXPROCS=8"}, 70*time.Millisecond)
	for i := range cmds {
		if !bytes.Equal(a.steps[i], b.steps[i]) {
			t.Fatalf("%s differs between runs:\n  %q\n  %q", cmds[i], clip(a.steps[i]), clip(b.steps[i]))
		}
	}

	shrink := indexOf(cmds, "resize 32 9")
	grow := indexOf(cmds, "resize 48 14")
	screens := tmuxScreens(t, 48, 14, []tmuxPhase{
		{data: bytes.Join(a.steps[:shrink], nil)},
		{cols: 32, rows: 9, data: a.steps[shrink]},
		{data: bytes.Join(a.steps[shrink+1:grow], nil)},
		{cols: 48, rows: 14, data: a.steps[grow]},
		{data: bytes.Join(a.steps[grow+1:], nil)},
	})
	before, small, smallLater, regrown, end := screens[0], screens[1], screens[2], screens[3], screens[4]

	for _, s := range []struct {
		name   string
		screen []string
	}{{"32x9 after the shrink", small}, {"48x14 after the regrow", regrown}} {
		for r, row := range s.screen {
			if strings.ContainsRune(row, tmuxStale) {
				t.Errorf("%s: row %d keeps cells the redraw never painted: %q\n%s", s.name, r, row, strings.Join(s.screen, "\n"))
				break
			}
		}
	}
	if small[len(small)-1] == "" || before[len(before)-1] == "" {
		t.Fatalf("no footer on the bottom row:\nbefore:\n%s\nafter:\n%s", strings.Join(before, "\n"), strings.Join(small, "\n"))
	}
	if strings.Join(small, "\n") == strings.Join(smallLater, "\n") {
		t.Errorf("the turn stopped drawing at 32x9:\n%s", strings.Join(small, "\n"))
	}
	c := lockstepDrive(t, bin, path, resizeLast+1, nil, 0)
	plain := tmuxScreens(t, 48, 14, []tmuxPhase{{data: bytes.Join(c.steps, nil)}})[0]
	if strings.Join(end, "\n") != strings.Join(plain, "\n") {
		t.Errorf("after a round trip through 32x9 the replay ends unlike one never resized:\nresized:\n%s\nnever resized:\n%s",
			strings.Join(end, "\n"), strings.Join(plain, "\n"))
	}
	t.Logf("48x14 before:\n%s\n32x9:\n%s\n48x14 again:\n%s", strings.Join(before, "\n"), strings.Join(small, "\n"), strings.Join(regrown, "\n"))
}

func indexOf(cmds []string, want string) int {
	for i, c := range cmds {
		if c == want {
			return i
		}
	}
	panic("no " + want + " in the script")
}

const tmuxStale = 'ǂ'

type tmuxPhase struct {
	cols, rows int
	data       []byte
}

// tmuxScreens plays the phases into one tmux pane and captures the screen
// after each. A phase with a size resizes the pane first, then paints every
// cell with tmuxStale before its bytes: whatever the bytes do not repaint
// shows through.
func tmuxScreens(t *testing.T, cols, rows int, phases []tmuxPhase) [][]string {
	t.Helper()
	dir := t.TempDir()
	for i, p := range phases {
		if bytes.ContainsRune(p.data, tmuxStale) {
			t.Fatalf("phase %d draws %q itself: pick another stale marker", i, tmuxStale)
		}
		data := p.data
		if p.cols > 0 {
			var fill bytes.Buffer
			fill.WriteString("\x1b[0m")
			for r := 1; r <= p.rows; r++ {
				fmt.Fprintf(&fill, "\x1b[%d;1H%s", r, strings.Repeat(string(tmuxStale), p.cols))
			}
			data = append(fill.Bytes(), data...)
		}
		if err := os.WriteFile(filepath.Join(dir, "phase"+strconv.Itoa(i)), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(dir, "tmux.sock")
	run := func(args ...string) (string, error) {
		cmd := exec.Command("tmux", append([]string{"-S", socket, "-f", "/dev/null"}, args...)...)
		cmd.Env = append(os.Environ(), "TMUX=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	script := fmt.Sprintf("stty raw -echo; i=0; while [ $i -lt %d ]; do while [ ! -e %s/go$i ]; do sleep 0.02; done; cat %s/phase$i; touch %s/done$i; i=$((i+1)); done; sleep 600",
		len(phases), dir, dir, dir)
	if out, err := run("new-session", "-d", "-x", itoaTest(cols), "-y", itoaTest(rows), "sh", "-c", script); err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, out)
	}
	var panePID int
	defer func() {
		_, _ = run("kill-server")
		_ = os.Remove(socket)
		awaitPaneGone(t, panePID)
	}()
	panePID, _ = strconv.Atoi(strings.TrimSpace(mustOut(t, run, "display", "-p", "#{pane_pid}")))
	mustOut(t, run, "set", "-g", "status", "off")

	size := func() string {
		return strings.TrimSpace(mustOut(t, run, "display", "-p", "#{pane_width}x#{pane_height}"))
	}
	await := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("tmux: timed out waiting for %s", what)
			}
		}
	}
	var screens [][]string
	for i, p := range phases {
		if p.cols > 0 {
			want := itoaTest(p.cols) + "x" + itoaTest(p.rows)
			mustOut(t, run, "resize-window", "-x", itoaTest(p.cols), "-y", itoaTest(p.rows))
			await("the pane at "+want, func() bool { return size() == want })
			cols, rows = p.cols, p.rows
		}
		if err := os.WriteFile(filepath.Join(dir, "go"+strconv.Itoa(i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		await("phase "+strconv.Itoa(i)+" written", func() bool {
			_, err := os.Stat(filepath.Join(dir, "done"+strconv.Itoa(i)))
			return err == nil
		})
		var last string
		stable := 0
		await("phase "+strconv.Itoa(i)+" to settle on screen", func() bool {
			out := mustOut(t, run, "capture-pane", "-p")
			if out == last {
				stable++
			} else {
				stable = 0
			}
			last = out
			time.Sleep(50 * time.Millisecond)
			return stable >= 3
		})
		screen := strings.Split(strings.TrimRight(last, "\n"), "\n")
		for len(screen) < rows {
			screen = append(screen, "")
		}
		for r := range screen {
			screen[r] = strings.TrimRight(screen[r], " ")
		}
		screens = append(screens, screen[:rows])
	}
	return screens
}
