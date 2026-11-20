package angelus_test

import (
	"testing"

	"github.com/jack-work/figaro/internal/authz"
)

// Every method a door serves is declared in authz's action table. A handler
// registered without an entry would be refused at the guard; this names it
// before a caller meets the refusal.
func TestEveryServedMethodIsDeclared(t *testing.T) {
	r := newGrantRig(t)
	if len(r.hs.Map) < 60 {
		t.Fatalf("the door serves %d methods; the walk is broken", len(r.hs.Map))
	}
	for m := range r.hs.Map {
		if _, ok := authz.ActionOf(m); !ok {
			t.Errorf("%s is served and authz declares no action for it", m)
		}
	}
}
