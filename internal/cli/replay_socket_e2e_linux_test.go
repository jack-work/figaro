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

// A replay serves its tape on a socket in a figtape-* directory under TMPDIR.
// The directory was removed only by a defer, so a replay ended by a signal
// (tmux closing its pane sends SIGHUP) left it behind: hundreds piled up in
// /tmp. Every exit removes it.
func TestAReplayEndedByASignalLeavesNoSocketDir(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the binary in a pty; skipped under -short")
	}
	tapePath, err := filepath.Abs(filepath.Join("testdata", "tapes", "stream-eager.tape"))
	if err != nil {
		t.Fatal(err)
	}
	for _, sig := range []syscall.Signal{syscall.SIGHUP, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			r := newRestartRig(t)
			tmp := filepath.Join(r.root, "tmp")
			if err := os.MkdirAll(tmp, 0o700); err != nil {
				t.Fatal(err)
			}
			replay := r.cmd("replay", tapePath, "--speed", "0.05")
			replay.Env = append(replay.Env, "TERM=xterm-256color", "TMPDIR="+tmp)
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
			go func() { _, _ = io.Copy(io.Discard, ptmx) }()
			exited := make(chan error, 1)
			go func() { exited <- replay.Wait() }()

			dirs := func() []string {
				m, _ := filepath.Glob(filepath.Join(tmp, "figtape-*"))
				return m
			}
			deadline := time.Now().Add(15 * time.Second)
			for len(dirs()) == 0 {
				if time.Now().After(deadline) {
					_ = replay.Process.Kill()
					t.Fatal("the replay never made its socket dir")
				}
				time.Sleep(20 * time.Millisecond)
			}
			time.Sleep(500 * time.Millisecond)
			if err := replay.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			select {
			case <-exited:
			case <-time.After(15 * time.Second):
				_ = replay.Process.Kill()
				t.Fatalf("the replay did not end on %s", sig)
			}
			if left := dirs(); len(left) > 0 {
				t.Fatalf("%s left the socket dir behind: %v", sig, left)
			}
		})
	}
}
