//go:build linux

package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// SIGTERM ends an interactive send at once, and the terminal it leaves is the
// cooked one it found, not the raw one the pager ran in.
func TestSigtermEndsAnInteractiveSendAndRestoresTheTerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon and a real send; skipped under -short")
	}
	prov := newHeldProvider(t)
	rig := newSendRig(t, prov.srv.URL)

	born, err := rig.cmd("new", "-j", "--", "x").Output()
	if err != nil {
		t.Fatalf("new: %v\n%s", err, born)
	}
	lines := strings.Split(strings.TrimSpace(string(born)), "\n")
	var created struct {
		AriaID string `json:"aria_id"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &created); err != nil || created.AriaID == "" {
		t.Fatalf("new -j printed no aria id: %q", born)
	}
	select {
	case <-prov.held:
	case <-time.After(30 * time.Second):
		t.Fatal("the first turn never reached the provider")
	}

	send := rig.cmd("send", "--id", created.AriaID, "--", "y")
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer ptmx.Close()
	defer tty.Close()
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 40, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	cooked, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	send.Stdin, send.Stdout, send.Stderr = tty, tty, tty
	send.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := send.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, ptmx) }()
	exited := make(chan error, 1)
	go func() { exited <- send.Wait() }()

	deadline := time.Now().Add(15 * time.Second)
	for rig.queued(t, created.AriaID) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the second prompt was never queued behind the first turn")
		}
		time.Sleep(50 * time.Millisecond)
	}
	raw := false
	for !raw && time.Now().Before(deadline) {
		now, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS)
		raw = err == nil && now.Lflag&unix.ICANON == 0
		if !raw {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !raw {
		t.Fatal("the send never took the terminal raw, so it is not the interactive session under test")
	}

	if err := send.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 128+int(syscall.SIGTERM) {
			t.Fatalf("send ended with %v, want exit %d", err, 128+int(syscall.SIGTERM))
		}
	case <-time.After(5 * time.Second):
		_ = send.Process.Kill()
		<-exited
		t.Fatal("send was still running 5s after SIGTERM")
	}
	after, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	const modes = unix.ICANON | unix.ECHO | unix.ISIG
	if after.Lflag&modes != cooked.Lflag&modes {
		t.Fatalf("terminal left with lflag %#x, want the cooked %#x", after.Lflag&modes, cooked.Lflag&modes)
	}
}
