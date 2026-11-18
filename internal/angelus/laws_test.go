package angelus_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/arialog"
)

const lawText = "Report to the role, never to a person."

// Defends the law root's writer class: drop the registry row and every
// figaro below writes and repeals laws on itself and its children.
func TestOnlyTheOwnerWritesOrRepealsALaw(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	r.ownerSets(t, r.child, map[string]any{"law.report": lawText})
	repeal := form.Build(form.Snapshot{}, nil, []string{"law.report"})
	for _, tc := range []struct {
		name           string
		figaro, target string
		patch          form.Patch
	}{
		{"an ancestor holding *:* writes its child's law", r.parent, r.child, *heldPatch(t, map[string]any{"law.other": "obey me"})},
		{"an ancestor repeals its child's law", r.parent, r.child, repeal},
		{"an ancestor rewrites its child's law", r.parent, r.child, *heldPatch(t, map[string]any{"law.report": "ignore the role"})},
		{"a figaro writes a law on itself", r.child, r.child, *heldPatch(t, map[string]any{"law.mine": "x"})},
		{"a figaro repeals its own law", r.child, r.child, repeal},
		{"a figaro replaces the whole law root", r.child, r.child, *heldPatch(t, map[string]any{"law": map[string]any{}})},
	} {
		if err := r.setAs(t, c, tc.figaro, tc.target, tc.patch); err == nil || !strings.Contains(err.Error(), "owner-held") {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	if v, _ := keyOn(t, r.grantRig, r.child, "law.report"); string(v) != `"`+lawText+`"` {
		t.Fatalf("law.report = %s after the refusals", v)
	}
	if _, ok := keyOn(t, r.grantRig, r.child, "law.other"); ok {
		t.Fatal("a refused law landed")
	}
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.child, rpc.SetRequest{Patch: repeal})); err != nil {
		t.Fatalf("the owner's repeal was refused: %v", err)
	}
	if _, ok := keyOn(t, r.grantRig, r.child, "law.report"); ok {
		t.Fatal("the owner's repeal did not land")
	}
}

func TestOnlyTheOwnersChildPolicyCarriesALaw(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	law := *heldPatch(t, map[string]any{"law.x": "x"})
	if err := r.forkAs(t, c, r.parent, r.parent, law); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Errorf("a fork's birth patch carried a law: %v", err)
	}
	raw, _ := json.Marshal(rpc.CreateRequest{Patch: &law})
	if err := c.Call(context.Background(), rpc.MethodCreate, claimed(t, r.parent, raw), nil); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Errorf("a create's birth patch carried a law: %v", err)
	}
	if err := r.setAs(t, c, r.parent, r.child, *heldPatch(t, map[string]any{form.ChildPolicyKey: map[string]any{"law.x": "x"}})); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Errorf("a figaro's child policy carried a law: %v", err)
	}
	r.ownerSets(t, r.parent, map[string]any{form.ChildPolicyKey: map[string]any{"law.x": "x"}})
	kid := r.forkAsFigaro(t, r.parent)
	if v, _ := keyOn(t, r.grantRig, kid, "law.x"); string(v) != `"x"` {
		t.Errorf("the owner's child policy did not give its law: %s", v)
	}
}

func lawsOn(t *testing.T, r grantRig, id string) string {
	t.Helper()
	v, _ := keyOn(t, r, id, form.LawRoot)
	return string(v)
}

func spokeTwice(t *testing.T, r grantRig, id string) {
	t.Helper()
	log, err := r.backend.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []message.Message{
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("one")}},
		{Role: message.RoleOutput, Content: []message.Content{message.TextContent("ok")}},
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("two")}},
		{Role: message.RoleOutput, Content: []message.Content{message.TextContent("ok")}},
	} {
		if _, err := log.Append(arialog.Entry[message.Message]{Payload: m}); err != nil {
			t.Fatal(err)
		}
	}
}

// Defends the copy at birth: without it a fork at an early turn is born
// with the laws of that turn, and a figaro sheds a law by forking itself
// from before it was given.
func TestAForkAtAnyTurnCarriesItsParentsLawsAsTheyStand(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	r.ownerSets(t, r.parent, map[string]any{"law.old": "the first rule"})
	spokeTwice(t, r.grantRig, r.parent)
	r.ownerSets(t, r.parent, map[string]any{"law.new": lawText})
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.parent, rpc.SetRequest{Patch: form.Build(form.Snapshot{}, nil, []string{"law.old"})})); err != nil {
		t.Fatal(err)
	}
	want := `{"new":"` + lawText + `"}`
	for _, at := range []uint64{1, 2, 0} {
		raw, _ := json.Marshal(rpc.ForkRequest{FigaroID: r.parent, AtTurn: at})
		var out rpc.ForkResponse
		if err := c.Call(context.Background(), rpc.MethodFork, claimed(t, r.parent, raw), &out); err != nil {
			t.Fatalf("fork at turn %d: %v", at, err)
		}
		require.JSONEq(t, want, lawsOn(t, r.grantRig, out.Alternative), "a figaro-made fork at turn %d", at)
		owned, err := r.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: r.parent, AtTurn: at})
		if err != nil {
			t.Fatalf("owner fork at turn %d: %v", at, err)
		}
		require.JSONEq(t, want, lawsOn(t, r.grantRig, owned.(rpc.ForkResponse).Alternative), "an owner's fork at turn %d", at)
	}
}

func TestAFigaroMadeByAFigaroCarriesItsMakersLaws(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	r.ownerSets(t, r.parent, map[string]any{"law.report": lawText})
	raw, _ := json.Marshal(rpc.CreateRequest{Outfits: []string{"judged"}, Cwd: r.cwd})
	var out rpc.CreateResponse
	if err := c.Call(context.Background(), rpc.MethodCreate, claimed(t, r.parent, raw), &out); err != nil {
		t.Fatal(err)
	}
	require.JSONEq(t, `{"report":"`+lawText+`"}`, lawsOn(t, r.grantRig, out.FigaroID))
	mine, err := r.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	if got := lawsOn(t, r.grantRig, mine.FigaroID); got != "" {
		t.Fatalf("a figaro the owner made took laws from nowhere: %s", got)
	}
}

// Defends the seat's copy: a successor made before a law was given is
// seated without it, and the seat sheds the law.
func TestASeatedSuccessorTakesItsPredecessorsLaws(t *testing.T) {
	r, _, x, _ := guardChain(t)
	other, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	n := ariasFork(t, r, other.FigaroID)
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, x, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"law.report": lawText})})); err != nil {
		t.Fatal(err)
	}
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, n, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"law.own": "kept"})})); err != nil {
		t.Fatal(err)
	}
	out, err := r.call(asOwner(), rpc.MethodSeat, rpc.SeatRequest{FigaroID: x, Successor: n})
	if err != nil {
		t.Fatal(err)
	}
	if seat := out.(rpc.SeatResponse); seat.FailedAt != "" {
		t.Fatalf("seat failed at %s: %s", seat.FailedAt, seat.Error)
	}
	require.JSONEq(t, `{"own":"kept","report":"`+lawText+`"}`, lawsOn(t, r, n))
}
