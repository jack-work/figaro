//go:build linux

package cli

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func watchListenerConnects(t *testing.T, path string) (*net.UnixListener, *atomic.Bool) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ul := ln.(*net.UnixListener)
	ul.SetUnlinkOnClose(false)
	var connected atomic.Bool
	go func() {
		c, err := ul.Accept()
		if err == nil {
			connected.Store(true)
			c.Close()
		}
	}()
	t.Cleanup(func() { ul.Close() })
	return ul, &connected
}

// Defends F1: rest against an idle manager-held socket must never connect(2),
// because connect(2) triggers EPOLLIN in systemd and wakes figaro.service.
func TestRestAgainstIdleManagerSocketNeverConnects(t *testing.T) {
	if testing.Short() {
		t.Skip("builds figaro and keeps a rig under /var/tmp")
	}
	r := newRestartRig(t)
	_, connected := watchListenerConnects(t, r.socket())

	out := r.run("rest")
	if !strings.Contains(out, "not running") || !strings.Contains(out, "service manager") {
		t.Fatalf("rest output: %s", out)
	}
	time.Sleep(50 * time.Millisecond)
	if connected.Load() {
		t.Fatal("rest called connect(2) on an idle manager-held socket")
	}
	if _, err := os.Stat(r.socket()); err != nil {
		t.Fatalf("rest removed the manager's socket: %v", err)
	}
}

// Defends F1: rest --force on a socket-activated daemon kills the daemon,
// leaves the manager's socket in place, and does not connect(2) in
// removeUnheldSocket (which would immediately re-trigger the service).
func TestRestForceOnActivatedDaemonNeitherDialsNorRemovesSocket(t *testing.T) {
	if testing.Short() {
		t.Skip("starts an isolated fixture daemon")
	}
	r := newRestartRig(t)
	ln, err := net.Listen("unix", r.socket())
	if err != nil {
		t.Fatal(err)
	}
	ul := ln.(*net.UnixListener)
	ul.SetUnlinkOnClose(false)
	t.Cleanup(func() { ul.Close() })
	f, err := ul.File()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	m := managerSocket{t: t, file: f}

	d := m.activate(r, "angelus")
	r.run("ls", "--json")
	if out := r.run("rest", "--force"); !strings.Contains(out, "forcefully terminated") {
		t.Fatalf("rest --force output: %s", out)
	}
	if err := waitDone(t, d.done, 10*time.Second); err == nil {
		t.Fatalf("SIGKILLed daemon exited 0:\n%s", d.log)
	}
	if _, err := os.Stat(r.socket()); err != nil {
		t.Fatalf("rest --force removed the manager's socket: %v", err)
	}
	_ = ul.SetDeadline(time.Now().Add(100 * time.Millisecond))
	if c, err := ul.Accept(); err == nil {
		c.Close()
		t.Fatal("removeUnheldSocket queued a connection on the manager's socket after SIGKILL")
	} else {
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() {
			t.Fatalf("unexpected accept error on manager socket: %v", err)
		}
	}
}

func TestRestRemovesStaleUnheldSocket(t *testing.T) {
	if testing.Short() {
		t.Skip("builds figaro and keeps a rig under /var/tmp")
	}
	r := newRestartRig(t)
	ln, err := net.Listen("unix", r.socket())
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()

	out := r.run("rest")
	if !strings.Contains(out, "not running") {
		t.Fatalf("rest output: %s", out)
	}
	if _, err := os.Stat(r.socket()); !os.IsNotExist(err) {
		t.Fatalf("stale unheld socket remained on disk: %v", err)
	}
}

func TestRestRefusesWhenPidFilePermissionIsDenied(t *testing.T) {
	if testing.Short() {
		t.Skip("builds figaro and keeps a rig under /var/tmp")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses 0000 mode")
	}
	r := newRestartRig(t)
	_, connected := watchListenerConnects(t, r.socket())
	pidPath := filepath.Join(r.root, "run", "angelus.pid")
	if err := os.WriteFile(pidPath, []byte("999999"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(pidPath, 0o600) })

	out, err := r.cmd("rest").CombinedOutput()
	if err == nil {
		t.Fatalf("rest succeeded despite unreadable angelus.pid:\n%s", out)
	}
	if !strings.Contains(string(out), "angelus.pid") {
		t.Fatalf("refusal did not name angelus.pid:\n%s", out)
	}
	if connected.Load() {
		t.Fatal("rest dialed the socket when angelus.pid returned EACCES")
	}
	if _, serr := os.Stat(r.socket()); serr != nil {
		t.Fatalf("rest removed the socket on EACCES: %v", serr)
	}
}
