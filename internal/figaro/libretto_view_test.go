package figaro

import (
	"encoding/json"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/store"
)

// The libretto's bookkeeping keys stay out of the model's context; alive is
// the exception, so a dead source is reported in band.
func TestBookkeepingNeverReachesTheModel(t *testing.T) {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }

	got := withoutBookkeeping(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"status":              raw(`"merged"`),
		store.KeyLibrettoAt:   raw(`41`),
		store.KeyLibrettoRefs: raw(`3`),
	}, nil))
	if _, ok := got.Entry(store.KeyLibrettoAt); ok {
		t.Errorf("at survived: %v", got.Entries())
	}
	if _, ok := got.Entry(store.KeyLibrettoRefs); ok {
		t.Errorf("refs survived: %v", got.Entries())
	}
	if string(mustEntry(got, "status")) != `"merged"` {
		t.Errorf("the mirror did not survive: %v", got.Entries())
	}

	alive := withoutBookkeeping(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		store.KeyLibrettoAlive: raw(`false`),
		store.KeyLibrettoAt:    raw(`41`),
	}, nil))
	if string(mustEntry(alive, store.KeyLibrettoAlive)) != `false` {
		t.Errorf("the death was hidden: %v", alive.Entries())
	}

	if p := withoutBookkeeping(form.Build(form.Snapshot{}, map[string]json.RawMessage{store.KeyLibrettoAt: raw(`42`)}, []string{store.KeyLibrettoRefs})); !p.IsIdentity() {
		t.Errorf("pure bookkeeping rendered %v", p)
	}
}

// The patch is the store's published value; stripping in place edits history.
func TestStrippingDoesNotEditHistory(t *testing.T) {
	original := form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"status":            json.RawMessage(`"merged"`),
		store.KeyLibrettoAt: json.RawMessage(`41`),
	}, []string{store.KeyLibrettoRefs, "old"})

	withoutBookkeeping(original)

	if _, ok := original.Entry(store.KeyLibrettoAt); !ok {
		t.Errorf("the store's own patch lost a key: %v", original.Entries())
	}
	if n := countRemovals(original); n != 2 {
		t.Errorf("the store's own removes were rewritten: %d, %v", n, original.Entries())
	}
}

// An outfit stump is a seed, not a subject: it may not be studied.
func TestStudyRefusesAnOutfitByName(t *testing.T) {
	a := &Agent{backend: kindBackend{kind: "outfit"}, id: "a1"}
	err := a.requireStudyTarget("@seed")
	if err == nil {
		t.Fatal("studying an outfit was allowed")
	}
	if !strings.Contains(err.Error(), "outfit, not a form") {
		t.Fatalf("the refusal does not name the outfit: %v", err)
	}
	if err := (&Agent{backend: kindBackend{kind: "form"}, id: "a1"}).requireStudyTarget("@f"); err != nil {
		t.Fatalf("an unbound form was refused: %v", err)
	}
}

// kindBackend panics on anything but Node(), so the test cannot exercise another path by accident.
type kindBackend struct {
	store.Backend
	kind string
}

func (b kindBackend) Node(id string) (store.NodeView, bool) {
	return store.NodeView{ID: id, Kind: b.kind}, true
}

func countRemovals(p message.Patch) int {
	n := 0
	for _, e := range p.Entries() {
		if e.IsRemoval() {
			n++
		}
	}
	return n
}
