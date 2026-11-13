package angelus_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/slotpool"
)

const fourSlotPoolConfig = `
[slot.pools.groups]
enabled = true
outfit = "leased"
provider = "mock"
rou2_lease_class = "group"
rou2_url = "http://127.0.0.1:9110"
say_url = "https://broker.example/groups/say"
slots = ["slot0", "slot1", "slot2", "slot3"]
`

const brokerHolder = "service:broker@auth.kelliher.info"

func ledgerIn(t *testing.T, dir string) *slotpool.Ledger {
	t.Helper()
	l, err := slotpool.OpenLedger(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func leaseTo(t *testing.T, l *slotpool.Ledger, slot, key string, generation uint64, to slotpool.State) {
	t.Helper()
	steps := []func() error{
		func() error { return l.Begin("groups", slot, key, generation, brokerHolder) },
		func() error { return l.GoLive("groups", key, generation, "bearer-of-"+slot) },
		func() error { _, err := l.Release("groups", key, generation); return err },
		func() error { return l.Released("groups", key, generation) },
	}
	reached := []slotpool.State{slotpool.Minting, slotpool.Live, slotpool.Releasing, slotpool.Released}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("%s to %s: %v", slot, reached[i], err)
		}
		if reached[i] == to {
			return
		}
	}
}

// The ledger is the truth a restart reads. A live lease gets its box
// back before any door serves; a lease caught mid-mint or mid-release
// does not, since the release path finishes those; and a released row
// names nothing that still exists.
func TestAtStartOnlyALiveLeaseGetsItsBoxBack(t *testing.T) {
	r := newRigConfigured(t, grantPolicy, fourSlotPoolConfig)
	dir := t.TempDir()
	before := ledgerIn(t, dir)
	leaseTo(t, before, "slot0", "chat:10", 1, slotpool.Live)
	leaseTo(t, before, "slot1", "chat:11", 1, slotpool.Minting)
	leaseTo(t, before, "slot2", "chat:12", 4, slotpool.Releasing)
	leaseTo(t, before, "slot3", "chat:13", 2, slotpool.Released)

	slots, err := angelus.OpenSlotPools(dir, r.a.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.a.Settings.LeasedBoxes(); !slices.Equal(got, []string{"slot0-g1"}) {
		t.Fatalf("boxes leased after start: %v, want only the live lease's slot0-g1", got)
	}
	if !slots.Ledger.Found() {
		t.Error("a ledger that was on disk reads as lost")
	}
	for key, want := range map[string]slotpool.State{"chat:10": slotpool.Live, "chat:11": slotpool.Minting, "chat:12": slotpool.Releasing} {
		if l, ok := slots.Ledger.Held("groups", key); !ok || l.State != want {
			t.Errorf("%s after start: %v %v, want %s", key, l.State, ok, want)
		}
	}
	if g, err := slots.Generations.Next("slot0", slots.Ledger.Floor("slot0")); err != nil || g != 2 {
		t.Errorf("the next generation of slot0 is %d (%v), want 2: the counters or the floor were not opened", g, err)
	}
}

// A slot can leave the config while its lease is live. The lease has no
// box to come back to, so start marks it for release and its bearer
// leaves the disk; it must not stay live with nothing behind it.
func TestALiveLeaseWhoseSlotLeftTheConfigIsMarkedForRelease(t *testing.T) {
	r := newRigConfigured(t, grantPolicy, mockPoolConfig)
	dir := t.TempDir()
	before := ledgerIn(t, dir)
	leaseTo(t, before, "slot0", "chat:10", 3, slotpool.Live)
	leaseTo(t, before, "slot9", "chat:19", 7, slotpool.Live)

	slots, err := angelus.OpenSlotPools(dir, r.a.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.a.Settings.LeasedBoxes(); !slices.Equal(got, []string{"slot0-g3"}) {
		t.Fatalf("boxes leased after start: %v, want only slot0-g3", got)
	}
	gone, ok := slots.Ledger.Find("groups", "chat:19", 7)
	if !ok || gone.State != slotpool.Releasing || gone.SayBearer != "" {
		t.Errorf("the lease of a slot no longer configured: state %s, bearer kept %v, want releasing with no bearer", gone.State, gone.SayBearer != "")
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, "ledger.json"))
	if err != nil || strings.Contains(string(onDisk), "bearer-of-slot9") {
		t.Errorf("the released lease's bearer is still on disk (%v)", err)
	}
}

// A ledger this build cannot read stops the start: guessing at leases
// would hand a slot out twice. With no ledger at all the daemon starts
// empty, and says the ledger was not found so recovery can run.
func TestALedgerThisBuildCannotReadStopsTheStart(t *testing.T) {
	r := newRigConfigured(t, grantPolicy, mockPoolConfig)
	for name, body := range map[string]string{
		"not json":        "{",
		"another version": `{"version":2,"leases":[],"pages":[]}`,
		"an unknown key":  `{"version":1,"route":"x"}`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "ledger.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := angelus.OpenSlotPools(dir, r.a.Settings); err == nil {
			t.Errorf("%s: the pool state opened", name)
		}
	}
	slots, err := angelus.OpenSlotPools(t.TempDir(), r.a.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if slots.Ledger.Found() || len(r.a.Settings.LeasedBoxes()) != 0 {
		t.Errorf("an empty state dir: found %v, boxes %v", slots.Ledger.Found(), r.a.Settings.LeasedBoxes())
	}
}
