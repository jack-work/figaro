package cli

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func (r *hushRig) daemonPIDs(t *testing.T) []int {
	t.Helper()
	runtimeDir := "FIGARO_RUNTIME_DIR=" + filepath.Join(r.root, "run")
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
		daemon, mine := false, false
		for _, v := range vars {
			daemon = daemon || v == "_FIGARO_DAEMON=1"
			mine = mine || v == runtimeDir
		}
		if daemon && mine {
			pids = append(pids, pid)
		}
	}
	return pids
}

// The daemon is spawned while the hush agent is still unlocking, so a failed
// unlock must take the daemon down with it, as when hush ran first.
func TestAHushFailureLeavesNoDaemonBehind(t *testing.T) {
	hushE2EEnabled(t)
	rig := newHushRig(t, "gate")
	rig.start(t)
	if err := rig.run("stop", "--force"); err != nil {
		t.Fatalf("figaro stop --force: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rig.hushDir, "config", "identity.age"), []byte("not an identity\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := rig.run("list")
	if err == nil {
		t.Fatal("figaro list succeeded with an identity that cannot unlock")
	}
	if !strings.Contains(err.Error(), "hush") {
		t.Fatalf("the failure does not name hush:\n%v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for len(rig.daemonPIDs(t)) > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if left := rig.daemonPIDs(t); len(left) > 0 {
		t.Fatalf("daemon(s) %v outlived the hush failure", left)
	}
	if _, err := os.Stat(filepath.Join(rig.root, "run", "angelus.sock")); err == nil {
		t.Fatal("the daemon bound its socket although hush failed")
	}
	log, _ := os.ReadFile(filepath.Join(rig.root, "run", "angelus.startup"))
	if !strings.Contains(string(log), "could not start the hush agent") {
		t.Fatalf("the daemon did not say why it left; angelus.startup:\n%s", log)
	}
}

func dialable(path string) bool {
	c, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// A daemon behind a shut gate serves nothing: not the CLI socket, not the web
// endpoint, not a box endpoint, from spawn until it exits.
func TestNoEndpointAnswersBeforeTheHushGateOpens(t *testing.T) {
	hushE2EEnabled(t)
	rig := newHushRig(t, "endpoints")
	endpoints := map[string]string{
		"cli": filepath.Join(rig.root, "run", "angelus.sock"),
		"web": filepath.Join(rig.root, "web.sock"),
		"box": filepath.Join(rig.root, "box.sock"),
	}
	cfg := fmt.Sprintf("[endpoint.web]\npath = %q\nauthn = \"upstream\"\n[authz.upstream]\nissuer = \"https://auth.example\"\n[sandbox.boxes.b]\nexec = %q\nendpoint = %q\noutfits = [\"*\"]\nproviders = [\"*\"]\n",
		endpoints["web"], filepath.Join(rig.root, "box-exec.sock"), endpoints["box"])
	if err := os.WriteFile(filepath.Join(rig.root, "config", "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	rig.start(t)
	for name, p := range endpoints {
		if !dialable(p) {
			t.Fatalf("with the gate open the %s endpoint %s does not answer: this test cannot see an endpoint", name, p)
		}
	}
	if err := rig.run("stop", "--force"); err != nil {
		t.Fatalf("figaro stop --force: %v", err)
	}
	for _, p := range endpoints {
		os.Remove(p)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(rig.bin)
	cmd.Env = append(append([]string{}, rig.env...), "_FIGARO_DAEMON=1", "_FIGARO_HUSH_GATE=3")
	cmd.ExtraFiles = []*os.File{r}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r.Close()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			t.Fatalf("the daemon exited while its gate was still shut (%v):\n%s", err, stderr.String())
		default:
		}
		for name, p := range endpoints {
			if dialable(p) {
				w.Close()
				t.Fatalf("the %s endpoint %s answered before the hush gate opened", name, p)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	w.Close()
	select {
	case err := <-exited:
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			t.Fatalf("a shut gate should end the daemon with exit 1, got %v:\n%s", err, stderr.String())
		}
	case <-time.After(15 * time.Second):
		cmd.Process.Kill()
		t.Fatal("the daemon did not exit after its gate closed shut")
	}
	for name, p := range endpoints {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("the %s endpoint %s was bound although the gate never opened", name, p)
		}
	}
}
