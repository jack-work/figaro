package angelus

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/config"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
)

type ttlFakeBackend struct {
	store.Backend // never called: expireTTL touches only the methods below
	due           []store.TTLEntry
	forgotten     []string
	subtree       map[string][]string
	metas         map[string]*store.AriaMeta
	lastTS        map[string]int64
	roles         map[string]string
}

func (f *ttlFakeBackend) TTLDue(int64) []store.TTLEntry { return f.due }
func (f *ttlFakeBackend) TTLForget(ids ...string)       { f.forgotten = append(f.forgotten, ids...) }
func (f *ttlFakeBackend) DeleteSet(id string) []string {
	return append([]string{id}, f.subtree[id]...)
}
func (f *ttlFakeBackend) Meta(id string) (*store.AriaMeta, error) { return f.metas[id], nil }
func (f *ttlFakeBackend) LastTS(id string) int64                  { return f.lastTS[id] }
func (f *ttlFakeBackend) Forms() []store.NodeView {
	var out []store.NodeView
	for role := range f.roles {
		out = append(out, store.NodeView{ID: role})
	}
	return out
}
func (f *ttlFakeBackend) FormState(id string) (form.Snapshot, error) {
	raw, _ := json.Marshal(f.roles[id])
	return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": raw}, nil)), nil
}

type ttlMockFigaro struct{ id string }

func (m *ttlMockFigaro) ID() string { return m.id }
func (m *ttlMockFigaro) Interrupt() {}
func (m *ttlMockFigaro) Kill()      {}
func (m *ttlMockFigaro) Info() figaro.FigaroInfo {
	return figaro.FigaroInfo{ID: m.id, State: "idle", CreatedAt: time.Now()}
}

func ttlSweepFixture(t *testing.T, due ...string) (*Angelus, *ttlFakeBackend, *[]string) {
	t.Helper()
	be := &ttlFakeBackend{
		subtree: map[string][]string{}, metas: map[string]*store.AriaMeta{},
		lastTS: map[string]int64{}, roles: map[string]string{},
	}
	for _, id := range due {
		be.due = append(be.due, store.TTLEntry{
			ID: id, TTL: time.Hour,
			CreatedAtMS: time.Now().Add(-2 * time.Hour).UnixMilli(),
			DeadlineMS:  time.Now().Add(-time.Hour).UnixMilli(),
		})
	}
	removed := &[]string{}
	hardDelete := 0
	a := &Angelus{Registry: NewRegistry(), Backend: be, Settings: &config.Loaded{
		Config: config.Config{Memory: config.MemoryConfig{ReapedGraceHours: &hardDelete}},
	}}
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

func hoursAgo(h int) int64 { return time.Now().Add(-time.Duration(h) * time.Hour).UnixMilli() }

// The loss this lane exists for: a node used yesterday afternoon must not be
// swept because its birth deadline passed.
func TestExpireTTLKeepsANodeAccessedWithinTheKeepalive(t *testing.T) {
	a, _, removed := ttlSweepFixture(t, "recent")
	a.Backend.(*ttlFakeBackend).lastTS["recent"] = hoursAgo(3)
	a.expireTTL()
	if len(*removed) != 0 {
		t.Fatalf("removed %v; a node written 3h ago is inside the default 1d keepalive", *removed)
	}
}

func TestExpireTTLTakesANodeOnceItsKeepaliveRunsOut(t *testing.T) {
	a, _, removed := ttlSweepFixture(t, "stale")
	be := a.Backend.(*ttlFakeBackend)
	be.lastTS["stale"] = hoursAgo(25)
	a.expireTTL()
	if len(*removed) != 1 {
		t.Fatalf("removed %v; a node idle past its keepalive and deadline must go", *removed)
	}
}

func TestExpireTTLCountsAReadLikeAWrite(t *testing.T) {
	a, _, removed := ttlSweepFixture(t, "read")
	be := a.Backend.(*ttlFakeBackend)
	be.due[0].LastAccessMS = hoursAgo(2)
	a.expireTTL()
	if len(*removed) != 0 {
		t.Fatalf("removed %v; an attend 2h ago is an access", *removed)
	}
}

func TestExpireTTLRetiredIgnoresTheDefaultKeepalive(t *testing.T) {
	a, _, removed := ttlSweepFixture(t, "tomb")
	be := a.Backend.(*ttlFakeBackend)
	be.due[0].RetiredAtMS = hoursAgo(1)
	be.lastTS["tomb"] = hoursAgo(1)
	a.expireTTL()
	if len(*removed) != 1 {
		t.Fatalf("removed %v; a retired node with a spent ttl is the tombstone's point", *removed)
	}
}

// A delete takes the subtree; the sweep must ask about all of it.
func TestExpireTTLKeepsAParentWhoseChildIsInUse(t *testing.T) {
	cases := map[string]func(a *Angelus, be *ttlFakeBackend){
		"child accessed within its keepalive": func(_ *Angelus, be *ttlFakeBackend) {
			be.lastTS["child"] = hoursAgo(5)
		},
		"child's own access stamp": func(_ *Angelus, be *ttlFakeBackend) {
			be.metas["child"] = &store.AriaMeta{LastAccessMS: hoursAgo(5)}
		},
		"child has a live agent": func(a *Angelus, _ *ttlFakeBackend) {
			if err := a.Registry.Register(&ttlMockFigaro{id: "child"}); err != nil {
				t.Fatal(err)
			}
		},
		"child has a bound shell": func(a *Angelus, _ *ttlFakeBackend) {
			if err := a.Registry.Bind(4343, "child", 0); err != nil {
				t.Fatal(err)
			}
		},
		"a role points at the child": func(_ *Angelus, be *ttlFakeBackend) {
			be.lastTS["child"] = hoursAgo(72)
			be.roles["@coordinador"] = "child"
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			a, be, removed := ttlSweepFixture(t, "parent")
			be.subtree["parent"] = []string{"child"}
			be.lastTS["parent"] = hoursAgo(72)
			arrange(a, be)
			a.expireTTL()
			if len(*removed) != 0 {
				t.Fatalf("removed %v; the delete would take a child in use", *removed)
			}
		})
	}
}

func TestExpireTTLTakesAParentWithIdleChildren(t *testing.T) {
	a, be, removed := ttlSweepFixture(t, "parent")
	be.subtree["parent"] = []string{"child"}
	be.lastTS["parent"] = hoursAgo(72)
	be.lastTS["child"] = hoursAgo(48)
	a.expireTTL()
	if len(*removed) != 1 {
		t.Fatalf("removed %v; nothing under the parent is in use", *removed)
	}
}
