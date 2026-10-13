//go:build tmux

// End-to-end terminal tests against a real provider; FIGARO_E2E_BIN reuses a build.
//
//	go test -tags tmux ./internal/cli/ -run TestE2E -v
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type pane struct {
	t    *testing.T
	sess string
	dir  string
	bin  string
}

func tmuxRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "no server running") {
		t.Fatalf("tmux %v: %v: %s", args, err, out)
	}
	return string(out)
}

// `tmux new-session -y N` gives pane height N-1: assert pane_height, never assume -y.
func newPane(t *testing.T, cols, rows int) *pane {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	bin := os.Getenv("FIGARO_E2E_BIN")
	if bin == "" {
		bin = filepath.Join(t.TempDir(), "figaro")
		build := exec.Command("go", "build", "-o", bin, "../../cmd/figaro")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build figaro: %v: %s", err, out)
		}
	}
	dir := t.TempDir()
	for _, s := range []string{"state", "run", "config"} {
		if err := os.MkdirAll(filepath.Join(dir, s), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		src := filepath.Join(home, ".config", "figaro")
		_ = exec.Command("cp", "-r", src+"/.", filepath.Join(dir, "config")).Run()
	}

	p := &pane{t: t, sess: fmt.Sprintf("figaro-e2e-%d", time.Now().UnixNano()), dir: dir, bin: bin}
	env := fmt.Sprintf("env FIGARO_STATE_DIR=%s/state FIGARO_RUNTIME_DIR=%s/run FIGARO_CONFIG_DIR=%s/config bash --norc",
		dir, dir, dir)
	tmuxRun(t, "new-session", "-d", "-s", p.sess, "-x", fmt.Sprint(cols), "-y", fmt.Sprint(rows), env)
	t.Cleanup(func() {
		tmuxRun(t, "kill-session", "-t", p.sess)
		// kill-session leaves the isolated daemon running.
		_ = exec.Command(p.bin, "stop").Run()
	})
	time.Sleep(500 * time.Millisecond)
	return p
}

func (p *pane) height() int {
	out := strings.TrimSpace(tmuxRun(p.t, "display", "-p", "-t", p.sess, "#{pane_height}"))
	var h int
	fmt.Sscan(out, &h)
	return h
}

func (p *pane) send(keys ...string) {
	p.t.Helper()
	tmuxRun(p.t, append([]string{"send-keys", "-t", p.sess}, keys...)...)
}

// typeText sends one character per read, the way a human types.
func (p *pane) typeText(s string) {
	p.t.Helper()
	for _, r := range s {
		tmuxRun(p.t, "send-keys", "-t", p.sess, "-l", string(r))
		time.Sleep(120 * time.Millisecond)
	}
}

func (p *pane) run(prompt string) {
	p.t.Helper()
	p.send(fmt.Sprintf("%s send -- %q", p.bin, prompt), "Enter")
}

// scrollback keeps frames that scrolled off the pane.
func (p *pane) scrollback() string {
	return tmuxRun(p.t, "capture-pane", "-t", p.sess, "-p", "-S", "-")
}

func (p *pane) visible() string {
	return tmuxRun(p.t, "capture-pane", "-t", p.sess, "-p")
}

func (p *pane) command() string {
	return strings.TrimSpace(tmuxRun(p.t, "list-panes", "-t", p.sess, "-F", "#{pane_current_command}"))
}

func (p *pane) waitExit(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if c := p.command(); c == "bash" || c == "sh" {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// inPager: an absence inside a pager is not an absence.
func (p *pane) inPager() bool {
	v := p.visible()
	return strings.Contains(v, "? help") || strings.Contains(v, "! status")
}

func TestE2E_SimpleTurnExitsWithOneFooter(t *testing.T) {
	p := newPane(t, 100, 30)
	if h := p.height(); h != 29 {
		t.Fatalf("pane_height = %d, want 29 (tmux -y 30 gives 29: the status bar takes a row)", h)
	}
	p.run("test")

	if !p.waitExit(90 * time.Second) {
		t.Fatalf("figaro never exited on its own (pane still running %q).\n"+
			"A non-reentrant mutex locked twice on the turn-done path wedges the\n"+
			"notify pump AND the input loop, so Ctrl-C/Ctrl-D/q/Esc are all dead.\n---\n%s",
			p.command(), p.scrollback())
	}
	scr := p.scrollback()
	if got := strings.Count(scr, "aria "); got != 1 {
		t.Errorf("status footer appears %d times, want exactly 1\n---\n%s", got, scr)
	}
	if !strings.Contains(scr, "> input") {
		t.Errorf("prompt block missing its input header\n---\n%s", scr)
	}
}

func TestE2E_ExitKeysWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []string
	}{
		{"ctrl-c", []string{"C-c"}},
		{"ctrl-d", []string{"C-d"}},
		{"pager-q", []string{"C-t"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPane(t, 100, 30)
			p.run("use bash to sleep 25 then say ACC")
			time.Sleep(12 * time.Second)
			p.send(tc.keys...)
			if tc.name == "pager-q" {
				time.Sleep(3 * time.Second)
				p.send("q")
			}
			if !p.waitExit(45 * time.Second) {
				t.Fatalf("%s did not end the process (still %q)\n---\n%s",
					tc.name, p.command(), p.scrollback())
			}
		})
	}
}

// Typing without a trigger key composes; pager letters must not swallow it.
func TestE2E_NaiveTypingComposesASteer(t *testing.T) {
	p := newPane(t, 100, 40)
	p.run("use bash to sleep 30 then say NAIVE")
	time.Sleep(12 * time.Second)
	p.typeText("just dig quickly")
	if p.inPager() {
		t.Fatalf("typing opened the pager, a printable character was treated as a motion\n---\n%s", p.visible())
	}
	if v := p.visible(); !strings.Contains(v, "just dig quickly") {
		t.Fatalf("draft does not hold the full text\n---\n%s", v)
	}
	p.send("Enter")
	_ = p.waitExit(90 * time.Second)
	if scr := p.scrollback(); !strings.Contains(scr, "↳ input") {
		t.Errorf("steer did not render as a steering node\n---\n%s", scr)
	}
}
