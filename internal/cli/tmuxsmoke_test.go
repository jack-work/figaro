package cli

// ---------------------------------------------------------------------------
// TMUX SMOKE HARNESS — the tests a unit test provably cannot write.
//
// WHY THIS FILE EXISTS. On 2026-07-25, EIGHT separate times, a green test
// certified broken code. Every one was caught by a human or an agent driving a
// real terminal, never by the suite. The diagnosis, from the aria that found
// the worst of them:
//
//	"Every test double that diverged from production diverged by being
//	 TIDIER THAN REALITY."
//
//   - the paging double treated `limit` as a COUNT of parts; production used a
//     BYTE budget. ~30 tests green while the pager rendered ONE node for an
//     800-node aria.
//   - a renderer test asserted over compose() OUTPUT — what figaro DECIDES to
//     paint — and so could not observe rows scrolling away. Green while the
//     user's reply was lost.
//   - composer tests fed WHOLE STRINGS through consume(); a real user types one
//     byte per read. That hid a byte-vs-rune bug that mojibaked all non-ASCII.
//   - and two bugs shipped that NO in-process test could have caught at all:
//     the process failing to EXIT after a turn, and a footer duplicated into
//     scrollback.
//
// Those last two are the point. A model of a terminal only knows the bugs its
// author imagined. This harness drives the REAL binary in a REAL pty and reads
// back what a REAL user would see.
//
// DELIBERATELY NOT HERMETIC. These tests use a real provider. A fake provider
// would be one more double free to drift from production — the exact failure
// this file exists to prevent. The cost is tokens and seconds; the benefit is
// that a passing run means something.
//
// RUN THEM:
//
//	FIGARO_TMUX_SMOKE=1 go test ./internal/cli/ -run TestSmoke -v
//
// Skipped by default, so `go test ./...` stays fast and hermetic.
// Full method and traps: the tmux-testing skill (~/.config/figaro/skills/tmux-testing.md) — read it before adding a case.
// ---------------------------------------------------------------------------

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// smokeEnabled gates the suite. Two doors, both explicit: the env var, and the
// presence of tmux. Never auto-enable — a test that silently costs tokens is a
// test people learn to distrust.
func smokeEnabled(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("drives a real terminal and a real provider; skipped under -short")
	}
	if os.Getenv("FIGARO_TMUX_SMOKE") == "" {
		t.Skip("set FIGARO_TMUX_SMOKE=1 to run (drives a real provider — costs tokens)")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
}

// smokeBinary returns a figaro built FROM THIS WORKTREE.
//
// Building from source is strictly stronger than the usual "nix build, then
// assert --version matches HEAD" dance: identity with the tree under test is
// guaranteed by construction rather than checked after the fact, and it picks
// up uncommitted edits, which is what you actually want while iterating.
//
// FIGARO_SMOKE_BIN overrides it when you deliberately want to smoke a specific
// build (a nix store path, say). In that case identity is NOT guaranteed, so we
// print what we are testing — a stale binary replaying already-fixed bugs cost
// this project an entire debugging session.
func smokeBinary(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("FIGARO_SMOKE_BIN"); bin != "" {
		out, _ := exec.Command(bin, "--version").CombinedOutput()
		t.Logf("FIGARO_SMOKE_BIN in use — identity with this worktree is NOT guaranteed: %s",
			strings.TrimSpace(firstLine(string(out))))
		return bin
	}
	bin := filepath.Join(t.TempDir(), "figaro")
	rev, _ := exec.Command("git", "rev-parse", "--short=12", "HEAD").Output()
	ld := fmt.Sprintf("-X github.com/jack-work/figaro/internal/cli.commit=%s",
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

// smokeStore builds an isolated state/runtime/config trio and returns the env.
// NEVER the real store: these tests start daemons, write arias and kill things.
func smokeStore(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"state", "run", "config"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Provider credentials and loadouts come from the real config, copied. We
	// read it; we never write it.
	//
	// THE COPY CARRIES CREDENTIALS, SO IT IS HARDENED EXPLICITLY. `cp` without
	// -p gives the destination the source mode masked by the umask, and the real
	// providers/anthropic.toml is mode 644 inside a 755 providers/ — safe at rest
	// ONLY because ~/.config/figaro is 700. Copying it moves it out from under the
	// one thing defending it. The config dir above is created 0700, which contains
	// it today, but relying on a single parent directory is precisely the pattern
	// that leaked a sibling harness's copy into world-traversable /var/tmp. So
	// re-assert the boundary AND tighten the secrets themselves, belt and braces:
	// a later refactor that recreates this directory differently must not silently
	// re-open the hole.
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
					return nil // absent is fine: not every config has both
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
	return append(os.Environ(),
		"FIGARO_STATE_DIR="+filepath.Join(dir, "state"),
		"FIGARO_RUNTIME_DIR="+filepath.Join(dir, "run"),
		"FIGARO_CONFIG_DIR="+filepath.Join(dir, "config"),
	)
}

// pane is one tmux window on a PRIVATE server. Private because a shared server
// leaks sessions between runs, and because killing the user's tmux would be
// unforgivable.
type pane struct {
	t      *testing.T
	socket string
	env    []string
	bin    string
	height int // the REAL pane height, not the -y you asked for
}

// newPane opens a pane of exactly h usable rows.
//
// TRAP, and it invalidated an entire night of measurements: `tmux new-session
// -y N` yields pane_height N-1, because the status bar takes a row. Three
// separate investigators reported "h=1 loses the reply" while measuring at pane
// height ZERO — a state no user can reach. We turn the status bar OFF and then
// READ BACK #{pane_height}, so the number in a failure message is the number
// figaro actually saw.
func newPane(t *testing.T, env []string, bin string, w, h int) *pane {
	t.Helper()
	p := &pane{t: t, socket: filepath.Join(t.TempDir(), "tmux.sock"), env: env, bin: bin}
	// -y h+1, MEASURED not guessed: tmux subtracts the status row at CREATION,
	// and turning the status bar off afterwards does not give it back to a
	// detached session (nor does resize-window). Probed: -y 30 -> 29 whether
	// status is turned off before or after; -y 31 -> 30. So ask for one more.
	p.tmux("new-session", "-d", "-s", "smoke", "-x", smokeItoa(w), "-y", smokeItoa(h+1), "bash", "--norc")
	p.tmux("set", "-g", "status", "off")
	got := strings.TrimSpace(p.tmuxOut("display", "-p", "-t", "smoke:0", "#{pane_height}"))
	p.height = smokeAtoi(got)
	if p.height != h {
		// Never silently proceed on a height you did not ask for: an entire
		// night of "h=1 loses the reply" reports were measured at pane height 0.
		t.Logf("pane height is %d (asked for %d) — assertions use %d", p.height, h, p.height)
	}
	t.Cleanup(p.close)
	return p
}

func (p *pane) tmux(args ...string) {
	p.t.Helper()
	if out, err := p.run(args...); err != nil {
		p.t.Skipf("tmux %v failed (%v): %s", args, err, out)
	}
}

func (p *pane) tmuxOut(args ...string) string {
	p.t.Helper()
	out, err := p.run(args...)
	if err != nil {
		p.t.Skipf("tmux %v failed (%v): %s", args, err, out)
	}
	return out
}

func (p *pane) run(args ...string) (string, error) {
	cmd := exec.Command("tmux", append([]string{"-S", p.socket}, args...)...)
	cmd.Env = append(p.env, "TMUX=") // never inherit an outer session
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// send types a literal string as ONE read. Use typeSlowly for anything a human
// would type — see the comment there.
func (p *pane) send(s string) { p.tmux("send-keys", "-l", s) }

// key sends a named key (Enter, C-c, C-d, Escape...).
func (p *pane) key(k string) { p.tmux("send-keys", k) }

// typeSlowly types one character per tmux call, with a gap.
//
// THIS DISTINCTION IS NOT PEDANTRY. Every composer test in this repo once fed
// whole strings through the input path and passed, while a real user typing one
// byte per read hit a byte-vs-rune bug that corrupted every non-ASCII character.
// A whole-string send is a DIFFERENT INPUT PATTERN from typing. If the thing
// under test is input, type slowly.
func (p *pane) typeSlowly(s string) {
	for _, r := range s {
		p.send(string(r))
		time.Sleep(120 * time.Millisecond)
	}
}

// visible is the pane as shown. scrollback is everything, including frames that
// existed for milliseconds before scrolling away — which is the ONLY way to see
// a duplicated footer or a submit-time frame.
func (p *pane) visible() string    { return p.tmuxOut("capture-pane", "-p") }
func (p *pane) scrollback() string { return p.tmuxOut("capture-pane", "-p", "-S", "-") }

// waitIdle polls until the pane stops changing. Never sleep a fixed guess: a
// model's first token can take five seconds or fifty.
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

// alive reports whether the pane's foreground command is still figaro. This is
// how you catch a process that will not exit — no in-process test can.
func (p *pane) alive() bool {
	out := strings.TrimSpace(p.tmuxOut("display", "-p", "#{pane_current_command}"))
	return strings.Contains(out, "figaro")
}

// pagerChrome counts markers that mean the transcript pager is up.
//
// GATE EVERY ABSENCE ON THIS. Twice on the night this file was written, an aria
// reported content "missing" when the view had merely auto-promoted to the
// pager and the content sat above the tail window. An absence inside a pager is
// not an absence.
func pagerChrome(capture string) int {
	n := 0
	for _, marker := range []string{"? help", "! status"} {
		n += strings.Count(capture, marker)
	}
	return n
}

// bodyLines counts occurrences of tok as a RENDERED BODY LINE.
//
// TRAP: counting a token across the whole capture is UNSOUND. The footer mantra
// echoes the prompt, the prompt usually contains your token, and an 80-column
// shell echo wraps so its tail fragment matches too. Every trial inflates by
// two, which once made a live bug look fixed. Only the body line is sound.
func bodyLines(capture, tok string) int {
	n := 0
	for _, ln := range strings.Split(capture, "\n") {
		if strings.TrimSpace(ln) == tok {
			n++
		}
	}
	return n
}

// footers counts rendered status/bookend rows. Exactly one turn should leave
// exactly one behind.
func footers(capture string) int {
	n := 0
	for _, ln := range strings.Split(capture, "\n") {
		if strings.Contains(ln, "aria ") && strings.Contains(ln, "───") {
			n++
		}
	}
	return n
}

// statusRows counts RENDERED PAGER STATUS ROWS — rows, not substring hits.
//
// pagerChrome above answers "are we in the pager"; this answers "how many
// status rows are on the grid", which is a different and sharper question. A
// healthy pager paints exactly ONE, at screen[t.h-1]. Two means the terminal's
// grid scrolled under the painter (something wrote outside the frame buffer)
// and the painter then repainted the footer at h-1 while the old copy was
// still on screen — i.e. t.prev no longer describes the terminal.
//
// This is deliberately fix-shape-agnostic: whether the eventual fix leaves the
// pager before printing (0 status rows), routes the text through the frame
// buffer, or suppresses it (1 status row), the count is never 2.
func statusRows(capture string) int {
	n := 0
	for _, ln := range strings.Split(capture, "\n") {
		if strings.Contains(ln, "? help") {
			n++
		}
	}
	return n
}

// rawVisible is the pane WITH escape sequences retained (-e). Needed to prove a
// row did NOT come from the renderer: every footer row that goes through
// footerRows is wrapped in "\x1b[2m" ... "\x1b[0m", so an unstyled row sitting
// among them was written straight to the terminal, bypassing the frame buffer.
func (p *pane) rawVisible() string { return p.tmuxOut("capture-pane", "-p", "-e") }

// close kills the tmux server AND stops the scratch daemon.
//
// The daemon half is not optional. On the night this was written, seventeen
// agents each left an isolated daemon running: 230 orphaned processes, 1.2 GB
// of tmpfs, and a memory-pressure alert with processes already stalling. Every
// one of them had been told to stop the daemon BEFORE testing and never after.
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

// startTurn launches `figaro send` in the pane and returns once it is streaming.
func (p *pane) startTurn(prompt string) {
	p.t.Helper()
	p.send(p.bin + " send -- '" + prompt + "'")
	p.key("Enter")
}
