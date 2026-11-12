package wire_test

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/hierarchy/hierarchytest"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/topo"
)

func promotedPair(t *testing.T) (b *store.XwalBackend, stump, conv, alt string) {
	t.Helper()
	b, _ = backend(t, true)
	stump, err := b.CreateOutfit("d", patch("system.model", "m"))
	if err != nil {
		t.Fatal(err)
	}
	if conv, err = b.CreateConversation(stump); err != nil {
		t.Fatal(err)
	}
	if _, alt, err = b.Fork(conv); err != nil {
		t.Fatal(err)
	}
	if _, err := hierarchytest.Of(b).Promote(alt, 1); err != nil {
		t.Fatal(err)
	}
	return b, stump, conv, alt
}

func drawnTree(b *store.XwalBackend, ids ...string) map[string]string {
	out := map[string]string{}
	for _, id := range ids {
		n, _ := b.Store().Node(id)
		up := n.Present
		if v, ok := b.Store().Node(up); !ok || v.Kind != store.KindConversation {
			up = "top"
		}
		out[id] = up
	}
	return out
}

// An aria drawn at the root while it reads through its outfit version:
// normalize detaches it, so it reads through nothing, but it stays placed
// under the outfit. The override still says something and stays.
func TestNormalizeKeepsTheOverrideOfAnAriaItDetaches(t *testing.T) {
	b, _ := backend(t, true)
	stump, err := b.CreateOutfit("d", patch("system.model", "m"))
	if err != nil {
		t.Fatal(err)
	}
	conv, err := b.CreateConversation(stump)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.HierarchyEngine().WriteDrawn(map[string]string{conv: "null"}, nil); err != nil {
		t.Fatal(err)
	}
	r, err := b.Normalize(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Detached) != 1 || r.Detached[0] != conv || len(r.Dropped) != 0 {
		t.Fatalf("report %+v, want %s detached and nothing dropped", r, conv)
	}
	if got := b.Store().Tree().Edges(); got[conv] != "null" {
		t.Fatalf("edges after normalize %v, want %s still drawn at the root", got, conv)
	}
	n, _ := b.Store().Node(conv)
	if n.Parent != stump || n.Present != "null" {
		t.Fatalf("%s: placed under %q, drawn under %q, want its outfit %s and the root", conv, n.Parent, n.Present, stump)
	}
	if n.ForkPoint != "null" {
		t.Fatalf("%s still reads through %q after the detach", conv, n.ForkPoint)
	}
	again, err := b.Normalize(false)
	if err != nil || len(again.Detached) != 0 || len(again.Dropped) != 0 {
		t.Fatalf("a second normalize found work: %+v, %v", again, err)
	}
}

// An override naming a form or an outfit version is kept even when the
// history now says the root: it decides which delete sets the aria is in.
func TestNormalizeKeepsAnOverrideToANodeThatIsNotItsHistory(t *testing.T) {
	b, _, _, alt := promotedPair(t)
	r, err := b.Normalize(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Dropped) != 0 {
		t.Fatalf("dropped %+v; an override to the outfit version decides a delete set", r.Dropped)
	}
	if _, ok := b.Store().Tree().Edges()[alt]; !ok {
		t.Fatalf("%s lost its override to the outfit version", alt)
	}
}

// The incident this guards against: an aria drawn at the top (the root,
// "null"), away from the form it was born of, must not fall back into that form's delete set when
// the clean runs, or the form's lifetime takes it.
func TestAnAriaDrawnAwayFromATTLdBirthFormSurvivesItsExpiryAfterAClean(t *testing.T) {
	b, _ := backend(t, true)
	f, _, err := b.CreateForm("", patch("system.model", "m"))
	if err != nil {
		t.Fatal(err)
	}
	x, _, err := b.ForkWith(f, 0, patch("mantra", "x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.HierarchyEngine().WriteDrawn(map[string]string{x: "null"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ApplyFormPrivileged(f, patch("system.ttl", "1ms")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	due := false
	for _, e := range b.TTLDue(time.Now().UnixMilli()) {
		due = due || e.ID == f
	}
	if !due {
		t.Fatal("setup: the birth form's lifetime is not spent")
	}
	if _, err := b.Store().CleanTopology(false); err != nil {
		t.Fatal(err)
	}
	taken := b.DeleteSet(f)
	if slices.Contains(taken, x) {
		t.Fatalf("the form's expiry would take %s, drawn at the top: %v", x, taken)
	}
	if err := hierarchytest.Of(b).RemoveIf(f, taken); err != nil {
		t.Fatal(err)
	}
	if _, err := b.FormState(x); err != nil {
		t.Fatalf("%s did not survive its birth form's expiry: %v", x, err)
	}
}

// The dry run reports exactly what the real run then does.
func TestNormalizeDryRunReportsTheRealRun(t *testing.T) {
	b, _, _, _ := promotedPair(t)
	edges := b.Store().Tree().Edges()
	plan, err := b.Normalize(true)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Store().Tree().Edges(); !reflect.DeepEqual(got, edges) {
		t.Fatalf("the dry run changed the overrides: %v -> %v", edges, got)
	}
	if b.Store().Tree().Normalized() {
		t.Fatal("the dry run detached something")
	}
	done, err := b.Normalize(false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan, done) {
		t.Fatalf("dry run %+v, real run %+v", plan, done)
	}
}

// The store says when the overrides may have drifted from the disk: at boot,
// after any change to either, and not after a clean.
func TestTopologyDirtyFollowsTheDiskAndTheOverrides(t *testing.T) {
	b, stump, conv, alt := promotedPair(t)
	s := b.Store()
	if !s.TopologyDirty() {
		t.Fatal("a store nobody has cleaned must be dirty")
	}
	if _, err := s.CleanTopology(false); err != nil {
		t.Fatal(err)
	}
	if s.TopologyDirty() {
		t.Fatal("dirty straight after a clean")
	}
	if _, err := b.CreateConversation(stump); err != nil {
		t.Fatal(err)
	}
	if !s.TopologyDirty() {
		t.Fatal("a new aria on disk left the topology clean")
	}
	if _, err := s.CleanTopology(false); err != nil {
		t.Fatal(err)
	}
	if err := hierarchytest.Of(b).Arrange(map[string]string{conv: ""}, []string{alt}); err != nil {
		t.Fatal(err)
	}
	if !s.TopologyDirty() {
		t.Fatal("an arrangement left the topology clean")
	}
}

// The clean writes nothing when what it judged has moved on: an arrangement
// that lands mid-pass is kept whole.
func TestPruneRefusesWhenAnArrangementLandsMidPass(t *testing.T) {
	b, _, conv, alt := promotedPair(t)
	tree := b.Store().Tree()
	_, _, err := tree.Prune(func(edges map[string]string) []topo.Stale {
		if err := b.HierarchyEngine().WriteDrawn(nil, []string{conv}); err != nil {
			t.Fatal(err)
		}
		return []topo.Stale{{ID: alt, Reason: topo.StaleHistoryAgrees}}
	}, false)
	if err == nil {
		t.Fatal("prune wrote over an arrangement it did not see")
	}
	if _, ok := tree.Edges()[alt]; !ok {
		t.Fatal("prune dropped an override after refusing")
	}
	if _, ok := tree.Edges()[conv]; ok {
		t.Fatal("the arrangement that landed mid-pass was lost")
	}
}
