//go:build linux

package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

func newUnitRig(t *testing.T, providerURL string) *unitRig {
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
	rig := newSendRig(t, providerURL)
	u := &unitRig{t: t, rig: rig}
	for _, kv := range rig.env {
		if v, ok := strings.CutPrefix(kv, "FIGARO_RUNTIME_DIR="); ok {
			u.run = v
		}
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	u.unit = "figaro-scope-test-" + hex.EncodeToString(b[:])
	t.Cleanup(func() { _, _ = u.bounded(rig.cmd("stop", "--force"), unitWait) })
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
		if k == "FIGARO_NO_ANGELUS_SCOPE" || !(strings.HasPrefix(k, "FIGARO_") || strings.HasPrefix(k, "ANTHROPIC_") || strings.HasPrefix(k, "HUSH_")) {
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
	prov := newScriptedProvider(t, func(last string) (bool, string) {
		if strings.Contains(last, "scoped-") {
			return false, sseText("done")
		}
		return false, sseBashArgs("toolu_scope", map[string]any{"command": "echo scoped-$$; sleep 300", "yieldMs": 500})
	})
	u := newUnitRig(t, prov.srv.URL)
	if out, err := u.bounded(u.inUnit(true, "ls"), unitWait); err != nil {
		t.Fatalf("figaro ls in a transient service: %v\n%s%s", err, out, u.state())
	}
	pid := u.daemonPID()
	if !alive(pid) {
		t.Fatalf("the daemon %d died with the unit that autostarted it", pid)
	}
	cg := cgroupOf(pid)
	if !strings.Contains(cg, "/figaro-angelus-") || !strings.HasSuffix(cg, ".scope") || strings.Contains(cg, u.unit) {
		t.Fatalf("the daemon %d is in %q, want a figaro-angelus scope of its own", pid, cg)
	}
	if out, err := u.bounded(u.rig.cmd("ls"), unitWait); err != nil {
		t.Fatalf("figaro ls after the unit ended: %v\n%s%s", err, out, u.state())
	}
	if again := u.daemonPID(); again != pid {
		t.Fatalf("a second ls found daemon %d, not the one the unit started (%d)", again, pid)
	}

	if out, err := u.bounded(u.rig.cmd("new", "-f", "-j", "--", "run a command"), unitWait); err != nil {
		t.Fatalf("figaro new: %v\n%s%s", err, out, u.state())
	}
	prov.awaitAsk(t, 0, 60*time.Second, "scoped-")
	tree := descendants(pid)
	t.Cleanup(func() {
		for _, child := range tree {
			_ = syscall.Kill(child, syscall.SIGKILL)
		}
	})
	if len(tree) == 0 {
		t.Fatalf("a turn that ran bash left the daemon %d no child to check", pid)
	}
	for _, child := range tree {
		if got := cgroupOf(child); got != cg {
			t.Errorf("process %d under the daemon is in %q, not the daemon's %q", child, got, cg)
		}
	}
}

func sseBashArgs(id string, args map[string]any) string {
	raw, _ := json.Marshal(args)
	partial, _ := json.Marshal(string(raw))
	return strings.Replace(sseBash(id, ""), `"partial_json":"{\"command\":\"\"}"`, `"partial_json":`+string(partial), 1)
}

func descendants(root int) []int {
	children := map[int][]int{}
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		fields := strings.Fields(string(raw[strings.LastIndexByte(string(raw), ')')+1:]))
		if len(fields) < 2 {
			continue
		}
		ppid, _ := strconv.Atoi(fields[1])
		children[ppid] = append(children[ppid], pid)
	}
	var out []int
	queue := append([]int(nil), children[root]...)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		out = append(out, pid)
		queue = append(queue, children[pid]...)
	}
	return out
}

func TestADaemonRunByAUnitStaysThatUnits(t *testing.T) {
	u := newUnitRig(t, "http://127.0.0.1:9")
	if out, err := u.bounded(u.inUnit(false, "--angelus"), unitWait); err != nil {
		t.Fatalf("figaro --angelus as a transient service: %v\n%s%s", err, out, u.state())
	}
	t.Cleanup(func() { _, _ = u.bounded(u.systemd("systemctl", "stop", u.unit+".service"), unitWait) })
	u.waitForDaemon()
	pid := u.daemonPID()
	if cg := cgroupOf(pid); !strings.HasSuffix(cg, "/"+u.unit+".service") {
		t.Fatalf("a daemon its own unit runs moved to %q", cg)
	}
	if out, err := u.bounded(u.systemd("systemctl", "stop", u.unit+".service"), unitWait); err != nil {
		t.Fatalf("stop %s: %v\n%s%s", u.unit, err, out, u.state())
	}
	if alive(pid) {
		t.Fatalf("the daemon %d outlived the unit that runs it", pid)
	}
}

const unitWait = 2 * time.Minute

// bounded runs c and gives up after within, failing with what the unit, the
// daemon and its startup log were doing, instead of riding the package's
// timeout. WaitDelay keeps a grandchild holding the output pipe from
// stalling Wait after the process itself has gone.
func (u *unitRig) bounded(c *exec.Cmd, within time.Duration) ([]byte, error) {
	u.t.Helper()
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	c.WaitDelay = 5 * time.Second
	if err := c.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		return out.Bytes(), err
	case <-time.After(within):
		_ = c.Process.Kill()
		<-done
		u.t.Fatalf("%s did not finish in %s; its output:\n%s%s", strings.Join(c.Args, " "), within, out.String(), u.state())
		return nil, nil
	}
}

// state is everything worth seeing when a wait gives up: the unit as the
// user manager has it, the daemon's startup log, and the runtime dir.
func (u *unitRig) state() string {
	var b strings.Builder
	status := u.systemd("systemctl", "status", "--no-pager", "--full", "-n", "40", u.unit+".service")
	status.WaitDelay = time.Second
	st, _ := status.CombinedOutput()
	fmt.Fprintf(&b, "\n--- systemctl --user status %s.service\n%s", u.unit, st)
	if raw, err := os.ReadFile(filepath.Join(u.run, "angelus.startup")); err == nil {
		fmt.Fprintf(&b, "\n--- angelus.startup\n%s", raw)
	}
	entries, _ := os.ReadDir(u.run)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	fmt.Fprintf(&b, "\n--- %s: %s\n", u.run, strings.Join(names, " "))
	return b.String()
}

// waitForDaemon waits for the socket and the pid file both: Run listens
// before it writes angelus.pid, so a socket alone does not mean the pid is
// there to read.
func (u *unitRig) waitForDaemon() {
	u.t.Helper()
	sock, pidFile := filepath.Join(u.run, "angelus.sock"), filepath.Join(u.run, "angelus.pid")
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(pidFile); err == nil && socketExists(sock) {
			return
		}
		if time.Now().After(deadline) {
			u.t.Fatalf("no daemon socket and pid file in %s in 30 s%s", u.run, u.state())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
