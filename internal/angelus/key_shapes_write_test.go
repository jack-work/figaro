package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func (r lineageRig) ownerSet(t *testing.T, id string, keys map[string]any) error {
	t.Helper()
	_, err := r.h[rpc.MethodSet](asOwner(), withAria(t, id, rpc.SetRequest{Patch: *heldPatch(t, keys)}))
	return err
}

// Defends the shapes on the write path: every caller, the owner's fast door
// included, leaves each registry row its patch reaches in the row's shape.
// Skip refuseMisshapenKeys in refuseCallerKeys and the figaro rows go red;
// skip it on the owner's door and the owner rows do.
func TestAWriteLeavesEveryKeyItTouchesInShape(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	r.ownerSets(t, r.child, map[string]any{"system.retirement": map[string]any{"context_threshold": 1000}})

	figaroBad := map[string]map[string]any{
		"system.cache_control":       {"system.cache_control": "bogus"},
		"system.thinking_budget":     {"system.thinking_budget": -1},
		"system.study_incantation":   {"system.study_incantation": map[string]any{"onstudied": "x"}},
		"system.fanout.max_children": {"system.fanout": map[string]any{"max_children": "many"}},
	}
	for want, keys := range figaroBad {
		err := r.setAs(t, c, r.parent, r.child, *heldPatch(t, keys))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("figaro wrote %v: %v", keys, err)
		}
	}
	ownerBad := map[string]map[string]any{
		"system.max_tokens":      {"system.max_tokens": 0},
		"system.ttl":             {"system.ttl": "soon"},
		"system.retirement":      {"system.retirement.action": "stay"},
		"system.environment.FOO": {"system.environment.FOO": 7},
		"duke-title":             {"duke-title": 5},
		"system.scopes":          {form.ScopesKey: "read:*"},
	}
	for want, keys := range ownerBad {
		err := r.ownerSet(t, r.child, keys)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("owner wrote %v: %v", keys, err)
		}
	}
	if v, _ := keyOn(t, r.grantRig, r.child, "system.max_tokens"); v != nil {
		t.Fatalf("a refused write landed: system.max_tokens %s", v)
	}

	if err := r.setAs(t, c, r.parent, r.child, *heldPatch(t, map[string]any{"system.cache_control": "1h", "mantra": "x"})); err != nil {
		t.Fatalf("figaro refused good values: %v", err)
	}
	for _, keys := range []map[string]any{
		{"system.max_tokens": 4096, "system.methods.enabled": true},
		{"system.retirement.ttl": "1h"},
		{"system.environment": map[string]any{"FOO": "/tmp"}},
	} {
		if err := r.ownerSet(t, r.child, keys); err != nil {
			t.Errorf("owner refused %v: %v", keys, err)
		}
	}

	if _, err := r.backend.ApplyForm(r.child, *heldPatch(t, map[string]any{"system.thinking_budget": -5})); err != nil {
		t.Fatal(err)
	}
	if err := r.ownerSet(t, r.child, map[string]any{"mantra": "y"}); err != nil {
		t.Fatalf("an untouched bad value refused the owner's write: %v", err)
	}
	if err := r.setAs(t, c, r.parent, r.child, *heldPatch(t, map[string]any{"mantra": "z"})); err != nil {
		t.Fatalf("an untouched bad value refused a figaro's write: %v", err)
	}

	ctxOnly := &rpc.FormInput{Context: map[string]json.RawMessage{"duke-title": json.RawMessage(`5`)}}
	if _, err := r.h[rpc.MethodQua](asOwner(), withAria(t, r.child, rpc.QuaRequest{Text: "hi", Form: ctxOnly})); err == nil || !strings.Contains(err.Error(), "duke-title") {
		t.Fatalf("owner wrote duke-title 5 through a prompt's context: %v", err)
	}
}
