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

// `figaro stop` retires the hush agent the daemon spawned, witnessed in /proc.
//
//	FIGARO_HUSH_E2E=1 go test ./internal/cli -run TestHushAgent -count=1 -v
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

// hushRig is one isolated figaro, sharing nothing with the user's.
type hushRig struct {
	bin     string
	root    string
	env     []string
	hushDir string
	// The agent child carries hushRuntime in HUSH_MANAGED_RUNTIME_DIR; it is
	// what distinguishes this rig's agent from every other on the machine.
	hushRuntime string
}

func newHushRig(t *testing.T, name string) *hushRig {
	t.Helper()
	return newHushRigSharing(t, name, "")
}

func newHushRigSharing(t *testing.T, name, shareHush string) *hushRig {
	t.Helper()
	// /var/tmp rather than t.TempDir(): /tmp is tmpfs.
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

	hushDir := filepath.Join(root, "hush")
	if shareHush != "" {
		hushDir = shareHush
	}
	rig := &hushRig{
		bin:         smokeBinary(t),
		root:        root,
		hushDir:     hushDir,
		hushRuntime: filepath.Join(hushDir, "run"),
	}
	rig.env = append(os.Environ(),
		"FIGARO_RUNTIME_DIR="+filepath.Join(root, "run"),
		"FIGARO_STATE_DIR="+filepath.Join(root, "state"),
		"FIGARO_CONFIG_DIR="+filepath.Join(root, "config"),
		"FIGARO_HUSH_APP=figaro-test-hushstop",
		"FIGARO_HUSH_DIR="+hushDir,
		"FIGARO_HUSH_PASSPHRASE=figaro-hushstop-e2e",
		"FIGARO_HUSH_TTL=2m",
	)
	t.Cleanup(func() {
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

// agentPIDs matches on HUSH_MANAGED_RUNTIME_DIR: every agent on the machine
// is the same binary with the same argv.
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
			continue
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

func (r *hushRig) start(t *testing.T) []int {
	t.Helper()
	if err := r.run("list"); err != nil {
		t.Fatalf("bring the daemon up: %v", err)
	}
	pids := r.waitForAgent(t, 30*time.Second)
	if len(pids) == 0 {
		t.Fatalf("no hush agent appeared for %s within 30s; this test cannot measure anything", r.hushRuntime)
	}
	return pids
}

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

// --force SIGKILLs the daemon, so the daemon cannot clean up for itself.
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

// --keep-pids says a restart is coming; the unlocked identity stays up for it.
func TestHushAgentSurvivesStopKeepPIDs(t *testing.T) {
	hushE2EEnabled(t)
	rig := newHushRig(t, "keep")
	before := rig.start(t)

	if err := rig.run("stop", "--keep-pids"); err != nil {
		t.Fatalf("figaro stop --keep-pids: %v", err)
	}
	time.Sleep(2 * time.Second)
	after := rig.agentPIDs(t)
	if len(after) == 0 {
		t.Fatalf("`figaro stop --keep-pids` retired the agent(s) %v; --keep-pids is the restart gesture and must keep it", before)
	}
}

// A stop that derived the agent from its own environment rather than from
// what the daemon advertised would pass every test above and fail this one.
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

// A later `figaro stop` finds no daemon and still retires the agent it left.
func TestStopWithoutADaemonRetiresTheOrphan(t *testing.T) {
	hushE2EEnabled(t)
	rig := newHushRig(t, "orphan")
	pids := rig.start(t)

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

// Two daemons on one hush surface: stopping one leaves the agent to the other.
func TestStopLeavesASharedHushAgentToTheOtherDaemon(t *testing.T) {
	hushE2EEnabled(t)
	live := newHushRig(t, "live")
	livePIDs := live.start(t)
	sandbox := newHushRigSharing(t, "sandbox", live.hushDir)
	if err := sandbox.run("list"); err != nil {
		t.Fatalf("bring the second daemon up: %v", err)
	}
	shared := sandbox.waitForAgent(t, 30*time.Second)
	if len(shared) == 0 {
		t.Fatal("the second daemon never reached the shared agent")
	}
	if fmt.Sprint(shared) != fmt.Sprint(livePIDs) {
		t.Fatalf("the two daemons are not sharing an agent (%v vs %v); this test measures nothing", livePIDs, shared)
	}

	out := sandbox.output(t, "stop")
	t.Logf("sandbox stop said: %q", strings.TrimSpace(out))
	if !strings.Contains(out, "still in use") {
		t.Errorf("stop did not say it was leaving a shared agent alone: %q", out)
	}
	time.Sleep(2 * time.Second)
	if after := live.agentPIDs(t); len(after) == 0 {
		t.Fatalf("stopping the sandbox shut down the agent %v that the live daemon is using", livePIDs)
	}

	if err := live.run("stop"); err != nil {
		t.Fatalf("figaro stop: %v", err)
	}
	if left := live.waitForNoAgent(t, 15*time.Second); len(left) > 0 {
		t.Fatalf("the last daemon left the shared agent(s) %v running", left)
	}
}
