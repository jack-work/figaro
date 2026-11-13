package angelus_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/workerpool"
)

const fourWorkerPoolConfig = `
[worker.pools.groups]
enabled = true
outfit = "leased"
provider = "mock"
rou2_lease_class = "group"
rou2_url = "http://127.0.0.1:9110"
say_url = "https://broker.example/groups/say"
workers = ["worker0", "worker1", "worker2", "worker3"]
`

const brokerHolder = "service:broker@auth.kelliher.info"

func ledgerIn(t *testing.T, dir string) *workerpool.Ledger {
	t.Helper()
	l, err := workerpool.OpenLedger(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func leaseTo(t *testing.T, l *workerpool.Ledger, worker, key string, generation uint64, to workerpool.State) {
	t.Helper()
	steps := []func() error{
		func() error { return l.Begin("groups", worker, key, generation, brokerHolder) },
		func() error { return l.GoLive("groups", key, generation, "bearer-of-"+worker) },
		func() error { _, err := l.Release("groups", key, generation); return err },
		func() error { return l.Released("groups", key, generation) },
	}
	reached := []workerpool.State{workerpool.Minting, workerpool.Live, workerpool.Releasing, workerpool.Released}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("%s to %s: %v", worker, reached[i], err)
		}
		if reached[i] == to {
			return
		}
	}
}

// The ledger is the truth a restart reads. A live lease gets its box
// back before any endpoint serves; a lease caught mid-mint or mid-release
// does not, since the release path finishes those; and a released row
// names nothing that still exists.
func TestAtStartOnlyALiveLeaseGetsItsBoxBack(t *testing.T) {
	r := newRigConfigured(t, grantPolicy, fourWorkerPoolConfig)
	dir := t.TempDir()
	before := ledgerIn(t, dir)
	leaseTo(t, before, "worker0", "chat:10", 1, workerpool.Live)
	leaseTo(t, before, "worker1", "chat:11", 1, workerpool.Minting)
	leaseTo(t, before, "worker2", "chat:12", 4, workerpool.Releasing)
	leaseTo(t, before, "worker3", "chat:13", 2, workerpool.Released)

	workers, err := angelus.OpenWorkerPools(dir, r.a.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.a.Settings.LeasedBoxes(); !slices.Equal(got, []string{"worker0-g1"}) {
		t.Fatalf("boxes leased after start: %v, want only the live lease's worker0-g1", got)
	}
	if !workers.Ledger.Found() {
		t.Error("a ledger that was on disk reads as lost")
	}
	for key, want := range map[string]workerpool.State{"chat:10": workerpool.Live, "chat:11": workerpool.Minting, "chat:12": workerpool.Releasing} {
		if l, ok := workers.Ledger.Held("groups", key); !ok || l.State != want {
			t.Errorf("%s after start: %v %v, want %s", key, l.State, ok, want)
		}
	}
	if g, err := workers.Generations.Next("worker0", workers.Ledger.Floor("worker0")); err != nil || g != 2 {
		t.Errorf("the next generation of worker0 is %d (%v), want 2: the counters or the floor were not opened", g, err)
	}
}

// A worker can leave the config while its lease is live. The lease has no
// box to come back to, so start marks it for release and its bearer
// leaves the disk; it must not stay live with nothing behind it.
func TestALiveLeaseWhoseWorkerLeftTheConfigIsMarkedForRelease(t *testing.T) {
	r := newRigConfigured(t, grantPolicy, mockPoolConfig)
	dir := t.TempDir()
	before := ledgerIn(t, dir)
	leaseTo(t, before, "worker0", "chat:10", 3, workerpool.Live)
	leaseTo(t, before, "worker9", "chat:19", 7, workerpool.Live)

	workers, err := angelus.OpenWorkerPools(dir, r.a.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.a.Settings.LeasedBoxes(); !slices.Equal(got, []string{"worker0-g3"}) {
		t.Fatalf("boxes leased after start: %v, want only worker0-g3", got)
	}
	gone, ok := workers.Ledger.Find("groups", "chat:19", 7)
	if !ok || gone.State != workerpool.Releasing || gone.SayBearer != "" {
		t.Errorf("the lease of a worker no longer configured: state %s, bearer kept %v, want releasing with no bearer", gone.State, gone.SayBearer != "")
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, "ledger.json"))
	if err != nil || strings.Contains(string(onDisk), "bearer-of-worker9") {
		t.Errorf("the released lease's bearer is still on disk (%v)", err)
	}
}

// A ledger this build cannot read stops the start: guessing at leases
// would hand a worker out twice. With no ledger at all the daemon starts
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
		if _, err := angelus.OpenWorkerPools(dir, r.a.Settings); err == nil {
			t.Errorf("%s: the pool state opened", name)
		}
	}
	workers, err := angelus.OpenWorkerPools(t.TempDir(), r.a.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if workers.Ledger.Found() || len(r.a.Settings.LeasedBoxes()) != 0 {
		t.Errorf("an empty state dir: found %v, boxes %v", workers.Ledger.Found(), r.a.Settings.LeasedBoxes())
	}
}
