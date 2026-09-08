package figaro

import (
	"encoding/json"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/store"
)

// The translator reads the LIBRETTO, and a libretto holds its own machinery
// beside the mirror. None of that is the model's business: system.libretto.at
// moves on every fold, and refs moves whenever some OTHER aria studies the
// same form, which would put cross-aria bookkeeping in this aria's context.
//
// alive is the exception, deliberately: a dead source is reported in band, as
// an ordinary key transition, which is what keeps liveness out of the
// projection entirely.
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

	// A fold that moved nothing but bookkeeping renders no block at all.
	if p := withoutBookkeeping(form.Build(form.Snapshot{}, map[string]json.RawMessage{store.KeyLibrettoAt: raw(`42`)}, []string{store.KeyLibrettoRefs})); !p.IsIdentity() {
		t.Errorf("pure bookkeeping rendered %v", p)
	}
}

// The patch handed to the view is the STORE's published value, shared by
// every reader of that log. Stripping in place would edit history, and the
// per-LT cache would make whichever render ran first permanent.
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

// An OUTFIT is a seed, not a subject. It used to be study-able, which
// contradicted durable-forms §12 ("derivations may subscribe only to primary
// forms") in the direction nobody had noticed: an outfit stump is the named
// file a primary form is seeded FROM, and mirroring one would put a template
// under a refcount.
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

// kindBackend answers Node() with one kind and panics on anything else, so
// the test cannot accidentally exercise a path it did not mean to.
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
