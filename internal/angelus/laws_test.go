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
	for _, p := range []form.Patch{*heldPatch(t, map[string]any{"law.mine": "x"}), repeal} {
		if _, err := r.h[rpc.MethodSet](toolOf(r.child), withAria(t, r.child, rpc.SetRequest{Patch: p})); err == nil || !strings.Contains(err.Error(), "owner-held") {
			t.Errorf("a figaro's tool shell wrote or repealed its own law: %v", err)
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

func outfittedLineageRig(t *testing.T) lineageRig {
	t.Helper()
	r := newLineageRig(t, "outfits = [\"judged\", \"lawful\"]\n")
	r.ownerSets(t, r.parent, map[string]any{"system.grants.outfits": []string{"judged", "lawful"}, "law.report": lawText})
	return r
}

func TestAFigaroMadeByAFigaroCarriesItsMakersLaws(t *testing.T) {
	r := outfittedLineageRig(t)
	c := r.dial(t)
	raw, _ := json.Marshal(rpc.CreateRequest{Outfits: []string{"judged"}, Cwd: r.cwd})
	var out rpc.CreateResponse
	if err := c.Call(context.Background(), rpc.MethodCreate, claimed(t, r.parent, raw), &out); err != nil {
		t.Fatal(err)
	}
	require.JSONEq(t, `{"report":"`+lawText+`"}`, lawsOn(t, r.grantRig, out.FigaroID))
	fromTool, err := r.create(toolOf(r.parent), "judged")
	require.NoError(t, err)
	require.JSONEq(t, `{"report":"`+lawText+`"}`, lawsOn(t, r.grantRig, fromTool.FigaroID), "a figaro made from its maker's tool shell")
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

// Defends the layering at birth: the outfit lives in the form the child is
// forked from and the maker's laws land in the birth patch above it, so an
// outfit a figaro names adds a law but rewrites none of its maker's.
func TestAnOutfitAddsToItsMakersLawsButRewritesNone(t *testing.T) {
	r := outfittedLineageRig(t)
	out, err := r.create(toolOf(r.parent), "lawful")
	require.NoError(t, err)
	require.JSONEq(t, `{"report":"`+lawText+`","extra":"from the outfit"}`, lawsOn(t, r.grantRig, out.FigaroID))
	mine, err := r.create(asOwner(), "lawful")
	require.NoError(t, err)
	require.JSONEq(t, `{"report":"the outfit's word","extra":"from the outfit"}`, lawsOn(t, r.grantRig, mine.FigaroID))
}

// Defends the fork's copy against the outfit it is dressed in: without it
// a figaro granted an outfit that carries law.x forks itself free of its own
// law.x, at the head or at any turn.
func TestAForkDressedInAnOutfitKeepsItsParentsLaws(t *testing.T) {
	r := outfittedLineageRig(t)
	c := r.dial(t)
	spokeTwice(t, r.grantRig, r.parent)
	want := `{"report":"` + lawText + `","extra":"from the outfit"}`
	for _, at := range []uint64{0, 1} {
		raw, _ := json.Marshal(rpc.ForkRequest{FigaroID: r.parent, AtTurn: at, Outfits: []string{"lawful"}})
		var out rpc.ForkResponse
		if err := c.Call(context.Background(), rpc.MethodFork, claimed(t, r.parent, raw), &out); err != nil {
			t.Fatalf("fork at turn %d: %v", at, err)
		}
		require.JSONEq(t, want, lawsOn(t, r.grantRig, out.Alternative), "a fork at turn %d dressed in lawful", at)
	}
}

// Defends the one-gesture repeal: a law copied at birth lives on in every
// figaro drawn below, and a repeal that reached only the root left them
// bound.
func TestTheOwnersRepealReachesTheWholeLineageInOneCall(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	for _, id := range []string{r.parent, r.child, r.sibling} {
		r.ownerSets(t, id, map[string]any{"law.report": lawText})
	}
	grandchild := r.forkAsFigaro(t, r.child)
	require.JSONEq(t, `{"report":"`+lawText+`"}`, lawsOn(t, r.grantRig, grandchild))
	outsider, err := r.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	r.ownerSets(t, outsider.FigaroID, map[string]any{"law.report": lawText})
	repeal := rpc.SetRequest{Patch: form.Build(form.Snapshot{}, nil, []string{"law.report"}), Lineage: true}
	if err := c.Call(context.Background(), rpc.MethodSet, claimed(t, r.parent, withAria(t, r.parent, repeal)), nil); err == nil || !strings.Contains(err.Error(), "owner's alone") {
		t.Fatalf("a figaro set a whole lineage: %v", err)
	}
	if _, err := r.h[rpc.MethodSet](toolOf(r.parent), withAria(t, r.parent, repeal)); err == nil || !strings.Contains(err.Error(), "owner's alone") {
		t.Fatalf("a figaro's tool shell set a whole lineage: %v", err)
	}
	out, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.parent, repeal))
	if err != nil {
		t.Fatal(err)
	}
	reached := map[string]string{}
	for _, row := range out.(rpc.SetResponse).Lineage {
		reached[row.FigaroID] = row.Outcome + row.Refused
	}
	for _, id := range []string{r.child, r.sibling, grandchild} {
		if reached[id] != rpc.OutcomeApplied {
			t.Errorf("%s: %q in %v", id, reached[id], reached)
		}
	}
	for _, id := range []string{r.parent, r.child, r.sibling, grandchild} {
		if got := lawsOn(t, r.grantRig, id); got != "" && got != "{}" {
			t.Errorf("%s still holds %s", id, got)
		}
	}
	if _, ok := reached[outsider.FigaroID]; ok {
		t.Fatal("the repeal reached a figaro outside the lineage")
	}
	require.JSONEq(t, `{"report":"`+lawText+`"}`, lawsOn(t, r.grantRig, outsider.FigaroID))
	give := rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"law.spend": "Opus only under the throttle."}), Lineage: true}
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.parent, give)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{r.parent, r.child, r.sibling, grandchild} {
		require.JSONEq(t, `{"spend":"Opus only under the throttle."}`, lawsOn(t, r.grantRig, id), id)
	}
}

func (r lineageRig) roleHeldBy(t *testing.T, holder string, keys map[string]any) string {
	t.Helper()
	out, err := r.h[rpc.MethodFormCreate](asOwner(), mustJSON(t, rpc.FormCreateRequest{Patch: heldPatch(t, keys)}))
	if err != nil {
		t.Fatal(err)
	}
	role := out.(rpc.FormCreateResponse).FormID
	if _, err := r.h[rpc.MethodCast](asOwner(), withAria(t, holder, rpc.CastRequest{FormID: role})); err != nil {
		t.Fatal(err)
	}
	return role
}

// Defends a role's laws against its holder: without the guard the holder
// drops the role, or restudies it naming only the keys it likes, and reads
// no law again.
func TestAHolderCannotDropOrNarrowAwayItsRolesLaws(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	role := r.roleHeldBy(t, r.child, map[string]any{"law.report": lawText, "phase": "two"})
	asChild := func(method string, req rpc.StudyRequest) error {
		_, err := r.h[method](asOwner(), claimed(t, r.child, withAria(t, r.child, req)))
		return err
	}
	if err := c.Call(context.Background(), rpc.MethodDrop, claimed(t, r.child, withAria(t, r.child, rpc.StudyRequest{FormID: role})), nil); err == nil || !strings.Contains(err.Error(), "law.report") {
		t.Fatalf("the holder dropped its role's laws through its box door: %v", err)
	}
	if err := asChild(rpc.MethodDrop, rpc.StudyRequest{FormID: role}); err == nil || !strings.Contains(err.Error(), "law.report") {
		t.Fatalf("the holder dropped its role's laws under its claim on the local door: %v", err)
	}
	if _, err := r.h[rpc.MethodDrop](toolOf(r.child), withAria(t, r.child, rpc.StudyRequest{FormID: role})); err == nil || !strings.Contains(err.Error(), "law.report") {
		t.Fatalf("the holder dropped its role's laws from its tool shell: %v", err)
	}
	out, err := r.h[rpc.MethodFormCreate](asOwner(), mustJSON(t, rpc.FormCreateRequest{Patch: heldPatch(t, map[string]any{"law.report": lawText, "phase": "two", "target-aria": r.child})}))
	if err != nil {
		t.Fatal(err)
	}
	unstudied := out.(rpc.FormCreateResponse).FormID
	if err := asChild(rpc.MethodStudy, rpc.StudyRequest{FormID: unstudied, Keys: []string{"phase"}}); err == nil || !strings.Contains(err.Error(), "law.report") {
		t.Fatalf("the holder studied its role past the law: %v", err)
	}
	if err := asChild(rpc.MethodStudy, rpc.StudyRequest{FormID: unstudied, Keys: []string{"phase", "law"}}); err != nil {
		t.Fatalf("a study that keeps the laws was refused: %v", err)
	}
	plain := r.roleHeldBy(t, r.sibling, map[string]any{"phase": "one"})
	if err := c.Call(context.Background(), rpc.MethodDrop, claimed(t, r.sibling, withAria(t, r.sibling, rpc.StudyRequest{FormID: plain})), nil); err != nil {
		t.Fatalf("a role with no law could not be dropped: %v", err)
	}
	if _, err := r.h[rpc.MethodDrop](asOwner(), withAria(t, r.child, rpc.StudyRequest{FormID: role})); err != nil {
		t.Fatalf("the owner's drop was refused: %v", err)
	}
}
