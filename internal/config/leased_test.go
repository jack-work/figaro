package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const pool = "chat-groups"

func loadedPool(t *testing.T) *Loaded {
	t.Helper()
	l, err := loadBody(t, poolBody)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// Every rule that keys on a box name reads Loaded.Box, so a lease has to be
// found there, and with nothing a process could execute through or a door
// could be opened on.
func TestALeasedBoxIsABoxWithNoSocketAndNoGrace(t *testing.T) {
	l := loadedPool(t)
	if _, ok := l.Box("slot0-g7"); ok {
		t.Fatal("a box nobody leased was found")
	}
	if _, any := l.ShortestBoxPurgeGrace(); any {
		t.Fatal("a pool with no lease has a box purge grace")
	}
	name, err := l.LeaseBox(pool, "slot0", 7)
	if err != nil {
		t.Fatal(err)
	}
	if name != "slot0-g7" {
		t.Fatalf("leased %q, want slot0-g7", name)
	}
	b, ok := l.Box(name)
	if !ok {
		t.Fatal("the leased box is not found by Box")
	}
	if !slices.Equal(b.Outfits, []string{"broker-group"}) || !slices.Equal(b.Providers, []string{"rou2"}) {
		t.Errorf("outfits %v providers %v: want the pool's one of each", b.Outfits, b.Providers)
	}
	if b.Exec != "" || b.Door != "" || b.HostDoor != "" || b.FigaroClaim != "" || b.Profile != "" {
		t.Errorf("a leased box carries a socket, door, claim or profile: %+v", b)
	}
	if len(l.BoxDoors())+len(l.HostDoors())+len(l.DooredBoxes())+len(l.BoxesRequiringClaim()) != 0 {
		t.Error("the door machinery sees a leased box")
	}
	if !slices.Equal(l.LeasedBoxes(), []string{name}) {
		t.Errorf("leased boxes %v", l.LeasedBoxes())
	}

	l.ReleaseBox(name)
	if _, ok := l.Box(name); ok {
		t.Error("a released box is still found")
	}
	if _, ok := l.BoxPurgeGrace(name); ok {
		t.Error("a released box still answers a purge grace")
	}
}

// A kill asks BoxPurgeGrace how long the figaro stays restorable. With no
// answer it gets the daemon's days of grace, and a released group's text
// would sit in the store that long.
func TestALeasedBoxIsPurgedWithNoGrace(t *testing.T) {
	l := loadedPool(t)
	name, err := l.LeaseBox(pool, "slot1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := l.BoxPurgeGrace(name); !ok || d != 0 {
		t.Errorf("purge grace of a leased box = %v, %v; want 0, true", d, ok)
	}
	if d, ok := l.ShortestBoxPurgeGrace(); !ok || d != 0 {
		t.Errorf("shortest box purge grace with a lease live = %v, %v; want 0, true", d, ok)
	}
}

// The handlers swap in a freshly loaded config on every reload. The leases
// are not in the file, so the fresh config has to be handed the same table,
// or a reload would turn every live lease into an unknown box.
func TestALeaseOutlivesAReload(t *testing.T) {
	l := loadedPool(t)
	before, err := l.LeaseBox(pool, "slot0", 3)
	if err != nil {
		t.Fatal(err)
	}
	off := strings.Replace(poolBody, "enabled = true", "enabled = false", 1)
	if err := os.WriteFile(filepath.Join(l.ConfigDir, "config.toml"), []byte(off), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh, err := l.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := fresh.Pool(pool); p.Enabled {
		t.Fatal("the reload did not read the file again")
	}
	if _, ok := fresh.Box(before); !ok {
		t.Fatal("the reloaded config lost a live lease")
	}
	after, err := fresh.LeaseBox(pool, "slot1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Box(after); !ok {
		t.Error("a lease made after the reload is unknown to a holder of the config from before it")
	}
	fresh.ReleaseBox(before)
	if _, ok := l.Box(before); ok {
		t.Error("a release after the reload is unknown to a holder of the config from before it")
	}
}

// owner:self reads minted_by, and a lease's figaros are minted as its box. A
// name leased twice would hand the second lease what the first one minted.
func TestAWorkerHoldsOneLeaseAndNeverTheSameGenerationTwice(t *testing.T) {
	l := loadedPool(t)
	refused := func(worker string, generation uint64, want string) {
		t.Helper()
		_, err := l.LeaseBox(pool, worker, generation)
		if err == nil {
			t.Fatalf("%s generation %d was leased", worker, generation)
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%s generation %d refused for another reason than %q: %v", worker, generation, want, err)
		}
	}
	first, err := l.LeaseBox(pool, "slot0", 7)
	if err != nil {
		t.Fatal(err)
	}
	refused("slot0", 8, "still holds slot0-g7")
	l.ReleaseBox(first)
	refused("slot0", 7, "never used twice")
	refused("slot0", 6, "never used twice")
	if _, err := l.LeaseBox(pool, "slot0", 8); err != nil {
		t.Fatalf("the next generation after a release: %v", err)
	}
	if _, err := l.LeaseBox(pool, "slot1", 7); err != nil {
		t.Fatalf("another slot at a generation the first slot used: %v", err)
	}
}

func TestOnlyAPoolsOwnWorkerIsLeased(t *testing.T) {
	l := loadedPool(t)
	for name, c := range map[string]struct {
		pool, worker string
		generation   uint64
		want         string
	}{
		"a pool the config lacks": {"other", "slot0", 1, "no pool"},
		"a slot the pool lacks":   {pool, "slot9", 1, "not a slot of pool"},
		"a lease-shaped slot":     {pool, "slot0-g1", 1, "not a slot of pool"},
		"generation zero":         {pool, "slot0", 0, "starts at 1"},
	} {
		if _, err := l.LeaseBox(c.pool, c.worker, c.generation); err == nil {
			t.Errorf("%s: leased", name)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: refused for another reason than %q: %v", name, c.want, err)
		}
	}
	if len(l.LeasedBoxes()) != 0 {
		t.Errorf("a refused lease left a box: %v", l.LeasedBoxes())
	}
}

// A daemon that is not a pool daemon has no table, so nothing can put a box
// with no socket beside its real ones.
func TestADaemonWithoutAPoolLeasesNoBox(t *testing.T) {
	plain, err := loadBody(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.LeaseBox(pool, "slot0", 1); err == nil {
		t.Error("a config with no pool leased a box")
	}
	built := &Loaded{Config: loadedPool(t).Config}
	if _, err := built.LeaseBox(pool, "slot0", 1); err == nil || !strings.Contains(err.Error(), "no leased-box table") {
		t.Errorf("a config that was never loaded leased a box: %v", err)
	}
}
