package store

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"testing"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store/xwal"
)

// TestMain silences figwal's INFO segment/log chatter (it logs via the
// default slog logger) so benchmark output is readable. Re-enable with
// FIGARO_LOG_LEVEL set.
func TestMain(m *testing.M) {
	// The crash test re-enters this binary as a child (form_crash_test.go),
	// which has to happen before any test runs.
	if dir := os.Getenv(crashChildEnv); dir != "" {
		crashChild(dir)
		return
	}
	if os.Getenv("FIGARO_LOG_LEVEL") == "" {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	os.Exit(m.Run())
}

// seedTree builds a representative aria tree: `stumps` outfits, each with
// `convs` top-level conversations, each conversation forked `branches` times
// (so the tree has real lineage/depth, not a flat list). Every trunk gets a
// couple of real turns so it has a non-empty head + form to open.
func seedTree(tb testing.TB, b *XwalBackend, stumps, convs, branches int) int {
	tb.Helper()
	total := 0
	for si := 0; si < stumps; si++ {
		l, err := b.CreateOutfit(fmt.Sprintf("outfit%d", si), patchSet(map[string]string{
			"system.model": "m",
			"system.credo": "be terse",
			"outfit_field": fmt.Sprintf("v%d", si),
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
	ir, err := b.OpenFigIR(id)
	if err != nil {
		tb.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := ir.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleInput}}); err != nil {
			tb.Fatal(err)
		}
	}
	if err := b.SetMeta(id, &AriaMeta{MessageCount: n, TokensIn: 10}); err != nil {
		tb.Fatal(err)
	}
}

// TestNodes_TrunkScanCount pins the topology snapshot: one List + one Stumps
// on first use, then no tree scans while Trunks.Version is unchanged.
func TestNodes_TrunkScanCount(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
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
	trunkScanCount.Store(0)
	_ = b.store.Nodes()
	if got := trunkScanCount.Load(); got != 0 {
		t.Fatalf("warm Nodes() did %d trunk scans, want 0", got)
	}
}

func TestConversationList_TrunkScanCount(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	seedTree(t, b, 4, 3, 2)

	trunkScanCount.Store(0)
	nodes := b.store.Conversations()
	if got := trunkScanCount.Load(); got != 2 {
		t.Fatalf("cold Conversations() did %d trunk scans, want exactly 2", got)
	}
	if len(nodes) != 36 {
		t.Fatalf("Conversations() returned %d nodes, want 36", len(nodes))
	}

	trunkScanCount.Store(0)
	ids := b.store.ConversationIDs()
	if got := trunkScanCount.Load(); got != 0 {
		t.Fatalf("warm ConversationIDs() did %d trunk scans, want 0", got)
	}
	if len(ids) != len(nodes) {
		t.Fatalf("ConversationIDs() returned %d ids, want %d", len(ids), len(nodes))
	}

	all := b.store.Nodes()
	if got := trunkScanCount.Load(); got != 0 {
		t.Fatalf("warm Nodes() did %d trunk scans, want 0", got)
	}
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
			t.Fatalf("conversation view for %s differs from full tree: got %#v, want %#v", n.ID, n, w)
		}
	}

	var outfitID string
	for _, n := range all {
		if n.Kind == string(kindOutfit) {
			outfitID = n.ID
			break
		}
	}
	created, err := b.CreateConversation(outfitID)
	if err != nil {
		t.Fatal(err)
	}
	trunkScanCount.Store(0)
	updated := b.store.Conversations()
	if got := trunkScanCount.Load(); got != 2 {
		t.Fatalf("Conversations() after topology change did %d scans, want 2", got)
	}
	if len(updated) != len(nodes)+1 {
		t.Fatalf("Conversations() after topology change returned %d nodes, want %d", len(updated), len(nodes)+1)
	}
	if _, ok := b.store.Node(created); !ok {
		t.Fatalf("new conversation %s absent from rebuilt topology", created)
	}
	if got := trunkScanCount.Load(); got != 2 {
		t.Fatalf("warm Node() rescanned topology: scans=%d", got)
	}
}

// BenchmarkNodes measures store.Nodes() (the tree fill) and reports the
// trunk-scan count as a custom metric so the fan-out is visible numerically.
func BenchmarkNodes(b *testing.B) {
	be, err := NewXwalBackend(b.TempDir(), 0)
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
	be, err := NewXwalBackend(b.TempDir(), 0)
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

// BenchmarkListPathFill is the angelus list path's tree fill: one
// Backend.Nodes() snapshot, indexed by id, however many rows follow.
//
// It used to be a pair, bracketing the O(N^2) it replaced -- the handler
// called Backend.Node(id) per result and each call recomputed the whole
// tree. The "before" arm is gone: the topology snapshot is cached by
// version now, so the pattern it measured no longer costs what it cost, and a
// benchmark whose premise has expired reports a number nobody can read.
func BenchmarkListPathFill(b *testing.B) {
	be, err := NewXwalBackend(b.TempDir(), 0)
	if err != nil {
		b.Fatal(err)
	}
	defer be.Close()
	seedTree(b, be, 4, 3, 2)
	ids := convIDs(be)

	trunkScanCount.Store(0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nodes := map[string]NodeView{} // one snapshot, indexed by id
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

func BenchmarkFormState10000(b *testing.B) {
	be, err := NewXwalBackend(b.TempDir(), 0)
	if err != nil {
		b.Fatal(err)
	}
	defer be.Close()
	outfit, err := be.CreateOutfit("perf", patchSet(map[string]string{"system.model": "m"}))
	if err != nil {
		b.Fatal(err)
	}
	id, err := be.CreateConversation(outfit)
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 10_000; i++ {
		if _, err := be.ApplyForm(id, patchSet(map[string]string{
			fmt.Sprintf("key%d", i%100): fmt.Sprintf("value%d", i),
		})); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := be.FormState(id); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFormPatches10000 is gone with the API it measured. It timed
// "copy the whole patch history", which was never a question the provider
// asked: a translate asks what changed between two stamps. Its successors,
// which measure that question in both shapes, are BenchmarkFormDeltaPerSend*
// and BenchmarkFormWholePerSend* in formview_bench_test.go.

func BenchmarkVectors10000Branches(b *testing.B) {
	infos := make([]xwal.TrunkInfo, 10_000)
	for i := range infos {
		infos[i].ID = fmt.Sprintf("trunk-%05d", i)
		if i > 0 {
			infos[i].Parent = infos[(i-1)/4].ID
		}
	}
	s := &XwalStore{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := len(s.vectorsLocked(infos)); got != len(infos) {
			b.Fatalf("vectors = %d, want %d", got, len(infos))
		}
	}
}
