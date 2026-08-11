package store

// The form/patch read path, weighed on REAL forms.
//
// Synthetic history gets the shape of a board wrong in both directions: a
// benchmark that writes one small key per patch has uniform records and a
// uniform history length, where real arias have a handful of enormous boards
// and a long tail of tiny ones. The question this probe answers is the one a
// synthetic grid cannot: on the boards that actually exist, how long is the
// history a single Send's accessor is drawn from, and what does one warm read
// cost against it.
//
// It is env-gated and points at a COPY, per house law. The angelus takes an
// exclusive flock on <store>/arias/.daemon.lock before opening the backend,
// which makes two daemons contend; it does not make sharing safe, and the
// replay and repair paths can write. Copy. Always copy.
//
//	box=$(mktemp -d); chmod 700 "$box"
//	cp -a --reflink=auto ~/.local/state/figaro/arias "$box/arias"
//	chmod -R go-rwx "$box"
//	FIGARO_PROBE_ROOT=$box/arias go test ./internal/store -run RealFormPatchCost -v
//
// Reads go through accessorRange (formview_bench_test.go), the seam, so this
// same file measures the pre-change tree without an edit.

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"
)

func TestRealFormPatchCost(t *testing.T) {
	root := os.Getenv("FIGARO_PROBE_ROOT")
	if root == "" {
		t.Skip("set FIGARO_PROBE_ROOT to a COPY of a real store (see the doc comment)")
	}
	top := 12
	if v := os.Getenv("FIGARO_PROBE_TOP"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("FIGARO_PROBE_TOP: %v", err)
		}
		top = n
	}

	backend, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	nodes := backend.Nodes()
	t.Logf("store opened: %d nodes", len(nodes))

	type row struct {
		id      string
		kind    string
		history int
	}
	var rows []row
	opened, failed := 0, 0
	for _, n := range nodes {
		v, verr := backend.FormVersion(n.ID)
		if verr != nil {
			failed++
			continue
		}
		opened++
		ps, perr := accessorRange(backend, n.ID, 0, ^uint64(0))
		if perr != nil {
			failed++
			continue
		}
		if len(ps) == 0 {
			continue
		}
		_ = v
		rows = append(rows, row{id: n.ID, kind: n.Kind, history: len(ps)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].history > rows[j].history })
	t.Logf("forms opened: %d, unreadable: %d, with history: %d", opened, failed, len(rows))
	if len(rows) == 0 {
		t.Skip("no readable form history in this store")
	}

	total := 0
	for _, r := range rows {
		total += r.history
	}
	t.Logf("total patches across all boards: %d, mean %.1f, max %d",
		total, float64(total)/float64(len(rows)), rows[0].history)

	if len(rows) > top {
		rows = rows[:top]
	}
	t.Logf("%-12s %-8s %9s %14s %14s %10s",
		"FORM", "KIND", "HISTORY", "DELTA ns/op", "DELTA B/op", "RETURNED")
	for _, r := range rows {
		version, verr := backend.FormVersion(r.id)
		if verr != nil {
			continue
		}
		// One warm Send's question: what changed between the previous stamp
		// and this one. The answer is one patch; the cost of ASKING is the
		// number this probe exists to print.
		var got int
		perOp := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				ps, err := accessorRange(backend, r.id, version-1, version)
				if err != nil {
					b.Fatal(err)
				}
				got = len(ps)
			}
		})
		t.Logf("%-12s %-8s %9d %14d %14d %10d",
			r.id, r.kind, r.history,
			perOp.NsPerOp(), perOp.AllocedBytesPerOp(), got)
	}
}

// TestRealStoreOpens is the cheapest question worth asking of a copy of a real
// store, and the one a refactor of the read path must never stop answering
// yes to: does it open, does every node's board fold, and how long does that
// take cold.
func TestRealStoreOpens(t *testing.T) {
	root := os.Getenv("FIGARO_PROBE_ROOT")
	if root == "" {
		t.Skip("set FIGARO_PROBE_ROOT to a COPY of a real store")
	}
	started := time.Now()
	backend, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer backend.Close()
	openedIn := time.Since(started)

	nodes := backend.Nodes()
	var folded, unreadable int
	var keys int
	replayStart := time.Now()
	for _, n := range nodes {
		if n.ID == "null" {
			continue // the ceremonial genesis root is an anchor, not a board
		}
		snap, serr := backend.FormState(n.ID)
		if serr != nil {
			unreadable++
			t.Logf("unreadable board: %s (%s): %v", n.ID, n.Kind, serr)
			continue
		}
		folded++
		keys += snap.Len()
	}
	t.Logf("opened in %v; %d nodes; %d boards folded (%d keys total), %d unreadable; replay %v",
		openedIn, len(nodes), folded, keys, unreadable, time.Since(replayStart))
	// Pre-existing store damage is a finding, not a regression, and it must
	// not turn this probe into a test nobody can run. On the author's store
	// (2026-08) five conversations answer "index not found" for their form
	// channel, with empty _meta beside them: stillborn arias whose board was
	// never written. They read the same way before and after the view, and
	// they read that way in the LIVE store too, so the copy is not the cause.
	allowed := 0
	if v := os.Getenv("FIGARO_PROBE_ALLOW_UNREADABLE"); v != "" {
		n, aerr := strconv.Atoi(v)
		if aerr != nil {
			t.Fatalf("FIGARO_PROBE_ALLOW_UNREADABLE: %v", aerr)
		}
		allowed = n
	}
	if unreadable > allowed {
		t.Errorf("%d boards did not fold (tolerating %d; set FIGARO_PROBE_ALLOW_UNREADABLE to accept known damage)",
			unreadable, allowed)
	}
	if folded == 0 {
		t.Error("no boards folded: is FIGARO_PROBE_ROOT the arias dir?")
	}
	fmt.Fprintln(os.Stderr)
}
