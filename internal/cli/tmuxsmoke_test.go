package cli

// Smoke harness: the real binary in a real pty against a real provider. Run with
// FIGARO_TMUX_SMOKE=1 and -count=1; method and traps in skills/tmux-testing.md.

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Participation ledger: a skip prints nothing without -v, so a run in which
// every case declined on model weather is otherwise indistinguishable from a pass.
var (
	smokeLedgerMu sync.Mutex
	smokeRan      []string
	smokeDeclined []string
	smokeReasons  = map[string]string{}
	smokeFloor    int
)

// Stamped into the summary so a transcript replayed from the go test cache confesses.
var smokeStart = time.Now()

func envFloor() int {
	if v := os.Getenv("FIGARO_TMUX_SMOKE_MIN_RAN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

func smokeCase(t *testing.T) {
	t.Helper()
	// Read inside a test, not in TestMain: only env consulted while tests run
	// enters the go test cache key, so a changed floor forces a real run.
	smokeLedgerMu.Lock()
	smokeFloor = envFloor()
	smokeLedgerMu.Unlock()
	t.Cleanup(func() {
		smokeLedgerMu.Lock()
		defer smokeLedgerMu.Unlock()
		if t.Skipped() {
			why := smokeReasons[t.Name()]
			if why == "" {
				why = "(reason not recorded: a raw t.Skip; use decline)"
			}
			smokeDeclined = append(smokeDeclined, t.Name()+" — "+firstLine(why))
			return
		}
		smokeRan = append(smokeRan, t.Name())
	})
}

func decline(t *testing.T, format string, args ...any) {
	t.Helper()
	smokeLedgerMu.Lock()
	smokeReasons[t.Name()] = fmt.Sprintf(format, args...)
	smokeLedgerMu.Unlock()
	t.Skipf(format, args...)
}

// Called after m.Run(): the last case's Cleanup has not run before then.
func smokeParticipation() bool {
	smokeLedgerMu.Lock()
	defer smokeLedgerMu.Unlock()
	if len(smokeRan)+len(smokeDeclined) == 0 {
		return true
	}
	fmt.Fprintf(os.Stderr, "\nsmoke participation: %d ran, %d declined\n",
		len(smokeRan), len(smokeDeclined))
	fmt.Fprintf(os.Stderr,
		"  run started %s (pid %d) -- IF THAT IS NOT JUST NOW, THIS TRANSCRIPT WAS\n"+
			"  REPLAYED FROM CACHE and no terminal, daemon or provider was touched. Use -count=1.\n",
		smokeStart.Format(time.RFC3339), os.Getpid())
	for _, d := range smokeDeclined {
		fmt.Fprintf(os.Stderr, "  DECLINED  %s\n", d)
	}
	for _, r := range smokeRan {
		fmt.Fprintf(os.Stderr, "  ran       %s\n", r)
	}
	min := smokeFloor
	if len(smokeRan) < min {
		fmt.Fprintf(os.Stderr,
			"smoke participation FLOOR NOT MET: %d ran, FIGARO_TMUX_SMOKE_MIN_RAN=%d\n",
			len(smokeRan), min)
		return false
	}
	return true
}

func smokeEnabled(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("drives a real terminal and a real provider; skipped under -short")
	}
	if os.Getenv("FIGARO_TMUX_SMOKE") == "" {
		t.Skip("set FIGARO_TMUX_SMOKE=1 to run (drives a real provider: costs tokens)")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
}

// smokeBinary builds figaro from this worktree; FIGARO_SMOKE_BIN overrides it.
func smokeBinary(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("FIGARO_SMOKE_BIN"); bin != "" {
		out, _ := exec.Command(bin, "--version").CombinedOutput()
		t.Logf("FIGARO_SMOKE_BIN in use: identity with this worktree is NOT guaranteed: %s",
			strings.TrimSpace(firstLine(string(out))))
		return bin
	}
	bin := filepath.Join(t.TempDir(), "figaro")
	rev, _ := exec.Command("git", "rev-parse", "--short=12", "HEAD").Output()
	ld := fmt.Sprintf("-X github.com/jack-work/figaro/internal/boot.commit=%s",
		strings.TrimSpace(string(rev)))
	cmd := exec.Command("go", "build", "-ldflags", ld, "-o", bin, "./cmd/figaro")
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// smokeStore returns an env over an isolated state/runtime/config trio.
func smokeStore(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"state", "run", "config"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The copied config carries credentials that are 644 at rest, safe only
	// because ~/.config/figaro is 700: re-assert the boundary and tighten the
	// files.
	if home, err := os.UserHomeDir(); err == nil {
		src := filepath.Join(home, ".config", "figaro")
		cfg := filepath.Join(dir, "config")
		_ = exec.Command("cp", "-r", src+"/.", cfg).Run()
		if err := os.Chmod(cfg, 0o700); err != nil {
			t.Fatalf("hardening the config copy: %v", err)
		}
		for _, secret := range []string{"providers", "hush"} {
			_ = filepath.WalkDir(filepath.Join(cfg, secret), func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				mode := fs.FileMode(0o600)
				if d.IsDir() {
					mode = 0o700
				}
				_ = os.Chmod(path, mode)
				return nil
			})
		}
	}
	// An inherited FIGARO_ARIA statically attends the pane and disables binding.
	var env []string
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "FIGARO_ARIA="), strings.HasPrefix(kv, "FIGARO_NO_BIND="), strings.HasPrefix(kv, "FIGARO_CALLER="):
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"FIGARO_STATE_DIR="+filepath.Join(dir, "state"),
		"FIGARO_RUNTIME_DIR="+filepath.Join(dir, "run"),
		"FIGARO_CONFIG_DIR="+filepath.Join(dir, "config"),
	)
}

// pane is one window on a private tmux server, never the user's.
type pane struct {
	t      *testing.T
	socket string
	env    []string
	bin    string
	height int // read back from tmux, not the -y asked for
}

func newPane(t *testing.T, env []string, bin string, w, h int) *pane {
	t.Helper()
	p := &pane{t: t, socket: filepath.Join(t.TempDir(), "tmux.sock"), env: env, bin: bin}
	// `-y N` gives pane height N-1: the status row is taken at creation and
	// turning status off afterwards does not give it back. Read the height back.
	p.tmux("new-session", "-d", "-s", "smoke", "-x", smokeItoa(w), "-y", smokeItoa(h+1), "bash", "--norc")
	p.tmux("set", "-g", "status", "off")
	got := strings.TrimSpace(p.tmuxOut("display", "-p", "-t", "smoke:0", "#{pane_height}"))
	p.height = smokeAtoi(got)
	if p.height != h {
		t.Logf("pane height is %d (asked for %d), assertions use %d", p.height, h, p.height)
	}
	t.Cleanup(p.close)
	return p
}

// A tmux command that fails is a broken fixture, never a declined case.
func (p *pane) tmux(args ...string) {
	p.t.Helper()
	if out, err := p.run(args...); err != nil {
		p.t.Fatalf("tmux %v failed (%v): %s\nthe harness could not drive the terminal; "+
			"this is a broken fixture, not a case declining", args, err, out)
	}
}

func (p *pane) tmuxOut(args ...string) string {
	p.t.Helper()
	out, err := p.run(args...)
	if err != nil {
		p.t.Fatalf("tmux %v failed (%v): %s\nthe harness could not drive the terminal; "+
			"this is a broken fixture, not a case declining", args, err, out)
	}
	return out
}

func (p *pane) run(args ...string) (string, error) {
	cmd := exec.Command("tmux", append([]string{"-S", p.socket}, args...)...)
	cmd.Env = append(p.env, "TMUX=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// send arrives as one read; use typeSlowly for anything a human would type.
func (p *pane) send(s string) { p.tmux("send-keys", "-l", s) }

func (p *pane) key(k string) { p.tmux("send-keys", k) }

// typeSlowly sends one character per read, the way a human types.
func (p *pane) typeSlowly(s string) {
	for _, r := range s {
		p.send(string(r))
		time.Sleep(120 * time.Millisecond)
	}
}

// scrollback keeps frames that existed for milliseconds before scrolling away.
func (p *pane) visible() string    { return p.tmuxOut("capture-pane", "-p") }
func (p *pane) scrollback() string { return p.tmuxOut("capture-pane", "-p", "-S", "-") }

// waitIdle polls until the pane stops changing; a fixed sleep is a guess.
func (p *pane) waitIdle(d time.Duration) {
	p.t.Helper()
	var last string
	stable, deadline := 0, time.Now().Add(d)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		cur := p.visible()
		if cur == last {
			if stable++; stable >= 4 {
				return
			}
		} else {
			stable = 0
		}
		last = cur
	}
}

// alive reports whether the pane's foreground command is still figaro.
func (p *pane) alive() bool {
	out := strings.TrimSpace(p.tmuxOut("display", "-p", "#{pane_current_command}"))
	return strings.Contains(out, "figaro")
}

// pagerChrome counts pager markers: an absence inside a pager is not an absence.
func pagerChrome(capture string) int {
	n := 0
	for _, marker := range []string{"? help", "! status"} {
		n += strings.Count(capture, marker)
	}
	return n
}

// bodyLines counts tok as a whole rendered line: the footer mantra and the
// wrapped shell echo both contain it, so a substring count is unsound.
func bodyLines(capture, tok string) int {
	n := 0
	for _, ln := range strings.Split(capture, "\n") {
		if strings.TrimSpace(ln) == tok {
			n++
		}
	}
	return n
}

// footers counts rendered bookend rows; one turn leaves exactly one.
func footers(capture string) int {
	n := 0
	for _, ln := range strings.Split(capture, "\n") {
		if strings.Contains(ln, "aria ") && strings.Contains(ln, "───") {
			n++
		}
	}
	return n
}

// statusRows counts pager status rows on the grid: two means something wrote
// outside the frame buffer and the grid scrolled under the painter.
func statusRows(capture string) int {
	n := 0
	for _, ln := range strings.Split(capture, "\n") {
		if strings.Contains(ln, "? help") {
			n++
		}
	}
	return n
}

// rawVisible keeps escape sequences: a footer row from footerRows is wrapped in
// \x1b[2m...\x1b[0m, so an unstyled row among them bypassed the frame buffer.
func (p *pane) rawVisible() string { return p.tmuxOut("capture-pane", "-p", "-e") }

// close stops the scratch daemon as well as the tmux server; kill-server alone
// orphans it.
func (p *pane) close() {
	stop := exec.Command(p.bin, "stop", "--force")
	stop.Env = p.env
	_ = stop.Run()
	_, _ = p.run("kill-server")
}

func smokeItoa(i int) string { return fmt.Sprintf("%d", i) }
func smokeAtoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func (p *pane) startTurn(prompt string) {
	p.t.Helper()
	p.send(p.bin + " send -- '" + prompt + "'")
	p.key("Enter")
}
