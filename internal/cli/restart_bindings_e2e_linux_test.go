//go:build linux

package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk"
)

// A shell attended to an aria stays attended across every way the daemon can
// restart. Each case starts a real daemon, binds a real session leader, ends
// the daemon one way, brings the next one up the way that path does, and asks
// the new daemon what the shell is bound to.

type restartRig struct {
	t    *testing.T
	bin  string
	root string
	env  []string
}

func newRestartRig(t *testing.T) *restartRig {
	t.Helper()
	root, err := os.MkdirTemp("/var/tmp", "figaro-restartbind-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	for _, sub := range []string{"run", "state", "cache", "config/outfits", "hush"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("config/config.toml", "default_outfit = \"default\"\n")
	write("config/outfits/default.toml", "[system]\nprovider = \"anthropic\"\nmodel = \"claude-sonnet-4-5\"\n")

	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "FIGARO_") || strings.HasPrefix(k, "ANTHROPIC_") || strings.HasPrefix(k, "HUSH_") || k == "TMUX" {
			continue
		}
		env = append(env, kv)
	}
	r := &restartRig{t: t, bin: smokeBinary(t), root: root, env: append(env,
		"FIGARO_RUNTIME_DIR="+filepath.Join(root, "run"),
		"FIGARO_NO_ANGELUS_SCOPE=1",
		"FIGARO_STATE_DIR="+filepath.Join(root, "state"),
		"FIGARO_CACHE_DIR="+filepath.Join(root, "cache"),
		"FIGARO_CONFIG_DIR="+filepath.Join(root, "config"),
		"FIGARO_HUSH_APP=figaro-test-restartbind",
		"FIGARO_HUSH_DIR="+filepath.Join(root, "hush"),
		"FIGARO_HUSH_PASSPHRASE=figaro-restartbind-e2e",
		"FIGARO_HUSH_TTL=2m",
		"ANTHROPIC_API_KEY=sk-ant-unused",
	)}
	t.Cleanup(func() { _, _ = r.cmd("stop", "--force").CombinedOutput() })
	return r
}

func (r *restartRig) cmd(args ...string) *exec.Cmd {
	c := exec.Command(r.bin, args...)
	c.Env = r.env
	return c
}

func (r *restartRig) run(args ...string) string {
	r.t.Helper()
	out, err := r.cmd(args...).CombinedOutput()
	if err != nil {
		r.t.Fatalf("figaro %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (r *restartRig) mint() string {
	r.t.Helper()
	out := r.run("bind", "null", "-j")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var born struct {
		FigaroID string `json:"figaro_id"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &born); err != nil || born.FigaroID == "" {
		r.t.Fatalf("bind null -j printed no id: %q", out)
	}
	return born.FigaroID
}

// shell is a session leader that attends id and then stays alive, the way a
// terminal's shell does.
func (r *restartRig) shell(id string) int {
	r.t.Helper()
	c := exec.Command("sh", "-c", `"$0" --bind attend "$1" >/dev/null 2>&1 && exec sleep 600`, r.bin, id)
	c.Env = r.env
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() {
		_ = c.Process.Kill()
		_ = c.Wait()
	})
	pid := c.Process.Pid
	deadline := time.Now().Add(15 * time.Second)
	for r.boundTo(pid) != id {
		if time.Now().After(deadline) {
			r.t.Fatalf("the shell (pid %d) never attended %s", pid, id)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return pid
}

func (r *restartRig) socket() string { return filepath.Join(r.root, "run", "angelus.sock") }

func (r *restartRig) boundTo(pid int) string {
	r.t.Helper()
	r.t.Setenv("FIGARO_ARIA", "")
	r.t.Setenv("FIGARO_CALLER", "")
	cli, err := sdk.DialAngelus(transport.UnixEndpoint(r.socket()))
	if err != nil {
		return ""
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := cli.Resolve(ctx, pid)
	if err != nil || !resp.Found {
		return ""
	}
	return resp.FigaroID
}

func (r *restartRig) daemonPID() int {
	r.t.Helper()
	raw, err := os.ReadFile(filepath.Join(r.root, "run", "angelus.pid"))
	if err != nil {
		r.t.Fatalf("angelus.pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		r.t.Fatalf("angelus.pid: %q", raw)
	}
	return pid
}

func (r *restartRig) signalDaemon(sig syscall.Signal) {
	r.t.Helper()
	pid := r.daemonPID()
	if err := syscall.Kill(pid, sig); err != nil {
		r.t.Fatalf("signal the angelus (pid %d): %v", pid, err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			r.t.Fatalf("the angelus (pid %d) outlived %v", pid, sig)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// unit runs the daemon the way the systemd unit does: `figaro --angelus` in
// the foreground of its own process.
func (r *restartRig) unit() {
	r.t.Helper()
	c := r.cmd("--angelus")
	if err := c.Start(); err != nil {
		r.t.Fatal(err)
	}
	go func() { _ = c.Wait() }()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if cli, err := sdk.DialAngelus(transport.UnixEndpoint(r.socket())); err == nil {
			cli.Close()
			return
		}
		if time.Now().After(deadline) {
			r.t.Fatal("the unit's angelus never answered")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (r *restartRig) newBuild() string {
	r.t.Helper()
	raw, err := os.ReadFile(r.bin)
	if err != nil {
		r.t.Fatal(err)
	}
	to := filepath.Join(r.root, "next", "figaro")
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(to, raw, 0o700); err != nil {
		r.t.Fatal(err)
	}
	return to
}

func (r *restartRig) noTmux() string { return "figaro-restartbind-none-" + strconv.Itoa(os.Getpid()) }

func TestEveryRestartKeepsBindings(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns daemons; skipped under -short")
	}
	cases := []struct {
		name    string
		restart func(r *restartRig)
	}{
		{"stop then the next command", func(r *restartRig) {
			r.run("stop")
			r.run("ls", "-j")
		}},
		{"stop -k then the next command", func(r *restartRig) {
			r.run("stop", "-k")
			r.run("ls", "-j")
		}},
		{"stop --force then the next command", func(r *restartRig) {
			r.run("stop", "--force")
			r.run("ls", "-j")
		}},
		{"upgrade", func(r *restartRig) {
			r.run("upgrade", "--to", r.newBuild(), "--tmux-socket", r.noTmux())
		}},
		{"home-manager restartOnSwitch", func(r *restartRig) {
			to := r.newBuild()
			r.run("upgrade", "--to", to, "--if-changed", "--wait", "10", "--tmux-socket", r.noTmux(), "-n")
			r.run("upgrade", "--to", to, "--if-changed", "--wait", "10", "--tmux-socket", r.noTmux())
		}},
		{"systemd unit restart", func(r *restartRig) {
			r.signalDaemon(syscall.SIGTERM)
			r.unit()
		}},
		{"crash", func(r *restartRig) {
			r.signalDaemon(syscall.SIGKILL)
			r.run("ls", "-j")
		}},
		{"crash right after a restart", func(r *restartRig) {
			r.signalDaemon(syscall.SIGKILL)
			r.run("ls", "-j")
			r.signalDaemon(syscall.SIGKILL)
			r.run("ls", "-j")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRestartRig(t)
			id := r.mint()
			pid := r.shell(id)
			before := r.daemonPID()

			tc.restart(r)

			if after := r.daemonPID(); after == before {
				t.Fatalf("the angelus (pid %d) never restarted; this case measures nothing", before)
			}
			if got := r.boundTo(pid); got != id {
				t.Fatalf("after %s the shell (pid %d) is bound to %q, want %s", tc.name, pid, got, id)
			}
		})
	}
}

func TestStopForgetBindingsStartsTheNextDaemonUnattended(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns daemons; skipped under -short")
	}
	r := newRestartRig(t)
	id := r.mint()
	pid := r.shell(id)

	r.run("stop", "--forget-bindings")
	r.run("ls", "-j")

	if got := r.boundTo(pid); got != "" {
		t.Fatalf("stop --forget-bindings left the shell (pid %d) bound to %s", pid, got)
	}
}

// A pane whose own process is figaro has no shell to outlive it: upgrade
// respawns the pane as a new session, and hands the attendance across in the
// environment so the new session leader is bound before it reads its binding.
func TestARespawnedPaneTakesTheAttendanceUpgradeHandsIt(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns daemons; skipped under -short")
	}
	r := newRestartRig(t)
	id := r.mint()

	c := exec.Command("sh", "-c", `"$0" --bind ls -j >/dev/null 2>&1; exec sleep 600`, r.bin)
	c.Env = append(append([]string(nil), r.env...), handedAttendanceEnv+"="+id)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.Process.Kill()
		_ = c.Wait()
	})
	deadline := time.Now().Add(15 * time.Second)
	for r.boundTo(c.Process.Pid) != id {
		if time.Now().After(deadline) {
			t.Fatalf("the respawned session (pid %d) never took the attendance %s", c.Process.Pid, id)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
