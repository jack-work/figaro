package bwrap

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/figaro/internal/sandbox/local"
)

func open(t *testing.T) (sandbox.Sandbox, string) {
	t.Helper()
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap not installed")
	}
	if out, err := exec.Command("bwrap", "--unshare-all", "--share-net", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--", "true").CombinedOutput(); err != nil {
		t.Skipf("bwrap cannot sandbox here (user namespaces disabled?): %v: %s", err, out)
	}
	ws := t.TempDir()
	box, err := Provider{Host: local.NewHost(local.DefaultTTL), Scope: t.Name()}.Open(context.Background(), sandbox.Spec{Workspace: ws, Env: map[string]string{"FIGARO_SPEC": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	return box, ws
}

func run(t *testing.T, box sandbox.Sandbox, command string) (int, string) {
	t.Helper()
	var mu sync.Mutex
	var out strings.Builder
	p, err := box.Exec(context.Background(), sandbox.ExecRequest{Command: command, OnChunk: func(b []byte) { mu.Lock(); out.Write(b); mu.Unlock() }})
	if err != nil {
		t.Fatal(err)
	}
	res := p.Wait()
	mu.Lock()
	defer mu.Unlock()
	return res.ExitCode, strings.TrimSpace(out.String())
}

// Each claim in Isolation() is probed from inside the box, and each probe is
// first shown to succeed on the host so a pass means the box, not a typo.
func TestIsolationIsTruthful(t *testing.T) {
	cfg := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FIGARO_CONFIG_DIR", cfg)
	t.Setenv("FIGARO_CANARY", "leaked")
	box, ws := open(t)
	iso := box.Isolation()
	if iso.Provider != "bwrap" || !iso.FS || !iso.Env || !iso.Secrets || iso.UID || iso.Net {
		t.Fatalf("unexpected claims %+v", iso)
	}

	if code, out := run(t, box, "pwd"); code != 0 || out != ws {
		t.Fatalf("pwd = %q (exit %d), want %s", out, code, ws)
	}
	if code, _ := run(t, box, "echo hi > inside && cat inside"); code != 0 {
		t.Fatal("cannot write inside the workspace")
	}

	// Secrets: the config dir is a mount on the host and empty inside.
	if _, err := os.ReadFile(filepath.Join(cfg, "config.toml")); err != nil {
		t.Fatal("fixture: config.toml unreadable on the host")
	}
	if code, out := run(t, box, "cat "+filepath.Join(cfg, "config.toml")+"; echo EXIT=$?"); !strings.Contains(out, "EXIT=1") || strings.Contains(out, "secret") {
		t.Fatalf("config.toml readable inside the box (exit %d): %s", code, out)
	}
	home, _ := os.UserHomeDir()
	if entries, _ := os.ReadDir(home); len(entries) == 0 {
		t.Fatal("fixture: $HOME is empty on the host")
	}
	if _, out := run(t, box, "ls -A "+home); out != "" {
		t.Fatalf("$HOME visible inside the box: %s", out)
	}

	// Env: the daemon's environment does not reach the box.
	if _, out := run(t, box, "printenv FIGARO_CANARY FIGARO_SPEC"); out != "1" {
		t.Fatalf("env leaked or spec env missing: %q", out)
	}

	// FS: writes land only in the workspace.
	if code, _ := run(t, box, "touch /var/tmp/figaro-bwrap-escape"); code == 0 {
		os.Remove("/var/tmp/figaro-bwrap-escape")
		t.Fatal("wrote outside the workspace")
	}
	if _, err := box.FS().ReadFile("../x"); !errors.Is(err, sandbox.ErrOutsideWorkspace) {
		t.Fatalf("ReadFile(../x) = %v, want ErrOutsideWorkspace", err)
	}
	if err := os.Symlink("/etc", filepath.Join(ws, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := box.FS().ReadFile("link/hostname"); !errors.Is(err, sandbox.ErrOutsideWorkspace) {
		t.Fatalf("ReadFile through a symlink = %v, want ErrOutsideWorkspace", err)
	}
	if err := box.FS().WriteFile("new/dir/file", []byte("x"), 0o644); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("WriteFile under a missing dir = %v, want a plain ENOENT, not an escape", err)
	}
	if err := box.FS().MkdirAll("new/dir", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := box.FS().Stat(filepath.Join(ws, "new", "dir")); err != nil {
		t.Fatalf("absolute path inside the workspace refused: %v", err)
	}
}
