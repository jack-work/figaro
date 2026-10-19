package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	api "github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/sandbox/local"
)

func needBwrap(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap not installed")
	}
	if out, err := exec.Command("bwrap", "--unshare-all", "--ro-bind", "/", "/", "--", "true").CombinedOutput(); err != nil {
		t.Skipf("bwrap cannot sandbox here: %v: %s", err, out)
	}
}

func catInBox(t *testing.T, path string) string {
	t.Helper()
	ws := t.TempDir()
	box, err := Open(context.Background(), local.NewHost(local.DefaultTTL), "bwrap", t.Name(), ws, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { box.Close() })
	var mu sync.Mutex
	var out strings.Builder
	p, err := box.Exec(context.Background(), api.ExecRequest{Command: "cat " + path + "; echo EXIT=$?", OnChunk: func(b []byte) { mu.Lock(); out.Write(b); mu.Unlock() }})
	if err != nil {
		t.Fatal(err)
	}
	p.Wait()
	mu.Lock()
	defer mu.Unlock()
	return out.String()
}

func outsideTmpAndHome(t *testing.T) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	here, _ := filepath.Abs(".")
	for _, base := range []string{"/var/tmp", here} {
		if base == "" || strings.HasPrefix(base, "/tmp") || (home != "" && strings.HasPrefix(base, home)) {
			continue
		}
		dir, err := os.MkdirTemp(base, ".figaro-state-")
		if err != nil {
			continue
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		return dir
	}
	t.Skip("no writable directory outside /tmp and HOME")
	return ""
}

func plantIntermediateKey(t *testing.T, state string) string {
	t.Helper()
	if err := os.MkdirAll(outfit.BundledSkillsRoot(), 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(state, "troupe", "estate")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "intermediate.key")
	if err := os.WriteFile(key, []byte("INTERMEDIATE-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	return key
}

// The node intermediate signs node certificates for the whole troupe; a tool
// in a bwrap box must not read it, wherever the state dir resolved from.
// The default resolution here is XDG_STATE_HOME outside HOME, which no
// masked variable names. Both state dirs sit outside /tmp, which the box
// replaces anyway.
func TestABoxCannotReadTheTroupeKeysInTheDefaultStateDir(t *testing.T) {
	needBwrap(t)
	xdg := outsideTmpAndHome(t)
	t.Setenv("FIGARO_STATE_DIR", "")
	t.Setenv("XDG_STATE_HOME", xdg)
	key := plantIntermediateKey(t, filepath.Join(xdg, "figaro"))
	if out := catInBox(t, key); strings.Contains(out, "INTERMEDIATE-SECRET") || !strings.Contains(out, "EXIT=1") {
		t.Fatalf("a tool read the intermediate key: %q", out)
	}
}

func TestABoxCannotReadTheTroupeKeysInFigaroStateDir(t *testing.T) {
	needBwrap(t)
	state := outsideTmpAndHome(t)
	t.Setenv("FIGARO_STATE_DIR", state)
	key := plantIntermediateKey(t, state)
	if out := catInBox(t, key); strings.Contains(out, "INTERMEDIATE-SECRET") || !strings.Contains(out, "EXIT=1") {
		t.Fatalf("a tool read the intermediate key: %q", out)
	}
}
