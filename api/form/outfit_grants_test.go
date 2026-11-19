package form_test

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func TestAnOutfitGrantIsAListOfNamesOrGlobs(t *testing.T) {
	shape, ok := form.ShapeOf(form.OutfitGrantsKey)
	if !ok {
		t.Fatal("system.grants.outfits has no registry row")
	}
	for _, v := range []string{`[]`, `["hk"]`, `["hk","model-*","*","succession-?"]`} {
		if err := shape.Check(json.RawMessage(v)); err != nil {
			t.Errorf("%s refused: %v", v, err)
		}
	}
	for _, v := range []string{`"hk"`, `{}`, `null`, `[1]`, `[""]`, `["a b"]`, `[" hk"]`, `["hk,opus5"]`, `["x/y"]`, `["a["]`, `["a\\"]`} {
		if err := shape.Check(json.RawMessage(v)); err == nil {
			t.Errorf("%s accepted", v)
		}
	}
}

func TestAnOutfitIsGrantedByItsNameOrAGlobThatMatchesIt(t *testing.T) {
	grants := []string{"hk", "model-*"}
	for name, want := range map[string]bool{"hk": true, "model-opus5": true, "model-": true, "opus5": false, "hkx": false, "xmodel-a": false, "default": false} {
		if got := form.OutfitGranted(grants, name); got != want {
			t.Errorf("%s: granted %v, want %v", name, got, want)
		}
	}
	if !form.OutfitGranted([]string{"*"}, "anything") {
		t.Error("* did not grant every outfit")
	}
	if form.OutfitGranted(nil, "hk") {
		t.Error("no grant granted hk")
	}
}

func TestOutfitGrantsReadsTheListAndRefusesAMalformedOne(t *testing.T) {
	with := func(v string) form.Snapshot {
		return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{form.OutfitGrantsKey: json.RawMessage(v)}, nil))
	}
	if got, err := form.OutfitGrants(with(`["hk","model-*"]`)); err != nil || len(got) != 2 || got[1] != "model-*" {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := form.OutfitGrants(form.Snapshot{}); err != nil || got != nil {
		t.Fatalf("unset: %v %v", got, err)
	}
	if _, err := form.OutfitGrants(with(`["a["]`)); err == nil {
		t.Fatal("a malformed glob was read")
	}
}

func TestOutfitGrantsAreTheOwnersAndManagedApplyMayNameThem(t *testing.T) {
	if got := form.WriterOf(form.OutfitGrantsKey); got != form.WriterOwner {
		t.Fatalf("writer %s, want owner", got)
	}
	found := false
	for _, p := range form.OperatorPaths() {
		found = found || p == form.OutfitGrantsKey
	}
	if !found {
		t.Fatalf("%s is not an operator path: %v", form.OutfitGrantsKey, form.OperatorPaths())
	}
}
