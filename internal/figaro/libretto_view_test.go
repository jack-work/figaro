package figaro

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/message"
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

	got := withoutBookkeeping(message.Patch{Set: map[string]json.RawMessage{
		"status":              raw(`"merged"`),
		store.KeyLibrettoAt:   raw(`41`),
		store.KeyLibrettoRefs: raw(`3`),
	}})
	if _, ok := got.Set[store.KeyLibrettoAt]; ok {
		t.Errorf("at survived: %v", got.Set)
	}
	if _, ok := got.Set[store.KeyLibrettoRefs]; ok {
		t.Errorf("refs survived: %v", got.Set)
	}
	if string(got.Set["status"]) != `"merged"` {
		t.Errorf("the mirror did not survive: %v", got.Set)
	}

	alive := withoutBookkeeping(message.Patch{Set: map[string]json.RawMessage{
		store.KeyLibrettoAlive: raw(`false`),
		store.KeyLibrettoAt:    raw(`41`),
	}})
	if string(alive.Set[store.KeyLibrettoAlive]) != `false` {
		t.Errorf("the death was hidden: %v", alive.Set)
	}

	// A fold that moved nothing but bookkeeping renders no block at all.
	if p := withoutBookkeeping(message.Patch{
		Set:    map[string]json.RawMessage{store.KeyLibrettoAt: raw(`42`)},
		Remove: []string{store.KeyLibrettoRefs},
	}); !p.IsEmpty() {
		t.Errorf("pure bookkeeping rendered %v", p)
	}
}

// The patch handed to the view is the STORE's published value, shared by
// every reader of that log. Stripping in place would edit history, and the
// per-LT cache would make whichever render ran first permanent.
func TestStrippingDoesNotEditHistory(t *testing.T) {
	original := message.Patch{
		Set: map[string]json.RawMessage{
			"status":            json.RawMessage(`"merged"`),
			store.KeyLibrettoAt: json.RawMessage(`41`),
		},
		Remove: []string{store.KeyLibrettoRefs, "old"},
	}

	withoutBookkeeping(original)

	if _, ok := original.Set[store.KeyLibrettoAt]; !ok {
		t.Errorf("the store's own patch lost a key: %v", original.Set)
	}
	if len(original.Remove) != 2 {
		t.Errorf("the store's own removes were rewritten: %v", original.Remove)
	}
}
