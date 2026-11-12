package angelus_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

// The outfit is checked when the pool loads, at start and at every reload,
// and a pool whose outfit is refused stays unloaded until one passes.
func TestAPoolLoadsOnlyWithAnOutfitThatPasses(t *testing.T) {
	r := newRigConfigured(t, grantPolicy, mockPoolConfig)
	if _, err := r.hs.PoolOutfitHash("groups"); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("a pool whose outfit has no file loaded: %v", err)
	}
	path := filepath.Join(filepath.Dir(r.policy), "outfits", "leased.toml")
	reload := func(body string) (string, error) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.h[rpc.MethodConfigure](asOwner(), json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		return r.hs.PoolOutfitHash("groups")
	}
	const good = "[system]\nprovider = \"mock\"\nmodel = \"m\"\ntools = [\"say\"]\n"
	first, err := reload(good)
	if err != nil || first == "" {
		t.Fatalf("a pool whose outfit passes did not load: %q, %v", first, err)
	}
	if _, err := reload(good + "cwd = \"/home/gluck\"\n"); err == nil || !strings.Contains(err.Error(), "system.cwd") {
		t.Fatalf("a reload kept the pool loaded on an outfit that names a host path: %v", err)
	}
	if again, err := reload(good); err != nil || again != first {
		t.Fatalf("the outfit put back pins %q, want %q (%v)", again, first, err)
	}
	if _, err := r.hs.PoolOutfitHash("nowhere"); err == nil {
		t.Fatal("a pool the config lacks has an outfit")
	}
}
