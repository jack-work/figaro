package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The embedded hush agent is figaro's own binary, re-exec'd by
// hush/managed with HUSH_AGENT_CHILD=1. The daemon spawns it and
// keep-alives it for its whole life (keepHushAlive in angelus.go), so
// when the daemon goes the agent has nothing left to serve: it sits on
// its socket holding an unlocked identity until its TTL, which figaro
// sets to 24h. Every dev shell that started and stopped a daemon left
// one behind.
//
// This is the end-to-end oracle for that: a real daemon, a real agent,
// a real `figaro stop`, and /proc as the witness. It is gated because it
// builds a binary, spawns a daemon and writes an age identity; the
// in-suite instrument for the same fix is hushagent_test.go.
//
//	FIGARO_HUSH_E2E=1 go test ./internal/cli -run TestHushAgent -count=1 -v
//
// Run it from a dev shell: `go` must be the flake's toolchain, and the
// isolated dirs below must be the only store it can reach.
func hushE2EEnabled(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("spawns a daemon and an agent process; skipped under -short")
	}
	if os.Getenv("FIGARO_HUSH_E2E") == "" {
		t.Skip("set FIGARO_HUSH_E2E=1 to run (spawns a real daemon and a real hush agent)")
	}
	if runtime.GOOS != "linux" {
		t.Skip("the witness is /proc")
	}
}

// hushRig is one isolated figaro: its own runtime, state, config and
// hush surface, none of them shared with the user's.
type hushRig struct {
	bin  string
	root string
	env  []string
	// hushRuntime is where hush/managed puts the agent socket, and the
	// value the agent child carries in HUSH_MANAGED_RUNTIME_DIR. It is
	// how we tell OUR agent from every other one on the machine.
	hushRuntime string
}

func newHushRig(t *testing.T, name string) *hushRig {
	t.Helper()
	// /var/tmp, not /tmp: /tmp is tmpfs here, and an agent identity plus
	// a store is not something to spend RAM on. Also not t.TempDir(),
	// which follows TMPDIR straight back to tmpfs.
	root, err := os.MkdirTemp("/var/tmp", "figaro-hushstop-"+name+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })

	for _, sub := range []string{"run", "state", "config", "hush"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	rig := &hushRig{
		bin:  smokeBinary(t),
		root: root,
		// FIGARO_HUSH_DIR pins an embedded hush rooted here: its own
		// identity, its own socket. Without it the test would reach the
		// user's real /tmp/figaro-hush agent and stop THAT.
		hushRuntime: filepath.Join(root, "hush", "run"),
	}
	rig.env = append(os.Environ(),
		"FIGARO_RUNTIME_DIR="+filepath.Join(root, "run"),
		"FIGARO_STATE_DIR="+filepath.Join(root, "state"),
		"FIGARO_CONFIG_DIR="+filepath.Join(root, "config"),
		"FIGARO_HUSH_DIR="+filepath.Join(root, "hush"),
		"FIGARO_HUSH_PASSPHRASE=figaro-hushstop-e2e",
		// A short TTL so a test that somehow orphans an agent orphans it
		// for two minutes rather than a day.
		"FIGARO_HUSH_TTL=2m",
	)
	t.Cleanup(func() {
		// Belt and braces: whatever the test proved, leave nothing of
		// ours running. Only ever our own hush runtime dir.
		_ = rig.run("stop", "--force")
		for _, pid := range rig.agentPIDs(t) {
			_ = killPid(pid, syscall.SIGKILL)
		}
	})
	return rig
}

func (r *hushRig) run(args ...string) error {
	cmd := exec.Command(r.bin, args...)
	cmd.Env = r.env
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", filepath.Base(r.bin), strings.Join(args, " "), err, buf.String())
	}
	return nil
}

func (r *hushRig) output(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(r.bin, args...)
	cmd.Env = r.env
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// agentPIDs returns every hush agent child serving THIS rig's hush
// runtime dir. It matches on HUSH_MANAGED_RUNTIME_DIR, not on the
// executable name and not on a pattern: the user's real agent, and any
// other worktree's, are the same binary with the same argv, and the only
// thing that distinguishes them is which socket they answer.
func (r *hushRig) agentPIDs(t *testing.T) []int {
	t.Helper()
	var pids []int
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatalf("read /proc: %v", err)
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		environ, err := os.ReadFile(filepath.Join("/proc", e.Name(), "environ"))
		if err != nil {
			continue // it exited, or it is not ours to read
		}
		vars := strings.Split(string(environ), "\x00")
		child, dir := false, ""
		for _, v := range vars {
			switch {
			case v == "HUSH_AGENT_CHILD=1":
				child = true
			case strings.HasPrefix(v, "HUSH_MANAGED_RUNTIME_DIR="):
				dir = strings.TrimPrefix(v, "HUSH_MANAGED_RUNTIME_DIR=")
			}
		}
		if child && dir == r.hushRuntime {
			pids = append(pids, pid)
		}
	}
	return pids
}

func (r *hushRig) waitForAgent(t *testing.T, within time.Duration) []int {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if pids := r.agentPIDs(t); len(pids) > 0 {
			return pids
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (r *hushRig) waitForNoAgent(t *testing.T, within time.Duration) []int {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		pids := r.agentPIDs(t)
		if len(pids) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return pids
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// start brings the daemon up and waits until it has an agent.
func (r *hushRig) start(t *testing.T) []int {
	t.Helper()
	// `list` is enough to make the CLI ensure a daemon; the daemon's
	// keepHushAlive then spawns the agent on its first EnsureReady.
	if err := r.run("list"); err != nil {
		t.Fatalf("bring the daemon up: %v", err)
	}
	pids := r.waitForAgent(t, 30*time.Second)
	// Prove the fixture can fail. Without this line the whole test
	// passes on a machine where no agent was ever spawned, and reports
	// a fix that does not exist.
	if len(pids) == 0 {
		t.Fatalf("no hush agent appeared for %s within 30s; this test cannot measure anything", r.hushRuntime)
	}
	return pids
}

// TestHushAgentIsRetiredByStop is the reported bug: `figaro stop` stops
// the daemon and leaves the agent.
func TestHushAgentIsRetiredByStop(t *testing.T) {
	hushE2EEnabled(t)
	rig := newHushRig(t, "stop")
	before := rig.start(t)
	t.Logf("agent(s) up: %v", before)

	if err := rig.run("stop"); err != nil {
		t.Fatalf("figaro stop: %v", err)
	}
	if left := rig.waitForNoAgent(t, 15*time.Second); len(left) > 0 {
		t.Fatalf("`figaro stop` left hush agent(s) %v running on %s", left, rig.hushRuntime)
	}
}

// TestHushAgentIsRetiredByStopForce: --force SIGKILLs the daemon, so the
// daemon cannot clean anything up. The requirement is that --force is at
// least as thorough, not less.
func TestHushAgentIsRetiredByStopForce(t *testing.T) {
	hushE2EEnabled(t)
	rig := newHushRig(t, "force")
	before := rig.start(t)
	t.Logf("agent(s) up: %v", before)

	if err := rig.run("stop", "--force"); err != nil {
		t.Fatalf("figaro stop --force: %v", err)
	}
	if left := rig.waitForNoAgent(t, 15*time.Second); len(left) > 0 {
		t.Fatalf("`figaro stop --force` left hush agent(s) %v running on %s", left, rig.hushRuntime)
	}
}

// TestHushAgentSurvivesStopKeepPIDs: --keep-pids says a restart is
// coming. The agent holds an unlocked identity that the next daemon
// would otherwise have to rebuild, so it is exactly what "keep" should
// keep.
func TestHushAgentSurvivesStopKeepPIDs(t *testing.T) {
	hushE2EEnabled(t)
	rig := newHushRig(t, "keep")
	before := rig.start(t)

	if err := rig.run("stop", "--keep-pids"); err != nil {
		t.Fatalf("figaro stop --keep-pids: %v", err)
	}
	// Give a would-be retirement time to happen before concluding it did not.
	time.Sleep(2 * time.Second)
	after := rig.agentPIDs(t)
	if len(after) == 0 {
		t.Fatalf("`figaro stop --keep-pids` retired the agent(s) %v; --keep-pids is the restart gesture and must keep it", before)
	}
}

// TestStopLeavesAnotherFigarosHushAgentAlone is the invariant that makes
// the fix safe: two figaros, two hush surfaces, and stopping one must not
// reach into the other. A stop that re-derived the agent from its own
// environment rather than from what the daemon advertised would pass
// every test above and fail this one.
func TestStopLeavesAnotherFigarosHushAgentAlone(t *testing.T) {
	hushE2EEnabled(t)
	mine := newHushRig(t, "mine")
	theirs := newHushRig(t, "theirs")
	mine.start(t)
	theirsBefore := theirs.start(t)

	if err := mine.run("stop"); err != nil {
		t.Fatalf("figaro stop: %v", err)
	}
	if left := mine.waitForNoAgent(t, 15*time.Second); len(left) > 0 {
		t.Fatalf("stop left my own agent(s) %v running", left)
	}
	if after := theirs.agentPIDs(t); len(after) == 0 {
		t.Fatalf("stopping my figaro retired the OTHER figaro's agent(s) %v", theirsBefore)
	}
}

// TestStopWithoutADaemonRetiresTheOrphan: the agent outlives a daemon
// that was killed rather than stopped, and that is where the orphans on
// this machine came from. A later `figaro stop` finds no daemon; it
// should still retire the agent that daemon left.
func TestStopWithoutADaemonRetiresTheOrphan(t *testing.T) {
	hushE2EEnabled(t)
	rig := newHushRig(t, "orphan")
	pids := rig.start(t)

	// Kill the daemon the rude way, behind the CLI's back.
	pidBytes, err := os.ReadFile(filepath.Join(rig.root, "run", "angelus.pid"))
	if err != nil {
		t.Fatalf("read angelus.pid: %v", err)
	}
	var daemonPID int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(pidBytes)), "%d", &daemonPID); err != nil {
		t.Fatalf("parse angelus.pid: %v", err)
	}
	if err := killPid(daemonPID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill the daemon: %v", err)
	}
	if len(rig.agentPIDs(t)) == 0 {
		t.Fatalf("the agent %v died with the daemon; there is no orphan to measure", pids)
	}

	out := rig.output(t, "stop")
	t.Logf("figaro stop said: %s", strings.TrimSpace(out))
	if left := rig.waitForNoAgent(t, 15*time.Second); len(left) > 0 {
		t.Fatalf("`figaro stop` with no daemon left the orphaned agent(s) %v running", left)
	}
}
