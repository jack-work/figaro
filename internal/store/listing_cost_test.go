package store

// What a LISTING costs, which nothing measured.
//
// `fig ls` renders an OUTFIT column, and a node with no stump answers it from
// its own board, so labelOf calls FormState, which opens the Form and leaves
// it in the registry. The row cache is bounded and reported; the form
// registry is neither, and a listing walks every row in the store.
//
// Env-gated, against a COPY (see realform_probe_test.go):
//
//	FIGARO_PROBE_ROOT=$box/arias go test ./internal/store -run ListingCost -v

import (
	"os"
	"runtime"
	"strconv"
	"testing"
)

func TestListingCost(t *testing.T) {
	root := os.Getenv("FIGARO_PROBE_ROOT")
	if root == "" {
		t.Skip("set FIGARO_PROBE_ROOT to a COPY of a real store")
	}
	be, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()

	// int64, and signed deltas: HeapAlloc can fall between two readings (a GC
	// collects more than the step allocated), and an unsigned subtraction
	// turns that into 17592186044416 MiB of nonsense. I printed exactly that
	// for three runs before noticing.
	heap := func() int64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return int64(m.HeapAlloc)
	}
	mib := func(d int64) float64 { return float64(d) / (1 << 20) }
	// figwal's payload budget is the layer under every figure below: the
	// probe can be run at another one to see the trade directly.
	if mb := os.Getenv("FIGARO_PROBE_SEGCACHE_MB"); mb != "" {
		n, err := strconv.Atoi(mb)
		if err != nil {
			t.Fatal(err)
		}
		SetSegmentCacheBudget(int64(n) << 20)
	}
	t.Logf("segment cache budget: %.1f MiB", mib(SegmentCacheBudget()))
	before := heap()
	light := be.Store().listTrunks()
	afterFirstScan := heap()
	_ = be.Store().listTrunks()
	afterSecondScan := heap()
	rows := be.Conversations()
	afterTopology := heap()
	t.Logf("trunks=%d", len(light))
	t.Logf("first ListLight:  %+.1f MiB   (index hydration, retained across a GC)",
		mib(afterFirstScan-before))
	t.Logf("second ListLight: %+.1f MiB   (what a repeated `fig ls` costs)",
		mib(afterSecondScan-afterFirstScan))
	t.Logf("topology build:   %+.1f MiB",
		mib(afterTopology-afterSecondScan))
	// This is what a listing does per row: the OUTFIT column and recency.
	for i := range rows {
		be.label(&rows[i])
		_ = be.LastTS(rows[i].ID)
	}
	afterLabels := heap()
	headsAfterFirst := be.Store().LoadedHeads()
	t.Logf("figwal segment cache: %.1f MiB held after the listing",
		mib(SegmentCacheBytes()))

	be.mu.Lock()
	forms := len(be.forms)
	be.mu.Unlock()

	// THE STEADY STATE, which is what a status line on a timer produces:
	// forms evicted for idleness, then a listing. Before the label memo, the
	// second listing re-opened every form and re-hydrated every node.
	be.EvictIdle(map[string]bool{}, 0)
	evicted := heap()
	rows2 := be.Conversations()
	for i := range rows2 {
		be.label(&rows2[i])
		_ = be.LastTS(rows2[i].ID)
	}
	afterSecond := heap()
	be.mu.Lock()
	forms2 := len(be.forms)
	be.mu.Unlock()
	t.Logf("after evicting every form: %.1f MiB heap", mib(evicted))
	t.Logf("first listing:  loaded heads %d", headsAfterFirst)
	t.Logf("SECOND listing:   %+.1f MiB  (%d forms re-opened, loaded heads %d)",
		mib(afterSecond-evicted), forms2, be.Store().LoadedHeads())

	t.Logf("rows=%d", len(rows))
	t.Logf("topology total: %+.1f MiB", mib(afterTopology-before))
	t.Logf("labels:         %+.1f MiB  (%d forms now resident)",
		mib(afterLabels-afterTopology), forms)
	t.Logf("resident form patches: %d", be.ResidentFormPatches())
	t.Logf("per form: %.1f KiB", mib(afterLabels-afterTopology)*1024/float64(max(forms, 1)))
}
