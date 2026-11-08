//go:build linux

package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/jack-work/figaro/internal/tape"
)

// gen4 bug 6: a replay timed a running tool against today's wall clock, so a
// call recorded months ago read "[133000m12s]" until finished_at arrived. The
// stream-eager tape's write call runs for twelve seconds of the recording; a
// replay counts it on the tape's clock.
func TestAReplayTimesARunningToolOnTheTapesClock(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the binary in a pty; skipped under -short")
	}
	r := newRestartRig(t)
	if err := os.WriteFile(filepath.Join(r.root, "cache", "ambiwidth-xterm-256color"), []byte("narrow"), 0o600); err != nil {
		t.Fatal(err)
	}
	tapePath, err := filepath.Abs(filepath.Join("testdata", "tapes", "stream-eager.tape"))
	if err != nil {
		t.Fatal(err)
	}
	replay := r.cmd("replay", tapePath, "--speed", "4")
	replay.Env = append(replay.Env, "TERM=xterm-256color")
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer ptmx.Close()
	defer tty.Close()
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 40, Cols: 120}); err != nil {
		t.Fatal(err)
	}
	replay.Stdin, replay.Stdout, replay.Stderr = tty, tty, tty
	replay.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Pdeathsig: syscall.SIGKILL}
	if err := replay.Start(); err != nil {
		t.Fatal(err)
	}
	var out listenScreen
	go func() { _, _ = io.Copy(&out, ptmx) }()
	exited := make(chan error, 1)
	go func() { exited <- replay.Wait() }()
	select {
	case <-exited:
	case <-time.After(60 * time.Second):
		_ = replay.Process.Kill()
		t.Fatalf("the replay never ended; screen tail:\n%q", out.tail(800))
	}

	out.mu.Lock()
	screen := ansiEscape.ReplaceAllString(out.buf.String(), "")
	out.mu.Unlock()
	if m := regexp.MustCompile(`\[\d+m\d\ds\]`).FindString(screen); m != "" {
		t.Fatalf("a tool that ran for seconds on the tape was timed %s", m)
	}
	if !regexp.MustCompile(`\[\d+\.\ds\]`).MatchString(screen) {
		t.Fatalf("no running tool timer painted at all; screen tail:\n%q", out.tail(800))
	}
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[()][0-9A-Za-z]|\x1b[=>78]`)

// The gen4 tapes stretch a recorded turn x10 and leave the payload's
// timestamps alone. The clock follows the recorded form deltas, so a call
// opened 1.326s into the recording reads as opened when its frame plays.
func TestATapeClockFollowsARetimedTapesRecordedDeltas(t *testing.T) {
	started := time.UnixMilli(1794093571272)
	delta := func(at float64, ms int64) tape.Frame {
		return tapeFrame(t, at, tape.In, fmt.Sprintf(`{"jsonrpc":"2.0","method":"form.delta","params":{"figaro_id":"a","at":%d}}`, ms))
	}
	frames := []tape.Frame{
		delta(0.5, 1794093571069),
		delta(0.5, 1794093585606),
		delta(18.467, 1794093587397),
		delta(21.590, 1794093587709),
	}
	var pos time.Duration
	c := newTapeClock(started, frames, func() time.Duration { return pos })
	for _, tc := range []struct {
		at   float64
		want int64
	}{
		{13.822, 1794093586932},
		{18.294, 1794093587379},
		{21.481, 1794093587698},
	} {
		pos = time.Duration(tc.at * float64(time.Second))
		if got := c.now().UnixMilli(); got < tc.want-20 || got > tc.want+20 {
			t.Fatalf("at %.3fs the tape clock read %d, want %d (the recorded stamp of that frame)", tc.at, got, tc.want)
		}
	}
	pos = 30 * time.Second
	if got, want := c.now().UnixMilli(), int64(1794093587709+8410); got != want {
		t.Fatalf("past the last delta the clock reads %d, want %d", got, want)
	}
}
