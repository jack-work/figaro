//go:build linux

package cli

import (
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type managerSocket struct {
	t    *testing.T
	file *os.File
}

func holdSocketLikeAManager(t *testing.T, path string) managerSocket {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ul := ln.(*net.UnixListener)
	ul.SetUnlinkOnClose(false)
	f, err := ul.File()
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	t.Cleanup(func() { f.Close() })
	return managerSocket{t: t, file: f}
}

type activated struct {
	pid  int
	done chan error
	log  *strings.Builder
}

func (m managerSocket) activate(r *restartRig, names string) activated {
	m.t.Helper()
	c := exec.Command("sh", "-c", `LISTEN_PID=$$ exec "$0" --angelus`, r.bin)
	c.Env = append(append([]string{}, r.env...), "LISTEN_FDS=1", "LISTEN_FDNAMES="+names)
	c.ExtraFiles = []*os.File{m.file}
	out := &strings.Builder{}
	c.Stdout, c.Stderr = out, out
	if err := c.Start(); err != nil {
		m.t.Fatal(err)
	}
	a := activated{pid: c.Process.Pid, done: make(chan error, 1), log: out}
	go func() { a.done <- c.Wait() }()
	m.t.Cleanup(func() {
		_ = c.Process.Kill()
	})
	return a
}

func waitDone(t *testing.T, done <-chan error, within time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(within):
		t.Fatalf("still running after %s", within)
		return nil
	}
}

func startQueued(t *testing.T, c *exec.Cmd) (<-chan error, *strings.Builder) {
	t.Helper()
	out := &strings.Builder{}
	c.Stdout, c.Stderr = out, out
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	return done, out
}

// Defends stage 2 of the deployment modes: a daemon handed its CLI door by
// the service manager serves it, leaves the path to the manager when it
// stops, and a call queued while nothing runs is answered by the next start.
func TestSocketActivatedDaemonServesAndLeavesTheManagersSocket(t *testing.T) {
	if testing.Short() {
		t.Skip("starts an isolated fixture daemon")
	}
	r := newRestartRig(t)
	m := holdSocketLikeAManager(t, r.socket())

	first := m.activate(r, "angelus")
	r.run("ls", "--json")
	if out := r.run("rest"); !strings.Contains(out, "put to rest") {
		t.Fatalf("rest did not stop the activated daemon: %s", out)
	}
	if err := waitDone(t, first.done, 20*time.Second); err != nil {
		t.Fatalf("activated daemon exited badly: %v\n%s", err, first.log)
	}
	if _, err := os.Stat(r.socket()); err != nil {
		t.Fatalf("the daemon removed the manager's socket on its way out: %v\n%s", err, first.log)
	}

	out := r.run("rest")
	if !strings.Contains(out, "not running") || !strings.Contains(out, "service manager") {
		t.Fatalf("rest against an idle activation socket: %s", out)
	}
	if _, err := os.Stat(r.socket()); err != nil {
		t.Fatalf("rest removed the manager's socket: %v", err)
	}

	queued, queuedOut := startQueued(t, r.cmd("ls", "--json"))
	time.Sleep(200 * time.Millisecond)
	second := m.activate(r, "angelus")
	if err := waitDone(t, queued, 30*time.Second); err != nil {
		t.Fatalf("a call queued before the start was not answered: %v\n%s\n%s", err, queuedOut, second.log)
	}
	r.run("rest")
	if err := waitDone(t, second.done, 20*time.Second); err != nil {
		t.Fatalf("second activation exited badly: %v\n%s", err, second.log)
	}
}

func TestActivationWithoutTheCLISocketIsRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("starts an isolated fixture daemon")
	}
	r := newRestartRig(t)
	m := holdSocketLikeAManager(t, r.socket())
	d := m.activate(r, "figaro.socket")
	if err := waitDone(t, d.done, 20*time.Second); err == nil {
		t.Fatalf("a daemon given no socket named angelus started anyway:\n%s", d.log)
	}
	if !strings.Contains(d.log.String(), `no socket named "angelus"`) {
		t.Fatalf("refusal does not name the socket it wanted:\n%s", d.log)
	}
}
