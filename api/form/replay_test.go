package form

import (
	"encoding/json"
	"testing"
)

func TestReplayMaskShowsTheOriginalIdentity(t *testing.T) {
	r := Replay{Of: "parent", Turn: 2, LT: 9, AriaID: "parent"}
	base := FromMap(map[string]json.RawMessage{
		"aria_id":            json.RawMessage(`"parent"`),
		"system.forked_from": json.RawMessage(`"grandparent"`),
		"mantra":             json.RawMessage(`"work"`),
	})
	birth := Build(base, map[string]json.RawMessage{
		ForkedFromKey: json.RawMessage(`"parent"`),
		"mantra":      json.RawMessage(`"work"`),
		"keep":        json.RawMessage(`1`),
	}, nil)
	stamp := Build(base, map[string]json.RawMessage{"aria_id": json.RawMessage(`"branch"`)}, nil)
	fresh := Build(Snapshot{}, map[string]json.RawMessage{
		"aria_id":     json.RawMessage(`"branch"`),
		ForkedFromKey: json.RawMessage(`"parent"`),
	}, nil)

	for name, p := range map[string]Patch{"birth": birth, "stamp": stamp, "fresh": fresh} {
		got := base.Apply(r.Mask("branch", p))
		if v := got.Lookup("aria_id"); v == nil || *v != "parent" {
			t.Errorf("%s: aria_id = %v", name, v)
		}
		if v := got.Lookup(ForkedFromKey); v != nil && *v == "parent" {
			t.Errorf("%s: the fork mark survived", name)
		}
		for _, e := range r.Mask("branch", p).Entries() {
			if e.Key == ForkedFromKey && !e.IsRemoval() {
				t.Errorf("%s: a fork mark entry survived: %+v", name, e)
			}
		}
	}
	if got := base.Apply(r.Mask("branch", birth)); !got.Has("keep") {
		t.Error("an unrelated key was dropped")
	}
	if snap := r.MaskSnapshot("branch", base.Apply(birth).Apply(stamp)); *snap.Lookup("aria_id") != "parent" || snap.Has(ForkedFromKey) {
		t.Errorf("snapshot not masked: aria_id %v", *snap.Lookup("aria_id"))
	}
}

func TestReplayMaskLeavesHistoryAlone(t *testing.T) {
	r := Replay{Of: "parent", Turn: 2, AriaID: "parent"}
	old := Build(Snapshot{}, map[string]json.RawMessage{
		"aria_id":     json.RawMessage(`"ancestor"`),
		ForkedFromKey: json.RawMessage(`"grandparent"`),
	}, nil)
	got := Snapshot{}.Apply(r.Mask("branch", old))
	if *got.Lookup("aria_id") != "ancestor" || *got.Lookup(ForkedFromKey) != "grandparent" {
		t.Fatalf("history rewritten: %v", got)
	}
}
