package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func TestALogCarriesNoModelFromAnyBirth(t *testing.T) {
	r := logRig(t, defaultPolicy, nil)
	withModel := form.Build(form.Snapshot{}, map[string]json.RawMessage{form.KindKey: json.RawMessage(`"log"`), "system.model": json.RawMessage(`"m"`)}, nil)
	if _, err := r.call(asOwner(), rpc.MethodFormBind, rpc.FormBindRequest{Patch: &withModel}); err == nil || !strings.Contains(err.Error(), "has no model") {
		t.Fatalf("form.bind of a log with a model = %v, want refused", err)
	}

	model, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: model.FigaroID, Patch: logBirth()}); err == nil || !strings.Contains(err.Error(), "has no model") {
		t.Fatalf("a fork into a log that keeps its parent's provider = %v, want refused", err)
	}
	bare := form.Build(form.Snapshot{}, map[string]json.RawMessage{form.KindKey: json.RawMessage(`"log"`)}, []string{"system.provider", "system.model"})
	out, err := r.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: model.FigaroID, Patch: &bare})
	if err != nil {
		t.Fatalf("a fork into a log that drops the model: %v", err)
	}
	child := out.(rpc.ForkResponse).Alternative
	snap, err := r.backend.FormState(child)
	if err != nil {
		t.Fatal(err)
	}
	if !form.IsLog(snap) || snap.Has("system.provider") || snap.Has("system.model") {
		t.Fatalf("the forked log's form: %v", snap)
	}
}

func TestAnOutfitCannotDressALogInAModel(t *testing.T) {
	r := logRig(t, defaultPolicy, nil)
	id := mintLog(t, r, asOwner())
	_, err := r.h[rpc.MethodSet](asOwner(), withAria(t, id, rpc.SetRequest{Outfits: []string{"mock"}}))
	if err == nil || !strings.Contains(err.Error(), "has no model") {
		t.Fatalf("set -O mock on a log = %v, want refused", err)
	}
	_, err = r.h[rpc.MethodQua](asOwner(), withAria(t, id, rpc.QuaRequest{Text: "x", Form: &rpc.FormInput{Outfits: []string{"mock"}}}))
	if err == nil || !strings.Contains(err.Error(), "has no model") {
		t.Fatalf("send -O mock to a log = %v, want refused", err)
	}
	snap, err := r.backend.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Has("system.provider") || snap.Has("system.model") {
		t.Fatalf("an outfit dressed a log in a model: %v", snap)
	}
}
