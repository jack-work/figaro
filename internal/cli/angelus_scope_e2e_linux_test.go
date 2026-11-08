//go:build linux

package cli

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/testfence"
)

// fig-take-update ran in a transient service, its warm-up `figaro list`
// autostarted the daemon in that unit's cgroup, and systemd killed the daemon
// when the unit ended. A unit running the CLI must leave the daemon behind; a
// unit running the daemon itself still owns it.

type unitRig struct {
	t    *testing.T
	rig  *sendRig
	run  string
	unit string
}

func newUnitRig(t *testing.T) *unitRig {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a daemon under the user manager; skipped under -short")
	}
	xdg := os.Getenv("XDG_RUNTIME_DIR")
	if xdg == "" || !socketExists(filepath.Join(xdg, "systemd", "private")) {
		t.Skip("no systemd user manager here")
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skip("no systemd-run here")
	}
	rig := newSendRig(t, "http://127.0.0.1:9")
	u := &unitRig{t: t, rig: rig}
	for _, kv := range rig.env {
		if v, ok := strings.CutPrefix(kv, "FIGARO_RUNTIME_DIR="); ok {
			u.run = v
		}
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	u.unit = "figaro-scope-test-" + hex.EncodeToString(b[:])
	return u
}

func (u *unitRig) systemd(tool string, args ...string) *exec.Cmd {
	c := exec.Command(tool, append([]string{"--user"}, args...)...)
	env := []string{"DBUS_SESSION_BUS_ADDRESS=unix:path=" + filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "bus")}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "DBUS_SESSION_BUS_ADDRESS=") {
			env = append(env, kv)
		}
	}
	c.Env = env
	return c
}

// inUnit runs the figaro binary inside a transient service carrying the rig's
// environment, except the knob every test run sets to keep daemons in place.
func (u *unitRig) inUnit(wait bool, args ...string) *exec.Cmd {
	run := []string{"--unit", u.unit, "--collect", "--quiet", "--setenv", "DBUS_SESSION_BUS_ADDRESS=" + testfence.DeadSessionBus}
	if wait {
		run = append(run, "--wait", "--pipe")
	}
	for _, kv := range u.rig.env {
		k, _, _ := strings.Cut(kv, "=")
		if k == "FIGARO_NO_ANGELUS_SCOPE" || !(strings.HasPrefix(k, "FIGARO_") || strings.HasPrefix(k, "ANTHROPIC_")) {
			continue
		}
		run = append(run, "--setenv", kv)
	}
	run = append(run, "--", u.rig.bin)
	return u.systemd("systemd-run", append(run, args...)...)
}

func (u *unitRig) daemonPID() int {
	u.t.Helper()
	raw, err := os.ReadFile(filepath.Join(u.run, "angelus.pid"))
	if err != nil {
		u.t.Fatalf("no daemon pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		u.t.Fatalf("pid file %q: %v", raw, err)
	}
	u.t.Cleanup(func() {
		if alive(pid) && isOurDaemon(pid, u.run) {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
	})
	return pid
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func cgroupOf(pid int) string {
	raw, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	return strings.TrimSpace(string(raw))
}

func isOurDaemon(pid int, run string) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	return err == nil && strings.Contains(string(raw), "FIGARO_RUNTIME_DIR="+run+"\x00")
}

func TestAnAutostartedDaemonOutlivesTheUnitThatStartedIt(t *testing.T) {
	u := newUnitRig(t)
	if out, err := u.inUnit(true, "ls").CombinedOutput(); err != nil {
		t.Fatalf("figaro ls in a transient service: %v\n%s", err, out)
	}
	pid := u.daemonPID()
	if !alive(pid) {
		t.Fatalf("the daemon %d died with the unit that autostarted it", pid)
	}
	cg := cgroupOf(pid)
	if !strings.Contains(cg, "/figaro-angelus-") || !strings.HasSuffix(cg, ".scope") || strings.Contains(cg, u.unit) {
		t.Fatalf("the daemon %d is in %q, want a figaro-angelus scope of its own", pid, cg)
	}
	if out, err := u.rig.cmd("ls").CombinedOutput(); err != nil {
		t.Fatalf("figaro ls after the unit ended: %v\n%s", err, out)
	}
	if again := u.daemonPID(); again != pid {
		t.Fatalf("a second ls found daemon %d, not the one the unit started (%d)", again, pid)
	}
}

func TestADaemonRunByAUnitStaysThatUnits(t *testing.T) {
	u := newUnitRig(t)
	if out, err := u.inUnit(false, "--angelus").CombinedOutput(); err != nil {
		t.Fatalf("figaro --angelus as a transient service: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = u.systemd("systemctl", "stop", u.unit+".service").Run() })
	waitForSocket(t, filepath.Join(u.run, "angelus.sock"))
	pid := u.daemonPID()
	if cg := cgroupOf(pid); !strings.HasSuffix(cg, "/"+u.unit+".service") {
		t.Fatalf("a daemon its own unit runs moved to %q", cg)
	}
	if out, err := u.systemd("systemctl", "stop", u.unit+".service").CombinedOutput(); err != nil {
		t.Fatalf("stop %s: %v\n%s", u.unit, err, out)
	}
	if alive(pid) {
		t.Fatalf("the daemon %d outlived the unit that runs it", pid)
	}
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !socketExists(path) {
		if time.Now().After(deadline) {
			t.Fatalf("no daemon socket at %s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
