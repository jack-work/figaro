package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/sandbox/local"
)

func leasedBox(t *testing.T) (*config.Loaded, string) {
	t.Helper()
	dir := t.TempDir()
	body := `[authz]
policy = "policy.toml"

[worker.pools.groups]
outfit = "group"
provider = "rou2"
rou2_lease_class = "group"
rou2_url = "http://127.0.0.1:9110"
say_url = "https://broker.example/say"
workers = ["slot0"]
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	name, err := loaded.LeaseBox("groups", "slot0", 1)
	if err != nil {
		t.Fatal(err)
	}
	return loaded, name
}

// A leased box has no socket. Its figaro still has to be born and woken, so
// opening it cannot dial; and nothing may run, read or write through it on
// the host instead, which is what falling back to local would do.
func TestALeasedBoxOpensWithoutASocketAndExecutesNothing(t *testing.T) {
	loaded, name := leasedBox(t)
	host := t.TempDir()
	canary := filepath.Join(host, "canary")
	if err := os.WriteFile(canary, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	box, err := Open(context.Background(), local.NewHost(local.DefaultTTL), loaded, name, "a1", host)
	if err != nil {
		t.Fatalf("a leased box did not open: %v", err)
	}
	if box.Workspace != "" {
		t.Errorf("workspace %q: a leased box has no directory on the host", box.Workspace)
	}
	refusals := map[string]error{}
	_, refusals["exec"] = box.Exec(context.Background(), api.ExecRequest{Command: "true"})
	_, refusals["session start"] = box.Sessions().Start(api.ExecRequest{Command: "true"})
	_, refusals["session get"] = box.Sessions().Get("s1")
	_, refusals["read"] = box.FS().ReadFile(canary)
	_, refusals["stat"] = box.FS().Stat(canary)
	refusals["write"] = box.FS().WriteFile(filepath.Join(host, "new"), []byte("x"), 0o600)
	refusals["mkdir"] = box.FS().MkdirAll(filepath.Join(host, "dir"), 0o700)
	refusals["rename"] = box.FS().Rename(canary, filepath.Join(host, "moved"))
	refusals["update"] = box.FS().Update(canary, func(old []byte) ([]byte, error) { return nil, nil })
	for what, err := range refusals {
		if err == nil || !strings.Contains(err.Error(), "no exec socket") {
			t.Errorf("%s through a leased box: %v", what, err)
		}
	}
	if len(box.Sessions().List()) != 0 {
		t.Error("a leased box lists sessions")
	}
	entries, _ := os.ReadDir(host)
	if len(entries) != 1 || entries[0].Name() != "canary" {
		t.Errorf("the host directory changed: %v", entries)
	}
}

// Killing a figaro reaps its scope in its box. A leased box has none, and a
// dial of its missing socket would log a warning on every release.
func TestKillingAScopeInALeasedBoxDialsNothing(t *testing.T) {
	loaded, name := leasedBox(t)
	n, err := KillBoxScope(context.Background(), loaded, name, "a1")
	if n != 0 || err != nil {
		t.Fatalf("KillBoxScope = %d, %v; want 0, nil", n, err)
	}
}
