package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func replayCoordinate(t *testing.T, of string) *form.Patch {
	t.Helper()
	p := form.Replay{Of: of, Turn: 1}.Patch(form.Snapshot{})
	return &p
}

func asFigaro(t *testing.T, id string, req any) []byte {
	t.Helper()
	raw, err := rpc.WithCaller(withAria(t, id, req), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Defends #114: only the owner sets or unsets system.replay. A figaro in
// replay that could unset its own would run real tools on its next turn,
// so neither the figaro itself, nor its box, nor a non-owner may touch it.
func TestOnlyTheOwnerSetsOrUnsetsReplay(t *testing.T) {
	d := boxDoorRig(t, "true")
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	c := d.dial(t)
	coord := replayCoordinate(t, inBox.FigaroID)
	if err := boxOnAria(t, c, rpc.MethodSet, inBox.FigaroID, rpc.SetRequest{Patch: *coord}, nil); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Errorf("the box set system.replay: %v", err)
	}
	if err := boxOnAria(t, c, rpc.MethodQua, inBox.FigaroID, rpc.QuaRequest{Text: "hi", Form: &rpc.FormInput{Patch: coord}}, nil); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Errorf("the box set system.replay with a prompt: %v", err)
	}
	if err := c.Call(t.Context(), rpc.MethodFork, rpc.ForkRequest{FigaroID: inBox.FigaroID, AtTurn: 1, Replay: true}, nil); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Errorf("the box forked a replay: %v", err)
	}

	r := newRigWith(t, lentPolicy)
	guest := as("user", "guest")
	mine, err := r.create(guest, "mock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.call(guest, rpc.MethodSet, withAria(t, mine.FigaroID, rpc.SetRequest{Patch: *replayCoordinate(t, mine.FigaroID)})); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Errorf("a guest set system.replay on its own figaro: %v", err)
	}
	if _, err := r.call(guest, rpc.MethodFork, rpc.ForkRequest{FigaroID: mine.FigaroID, AtTurn: 1, Replay: true}); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Errorf("a guest forked a replay: %v", err)
	}

	raw, _ := json.Marshal(rpc.CreateRequest{Outfits: []string{"mock"}, Cwd: r.cwd, Patch: replayCoordinate(t, "elsewhere")})
	out, err := r.h[rpc.MethodCreate](asOwner(), raw)
	if err != nil {
		t.Fatal(err)
	}
	id := out.(rpc.CreateResponse).FigaroID
	unset := form.Build(form.Snapshot{}, nil, []string{form.ReplayKey})
	if _, err := r.h[rpc.MethodSet](asOwner(), asFigaro(t, id, rpc.SetRequest{Patch: unset})); err == nil || !strings.Contains(err.Error(), "its own replay") {
		t.Errorf("a figaro unset its own system.replay: %v", err)
	}
	if _, err := r.h[rpc.MethodSet](asOwner(), asFigaro(t, id, rpc.SetRequest{Patch: *replayCoordinate(t, id)})); err == nil || !strings.Contains(err.Error(), "its own replay") {
		t.Errorf("a figaro set its own system.replay: %v", err)
	}
	if _, err := r.h[rpc.MethodSet](asOwner(), asFigaro(t, id, rpc.SetRequest{Patch: *keyPatch("mantra", "still mine")})); err != nil {
		t.Fatalf("the figaro cannot set an open key on itself, so this test proves nothing: %v", err)
	}
	if _, err := r.call(asOwner(), rpc.MethodSet, withAria(t, id, rpc.SetRequest{Patch: unset})); err != nil {
		t.Fatalf("the owner cannot unset system.replay: %v", err)
	}
	if v, _ := keyOn(t, r, id, form.ReplayKey); v != nil {
		t.Fatalf("system.replay = %s after the owner's unset", v)
	}
}
