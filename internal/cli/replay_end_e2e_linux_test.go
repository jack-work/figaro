//go:build linux

package cli

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Since the listen learned to reconnect, a replay ended with "the angelus
// went away: reconnecting to <aria>": the tape's stand-in socket closing
// read as a lost daemon. A tape that runs out ends the replay quietly.
func TestAReplayEndsWithoutReconnecting(t *testing.T) {
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
	replay := r.cmd("replay", tapePath, "--speed", "50")
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
	case <-time.After(30 * time.Second):
		_ = replay.Process.Kill()
		t.Fatalf("the replay never ended; screen tail:\n%q", out.tail(800))
	}
	time.Sleep(100 * time.Millisecond)
	if out.has("went away") || out.has("reconnecting") {
		t.Fatalf("the end of the tape read as a lost daemon; screen tail:\n%q", out.tail(800))
	}
}
