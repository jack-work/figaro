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

	heap := func() uint64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapAlloc
	}
	before := heap()
	light := be.Store().listTrunks()
	afterFirstScan := heap()
	_ = be.Store().listTrunks()
	afterSecondScan := heap()
	rows := be.Conversations()
	afterTopology := heap()
	t.Logf("trunks=%d", len(light))
	t.Logf("first ListLight:  %+.1f MiB   (index hydration, retained across a GC)",
		float64(afterFirstScan-before)/(1<<20))
	t.Logf("second ListLight: %+.1f MiB   (what a repeated `fig ls` costs)",
		float64(afterSecondScan-afterFirstScan)/(1<<20))
	t.Logf("topology build:   %+.1f MiB",
		float64(afterTopology-afterSecondScan)/(1<<20))
	// This is what a listing does per row.
	for i := range rows {
		be.label(&rows[i])
	}
	afterLabels := heap()

	be.mu.Lock()
	forms := len(be.forms)
	be.mu.Unlock()

	t.Logf("rows=%d", len(rows))
	t.Logf("topology total: %+.1f MiB", float64(afterTopology-before)/(1<<20))
	t.Logf("labels:         %+.1f MiB  (%d forms now resident)",
		float64(afterLabels-afterTopology)/(1<<20), forms)
	t.Logf("resident form patches: %d", be.ResidentFormPatches())
	t.Logf("per form: %.1f KiB", float64(afterLabels-afterTopology)/float64(max(forms, 1))/1024)
}
