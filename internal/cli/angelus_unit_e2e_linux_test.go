//go:build linux

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The unit lookup is wired into ensureAngelus, not just tested beside it: a
// client whose runtime dir a stopped unit serves names that unit instead of
// reaching the self-spawn refusal. systemctl is a fake on PATH, so the box's
// own units are never touched, and FIGARO_NO_SELF_SPAWN keeps a client that
// skips the lookup from spawning anything.
func TestEnsureAngelusConsultsTheUnitBeforeSpawning(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary; skipped under -short")
	}
	root := t.TempDir()
	run := filepath.Join(root, "run")
	calls := filepath.Join(root, "systemctl.calls")
	fake := filepath.Join(root, "bin")
	if err := os.MkdirAll(fake, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
echo "$*" >> "` + calls + `"
case "$*" in
*show-environment*) exit 0 ;;
*"show -p"*) printf 'Id=figaro-fake.service\nLoadState=loaded\nActiveState=inactive\nSubState=dead\nMainPID=0\nEnvironment=FIGARO_RUNTIME_DIR=` + run + `\n' ;;
*) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(fake, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "FIGARO_") || k == "PATH" {
			continue
		}
		env = append(env, kv)
	}
	cmd := exec.Command(smokeBinary(t), "ls", "-j")
	cmd.Env = append(env,
		"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FIGARO_RUNTIME_DIR="+run,
		"FIGARO_STATE_DIR="+filepath.Join(root, "state"),
		"FIGARO_CACHE_DIR="+filepath.Join(root, "cache"),
		"FIGARO_CONFIG_DIR="+filepath.Join(root, "config"),
		"FIGARO_SYSTEMD_UNIT=figaro-fake.service",
		"FIGARO_NO_SELF_SPAWN=1",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("a client with no daemon and a stopped unit exited 0:\n%s", out)
	}
	if !strings.Contains(string(out), "figaro-fake.service is inactive") {
		t.Fatalf("the client never named the unit serving its runtime dir:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(run, "angelus.sock")); err == nil {
		t.Fatal("a daemon came up")
	}
	log, _ := os.ReadFile(calls)
	for _, line := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		for _, f := range strings.Fields(line) {
			if f == "start" {
				t.Fatalf("started the unit under FIGARO_NO_SELF_SPAWN: %q", line)
			}
		}
	}
}
