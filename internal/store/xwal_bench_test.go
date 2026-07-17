package store

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

// TestMain silences figwal's INFO segment/log chatter (it logs via the
// default slog logger) so benchmark output is readable. Re-enable with
// FIGARO_LOG_LEVEL set.
func TestMain(m *testing.M) {
	if os.Getenv("FIGARO_LOG_LEVEL") == "" {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	os.Exit(m.Run())
}

// seedTree builds a representative aria forest: `stumps` loadouts, each with
// `convs` top-level conversations, each conversation forked `branches` times
// (so the tree has real lineage/depth, not a flat list). Every trunk gets a
// couple of real turns so it has a non-empty head + chalkboard to open.
func seedTree(tb testing.TB, b *XwalBackend, stumps, convs, branches int) int {
	tb.Helper()
	total := 0
	for si := 0; si < stumps; si++ {
		l, err := b.CreateLoadout(fmt.Sprintf("loadout%d", si), patchSet(map[string]string{
			"system.model":  "m",
			"system.credo":  "be terse",
			"loadout_field": fmt.Sprintf("v%d", si),
		}))
		if err != nil {
			tb.Fatal(err)
		}
		for ci := 0; ci < convs; ci++ {
			conv, err := b.CreateConversation(l)
			if err != nil {
				tb.Fatal(err)
			}
			turn(tb, b, conv, 2)
			total++
			for bi := 0; bi < branches; bi++ {
				_, alt, err := b.Fork(conv)
				if err != nil {
					tb.Fatal(err)
				}
				turn(tb, b, alt, 2)
				total++
			}
		}
	}
	return total
}

func turn(tb testing.TB, b *XwalBackend, id string, n int) {
	tb.Helper()
	ir, err := b.Open(id)
	if err != nil {
		tb.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := ir.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleUser}}); err != nil {
			tb.Fatal(err)
		}
	}
	if err := b.SetMeta(id, &AriaMeta{MessageCount: n, TokensIn: 10}); err != nil {
		tb.Fatal(err)
	}
}

// TestNodes_TrunkScanCount pins the per-request disk-scan fan-out of a single
// store.Nodes() call. Before the fix Nodes() invoked trunks.List() twice and
// trunks.Stumps() once (3 full scans); after the fix it is exactly one List
// + one Stumps (2 scans), independent of tree size. The handler's per-entry
// Node() calls (the O(N^2) blowup) are gone — angelus snapshots Nodes() once.
func TestNodes_TrunkScanCount(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	seedTree(t, b, 4, 3, 2) // 4 stumps x 3 convs x (1 + 2 branches) = 36 trunks

	trunkScanCount.Store(0)
	_ = b.store.Nodes()
	if got := trunkScanCount.Load(); got != 2 {
		t.Fatalf("Nodes() did %d trunk scans, want exactly 2 (1 List + 1 Stumps)", got)
	}
}

func TestConversationList_TrunkScanCount(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	seedTree(t, b, 4, 3, 2)

	trunkScanCount.Store(0)
	nodes := b.store.Conversations()
	if got := trunkScanCount.Load(); got != 1 {
		t.Fatalf("Conversations() did %d trunk scans, want exactly 1", got)
	}
	if len(nodes) != 36 {
		t.Fatalf("Conversations() returned %d nodes, want 36", len(nodes))
	}

	trunkScanCount.Store(0)
	ids := b.store.ConversationIDs()
	if got := trunkScanCount.Load(); got != 1 {
		t.Fatalf("ConversationIDs() did %d trunk scans, want exactly 1", got)
	}
	if len(ids) != len(nodes) {
		t.Fatalf("ConversationIDs() returned %d ids, want %d", len(ids), len(nodes))
	}

	all := b.store.Nodes()
	want := make(map[string]NodeView, len(nodes))
	for _, n := range all {
		if n.Kind == string(kindConversation) {
			want[n.ID] = n
		}
	}
	for _, n := range nodes {
		w, ok := want[n.ID]
		if !ok || n.Parent != w.Parent || n.Trunk != w.Trunk ||
			n.BranchedLT != w.BranchedLT || !slices.Equal(n.Vector, w.Vector) {
			t.Fatalf("conversation view for %s differs from full forest: got %#v, want %#v", n.ID, n, w)
		}
	}
}

// BenchmarkNodes measures store.Nodes() (the forest fill) and reports the
// trunk-scan count as a custom metric so the fan-out is visible numerically.
func BenchmarkNodes(b *testing.B) {
	be, err := NewXwalBackend(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer be.Close()
	n := seedTree(b, be, 4, 3, 2)

	trunkScanCount.Store(0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = be.store.Nodes()
	}
	b.StopTimer()
	b.ReportMetric(float64(trunkScanCount.Load())/float64(b.N), "scans/op")
	b.ReportMetric(float64(n), "trunks")
}

func BenchmarkConversations(b *testing.B) {
	be, err := NewXwalBackend(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer be.Close()
	n := seedTree(b, be, 4, 3, 2)

	trunkScanCount.Store(0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = be.store.Conversations()
	}
	b.StopTimer()
	b.ReportMetric(float64(trunkScanCount.Load())/float64(b.N), "scans/op")
	b.ReportMetric(float64(n), "trunks")
}

func BenchmarkCanonicalCountCached(b *testing.B) {
	be, err := NewXwalBackend(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer be.Close()
	l, err := be.CreateLoadout("d", patchSet(map[string]string{"system.model": "m"}))
	if err != nil {
		b.Fatal(err)
	}
	id, err := be.CreateConversation(l)
	if err != nil {
		b.Fatal(err)
	}
	turn(b, be, id, 1000)
	if _, ok := be.CanonicalCount(id); !ok {
		b.Fatal("initial canonical count failed")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := be.CanonicalCount(id); !ok {
			b.Fatal("canonical count failed")
		}
	}
}

// BenchmarkListPathAfter / BenchmarkListPathBefore bracket the angelus list
// path's forest fill. BEFORE: the handler called Backend.Node(id) once per
// result, and EACH Node() recomputed the whole forest (1 List + 1 Stumps + a
// vectorsLocked List) — O(N) full scans => O(N^2) trunk-head opens. AFTER:
// the handler snapshots Backend.Nodes() ONCE and indexes by id — a single
// forest fill regardless of N. These benchmarks reproduce both call patterns
// over the same seeded tree so the win is provable.
func BenchmarkListPathBefore(b *testing.B) {
	be, err := NewXwalBackend(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer be.Close()
	seedTree(b, be, 4, 3, 2)
	ids := convIDs(be)

	trunkScanCount.Store(0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = be.Nodes()           // backend.List() / first forest fill
		for _, id := range ids { // OLD: per-entry Backend.Node => O(N) full scans
			_, _ = be.Node(id)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(trunkScanCount.Load())/float64(b.N), "scans/op")
	b.ReportMetric(float64(len(ids)), "trunks")
}

func BenchmarkListPathAfter(b *testing.B) {
	be, err := NewXwalBackend(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer be.Close()
	seedTree(b, be, 4, 3, 2)
	ids := convIDs(be)

	trunkScanCount.Store(0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nodes := map[string]NodeView{} // NEW: one snapshot, indexed by id
		for _, n := range be.Nodes() {
			nodes[n.ID] = n
		}
		for _, id := range ids {
			_ = nodes[id]
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(trunkScanCount.Load())/float64(b.N), "scans/op")
	b.ReportMetric(float64(len(ids)), "trunks")
}

func convIDs(be *XwalBackend) []string {
	var ids []string
	for _, n := range be.Nodes() {
		if n.Kind == string(kindConversation) {
			ids = append(ids, n.ID)
		}
	}
	return ids
}

// BenchmarkBackendList measures the full Backend.List() path (Nodes + the
// per-aria meta read) — the call the daemon makes for `fig ls` dormant arias.
func BenchmarkBackendList(b *testing.B) {
	be, err := NewXwalBackend(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer be.Close()
	seedTree(b, be, 4, 3, 2)

	trunkScanCount.Store(0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := be.List(); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(trunkScanCount.Load())/float64(b.N), "scans/op")
}
