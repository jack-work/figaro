package cli

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The daemon is spawned from one directory and the figaro born in another, so
// pwd's fallback and the figaro's own cwd cannot be confused.
func TestGetAndPwdAgainstARealDaemon(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	rig := newSendRig(t, "http://127.0.0.1:9")
	daemonDir, _ := filepath.EvalSymlinks(t.TempDir())
	work, _ := filepath.EvalSymlinks(t.TempDir())

	in := func(dir string, args ...string) *exec.Cmd {
		c := rig.cmd(args...)
		c.Dir = dir
		return c
	}
	if out, err := in(daemonDir, "form", "ls").CombinedOutput(); err != nil {
		t.Fatalf("form ls, which spawns the daemon: %v\n%s", err, out)
	}
	born, err := in(work, "new", "-f", "-j", "--", "hello").Output()
	if err != nil {
		t.Fatalf("new: %v\n%s", err, born)
	}
	var created struct {
		AriaID string `json:"aria_id"`
	}
	if err := json.Unmarshal(lastLine(born), &created); err != nil || created.AriaID == "" {
		t.Fatalf("new -j printed no id: %q", born)
	}
	id := created.AriaID

	run := func(args ...string) string {
		t.Helper()
		out, err := in(work, args...).Output()
		if err != nil {
			t.Fatalf("figaro %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	rig.must(t, "set", "--id", id, "system.ttl", "3600")

	if got := run("get", "--id", id, "system.cwd"); got != work+"\n" {
		t.Fatalf("get system.cwd = %q, want %q", got, work)
	}
	if got := run(".system.cwd", "--id", id); got != work+"\n" {
		t.Fatalf(".system.cwd = %q", got)
	}
	if got := run("show", "--id", id, ".system.cwd"); got != work+"\n" {
		t.Fatalf("show .system.cwd = %q", got)
	}
	if got := run("get", "--id", id, "system.ttl", "system.cwd"); got != "3600\n"+work+"\n" {
		t.Fatalf("get of two paths = %q", got)
	}
	if got := run("get", "--id", id, "-j", "system.cwd"); got != `"`+work+`"`+"\n" {
		t.Fatalf("get -j = %q", got)
	}
	if got := run("pwd", "--id", id); got != work+"\n" {
		t.Fatalf("pwd = %q, want the figaro's cwd %q", got, work)
	}

	out, err := in(work, "get", "--id", id, "no.such.key").CombinedOutput()
	if code := exitCode(err); code != 1 || !strings.Contains(string(out), "no key no.such.key") {
		t.Fatalf("a missing path: exit %d, %q", code, out)
	}

	rig.must(t, "unset", "--id", id, "system.cwd")
	if got := run("pwd", "--id", id); got != daemonDir+"\n" {
		t.Fatalf("pwd with system.cwd unset = %q, want the daemon's directory %q", got, daemonDir)
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -1
}
