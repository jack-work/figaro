package angelus

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

type feedTopologyReads struct {
	*store.XwalBackend
	inFeed atomic.Bool
	fed    atomic.Int32
	reads  atomic.Int32
}

func (b *feedTopologyReads) Node(id string) (store.NodeView, bool) {
	if b.inFeed.Load() {
		b.reads.Add(1)
	}
	return b.XwalBackend.Node(id)
}

func (b *feedTopologyReads) Nodes() []store.NodeView {
	if b.inFeed.Load() {
		b.reads.Add(1)
	}
	return b.XwalBackend.Nodes()
}

func (b *feedTopologyReads) watched(f func()) {
	b.inFeed.Store(true)
	defer b.inFeed.Store(false)
	b.fed.Add(1)
	f()
}

func (b *feedTopologyReads) Feed(f store.TopologyFeed) {
	born, limits := f.Born, f.Limits
	f.Born = func(id, parent string) { b.watched(func() { born(id, parent) }) }
	f.Limits = func(id string) { b.watched(func() { limits(id) }) }
	b.XwalBackend.Feed(f)
}

func TestABirthChargesItsQuotaWithoutRebuildingTheTopology(t *testing.T) {
	_, xb, outfit := demoteHandlers(t)
	b := &feedTopologyReads{XwalBackend: xb}
	a := &Angelus{Registry: NewRegistry(), Backend: b, Hubs: newHubs()}
	hs := NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: inProcess(context.Background())})
	top, _ := xb.CreateConversation(outfit)
	setQuota(t, xb, top, 5)
	budgetOf(hs, top)
	b.fed.Store(0)
	kid := forkOK(t, hs, top, top, quotaPatch(2))
	if b.fed.Load() == 0 {
		t.Fatal("the fork fed the admission cache nothing: the test watches no birth")
	}
	if n := b.reads.Load(); n != 0 {
		t.Fatalf("the birth of %s read the whole topology %d times from the admission feed; each read rebuilds it under the store lock", kid, n)
	}
	if got := budgetOf(hs, kid); !got.capped || got.quota != 2 {
		t.Fatalf("the child's budget = %+v, want capped at 2", got)
	}
	if got := budgetOf(hs, top); got.used != 3 {
		t.Fatalf("top's budget = %+v, want 3 used (the child and its grant)", got)
	}
}
