package angelus

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
)

type ttlFakeBackend struct {
	store.Backend // never called: expireTTL touches only the two below
	due           []store.TTLEntry
	forgotten     []string
}

func (f *ttlFakeBackend) TTLDue(int64) []store.TTLEntry { return f.due }
func (f *ttlFakeBackend) TTLForget(ids ...string)       { f.forgotten = append(f.forgotten, ids...) }

type ttlMockFigaro struct{ id string }

func (m *ttlMockFigaro) ID() string { return m.id }
func (m *ttlMockFigaro) Interrupt() {}
func (m *ttlMockFigaro) Kill()      {}
func (m *ttlMockFigaro) Info() figaro.FigaroInfo {
	return figaro.FigaroInfo{ID: m.id, State: "idle", CreatedAt: time.Now()}
}

func ttlSweepFixture(t *testing.T, due ...string) (*Angelus, *ttlFakeBackend, *[]string) {
	t.Helper()
	be := &ttlFakeBackend{}
	for _, id := range due {
		be.due = append(be.due, store.TTLEntry{
			ID: id, TTL: time.Hour,
			CreatedAtMS: time.Now().Add(-2 * time.Hour).UnixMilli(),
			DeadlineMS:  time.Now().Add(-time.Hour).UnixMilli(),
		})
	}
	removed := &[]string{}
	a := &Angelus{Registry: NewRegistry(), Backend: be}
	a.RemoveAria = func(id string, recursive bool) error {
		if !recursive {
			t.Errorf("the sweep must delete recursively; branches go with their ancestor")
		}
		*removed = append(*removed, id)
		return nil
	}
	return a, be, removed
}

func TestExpireTTLRemovesADormantNode(t *testing.T) {
	a, be, removed := ttlSweepFixture(t, "gone")
	a.expireTTL()
	if len(*removed) != 1 || (*removed)[0] != "gone" {
		t.Fatalf("removed %v, want [gone]", *removed)
	}
	if len(be.forgotten) != 1 || be.forgotten[0] != "gone" {
		t.Errorf("forgotten %v, want [gone]: a deleted node must leave the deadline set",
			be.forgotten)
	}
}

func TestExpireTTLSkipsALiveAria(t *testing.T) {
	a, be, removed := ttlSweepFixture(t, "busy")
	if err := a.Registry.Register(&ttlMockFigaro{id: "busy"}); err != nil {
		t.Fatal(err)
	}
	a.expireTTL()
	if len(*removed) != 0 {
		t.Fatalf("removed %v; an aria with a live agent must survive its deadline", *removed)
	}
	if len(be.forgotten) != 0 {
		t.Errorf("forgotten %v; a skipped node keeps its deadline for the next tick", be.forgotten)
	}
}

func TestExpireTTLSkipsABoundShell(t *testing.T) {
	a, _, removed := ttlSweepFixture(t, "watched")
	if err := a.Registry.Bind(4242, "watched", 0); err != nil {
		t.Fatal(err)
	}
	a.expireTTL()
	if len(*removed) != 0 {
		t.Fatalf("removed %v; a node with a bound shell must survive its deadline", *removed)
	}
}

func TestExpireTTLQuietWhenNothingIsDue(t *testing.T) {
	a, _, removed := ttlSweepFixture(t)
	a.expireTTL()
	if len(*removed) != 0 {
		t.Fatalf("removed %v with nothing due", *removed)
	}
}

func TestExpireTTLIgnoresABackendWithoutLifetimes(t *testing.T) {
	a := &Angelus{Registry: NewRegistry(), Backend: store.NewTestBackend(t)}
	a.RemoveAria = func(string, bool) error {
		t.Error("a backend that keeps no lifetimes must never reach a deletion")
		return nil
	}
	a.expireTTL()
}
