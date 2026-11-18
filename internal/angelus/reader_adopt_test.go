package angelus

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// projected is what a full projection of id's history serves through the
// same shared cache: turns below a fork base fault through the lineage.
func (f *adoptFixture) projected(t *testing.T, id string) []aria.Turn {
	t.Helper()
	fresh := NewAriaReaderBounded(f.backend, f.h.reader().proj, f.a.UICache)
	ra := &readAria{srv: aria.NewServer()}
	ra.srv.BindCache(id, f.a.UICache)
	if err := fresh.restore(id, ra); err != nil {
		t.Fatal(err)
	}
	return ra.srv.Turns()
}

func sameTurns(t *testing.T, what string, got, want []aria.Turn) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d turns, a full projection has %d", what, len(got), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			g, _ := json.Marshal(got[i])
			w, _ := json.Marshal(want[i])
			t.Fatalf("%s: turn %d differs\n got %s\nwant %s", what, want[i].ID, g, w)
		}
	}
}

// A fork's first read starts from the turns of the aria it was forked from,
// resident in the reader, and composes only the last of them and its own
// records. What it serves must be what a full projection of its history
// serves, at the head and at every interior turn.
func TestAForkAdoptsItsParentsTurnsAndServesAFullProjection(t *testing.T) {
	f := newAdoptFixture(t, 9)
	r := f.h.reader()
	if _, _, err := r.serverFor(f.parent); err != nil {
		t.Fatal(err)
	}
	for _, at := range []uint64{0, 2, 5, 9} {
		child := f.fork(t, at)
		srv := aria.NewServer()
		srv.BindCache(child, r.uiCache)
		from, ok := r.adoptFork(child, srv)
		if !ok {
			t.Fatalf("fork at turn %d: nothing adopted from the resident parent", at)
		}
		log, err := f.backend.OpenFigIR(child)
		if err != nil {
			t.Fatal(err)
		}
		srv.AppendSealed(r.project(child, log, from))
		sameTurns(t, fmt.Sprintf("fork at turn %d, adopted", at), srv.Turns(), f.projected(t, child))

		served, _, err := r.serverFor(child)
		if err != nil {
			t.Fatal(err)
		}
		sameTurns(t, fmt.Sprintf("fork at turn %d, served", at), served.Turns(), f.projected(t, child))
	}
}

// A resident server whose log grew recomposes from its newest turn on and
// must still serve what a full projection serves.
func TestAGrownLogRecomposesFromItsNewestTurn(t *testing.T) {
	f := newAdoptFixture(t, 4)
	r := f.h.reader()
	child := f.fork(t, 0)
	if _, _, err := r.serverFor(child); err != nil {
		t.Fatal(err)
	}
	f.write(t, child, 5, 7)
	srv, _, err := r.serverFor(child)
	if err != nil {
		t.Fatal(err)
	}
	sameTurns(t, "after growth", srv.Turns(), f.projected(t, child))
}

// With the parent live and its turns materialized, a reader that holds
// nothing of its own takes the fork's prefix from the agent.
func TestAForkAdoptsFromALiveParent(t *testing.T) {
	f := newAdoptFixture(t, 6)
	live, ok := f.a.Registry.Get(f.parent).(interface {
		Read(aria.Anchor, int) aria.Page
	})
	if !ok {
		t.Fatal("the parent is not live")
	}
	live.Read(aria.Anchor{}, 1<<20)
	child := f.fork(t, 4)
	r := NewAriaReaderBounded(f.backend, f.h.reader().proj, f.a.UICache)
	r.liveTurns = f.h.liveTurns
	r.ForkedFrom(child, f.parent)
	srv := aria.NewServer()
	srv.BindCache(child, r.uiCache)
	from, ok := r.adoptFork(child, srv)
	if !ok {
		t.Fatal("nothing adopted from the live parent")
	}
	log, err := f.backend.OpenFigIR(child)
	if err != nil {
		t.Fatal(err)
	}
	srv.AppendSealed(r.project(child, log, from))
	sameTurns(t, "adopted from the agent", srv.Turns(), f.projected(t, child))
}

// A waking agent takes its turn index from the reader that served it while
// dormant, or from the aria it was forked from, and composes only the newest
// turn; what it then serves must be a full projection.
func TestAWakingAgentAdoptsItsTurns(t *testing.T) {
	f := newAdoptFixture(t, 6)
	r := f.h.reader()
	if _, _, err := r.serverFor(f.parent); err != nil {
		t.Fatal(err)
	}
	read := f.fork(t, 0)
	if _, _, err := r.serverFor(read); err != nil {
		t.Fatal(err)
	}
	unread := f.fork(t, 3)
	for _, id := range []string{read, unread} {
		srv := aria.NewServer()
		srv.BindCache(id, r.uiCache)
		if _, ok := r.SeedAgent(id, srv); !ok {
			t.Fatalf("%s: nothing adopted for the waking agent", id)
		}
		woken, err := f.h.restoreOne(f.ctx, id, "test")
		if err != nil {
			t.Fatal(err)
		}
		live := woken.(interface {
			Read(aria.Anchor, int) aria.Page
			TurnServer() *aria.Server
		})
		live.Read(aria.Anchor{}, 1<<20)
		sameTurns(t, "woken "+id, live.TurnServer().Turns(), f.projected(t, id))
	}
}
