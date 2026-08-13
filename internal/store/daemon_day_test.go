package store

// WHAT A DAEMON'S DAY COSTS, which is the number Gluck has actually been
// feeling: not one listing, but a store whose arias have each been visited
// once. Before segment-granular lazy loading, every channel touched left its
// whole raw history in memory; figaro's own bounded caches sat on top of that.
//
// Env-gated, against a COPY (never the real store):
//
//	FIGARO_PROBE_ROOT=$box/arias go test ./internal/store -run DaemonDay -v
//
// FIGARO_PROBE_ARIAS caps how many arias are visited (0 = all).

import (
	"os"
	"runtime"
	"strconv"
	"testing"
)

func TestDaemonDayMemory(t *testing.T) {
	root := os.Getenv("FIGARO_PROBE_ROOT")
	if root == "" {
		t.Skip("set FIGARO_PROBE_ROOT to a COPY of a real store")
	}
	limit := 0
	if v := os.Getenv("FIGARO_PROBE_ARIAS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatal(err)
		}
		limit = n
	}
	be, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()

	heap := func() (alloc, sys int64) {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return int64(m.HeapAlloc), int64(m.HeapSys)
	}
	mib := func(v int64) float64 { return float64(v) / (1 << 20) }
	report := func(phase string) {
		alloc, sys := heap()
		t.Logf("%-26s heap alloc %8.1f MiB   sys %8.1f MiB   loaded-heads %d",
			phase, mib(alloc), mib(sys), be.Store().LoadedHeads())
	}

	report("open")
	rows := be.Conversations()
	report("topology")
	for i := range rows {
		be.label(&rows[i])
		_ = be.LastTS(rows[i].ID)
	}
	report("listing (label+recency)")

	// FIRST, the raw-residency phase: touch every aria's board and its IR
	// counter and decode nothing into figaro's caches. This is figwal's
	// footprint alone, which is what "opening a channel copies its whole
	// history" costs a daemon that has merely LOOKED at its store.
	for i := range rows {
		_, _ = be.FormVersion(rows[i].ID)
	}
	report("after touching every board")

	// The visit: what rendering one aria costs. A page of its IR, its board,
	// and its version -- the three things any client asks for.
	visited, entries := 0, 0
	for i := range rows {
		if limit > 0 && visited >= limit {
			break
		}
		lg, err := be.Open(rows[i].ID)
		if err != nil {
			continue
		}
		page, _ := lg.ReadPage(0, 0, 200)
		entries += len(page)
		if _, err := be.FormState(rows[i].ID); err != nil {
			continue
		}
		if _, err := be.FormVersion(rows[i].ID); err != nil {
			continue
		}
		visited++
	}
	report("after visiting every aria")
	t.Logf("visited %d arias, read %d IR entries", visited, entries)

	// And what a daemon holds once the caches it is allowed to keep are
	// dropped -- the honest floor.
	be.EvictIdle(map[string]bool{}, 0)
	report("after evicting idle")
}
