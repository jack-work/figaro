package angelus

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func (r *narrowRig) rewriteZ(set map[string]json.RawMessage, drop ...string) {
	r.t.Helper()
	a := r.hs.h.angelus
	snap, err := a.Backend.FormState(r.z)
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := a.Backend.ApplyForm(r.z, form.Build(snap, set, drop)); err != nil {
		r.t.Fatal(err)
	}
}

// Defends the fail-closed read: a board still carrying the old list at
// system.grants and no system.scopes would otherwise hold the implicit *:*.
func TestABoardWithAListAtSystemGrantsFailsClosed(t *testing.T) {
	r := newNarrowRig(t)
	narrow, _ := json.Marshal([]string{"*:aria:self"})
	r.rewriteZ(map[string]json.RawMessage{form.GrantsKey: narrow}, form.ScopesKey)
	attach := func(ref string) map[string]any { return map[string]any{"figaro_id": ref} }
	if _, err := r.call(r.z, rpc.MethodAttach, attach(r.y)); err == nil {
		t.Fatal("a figaro whose board holds a list at system.grants read another figaro")
	}
	if _, err := r.call(r.y, rpc.MethodAttach, attach(r.x)); err != nil {
		t.Fatalf("control: a figaro with no list there: %v", err)
	}
	r.rewriteZ(nil, form.GrantsKey)
	if _, err := r.call(r.z, rpc.MethodAttach, attach(r.y)); err != nil {
		t.Fatalf("control: the list unset, the figaro is unnarrowed again: %v", err)
	}
}
