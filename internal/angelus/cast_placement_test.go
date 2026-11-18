package angelus

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func mintRoleFor(t *testing.T, hs *Handlers, holder string) string {
	t.Helper()
	targetRaw, _ := json.Marshal(holder)
	return mintForm(t, hs.h, "target-aria", string(targetRaw))
}

func castAs(t *testing.T, hs *Handlers, figaro, aria, role string) error {
	t.Helper()
	raw, err := rpc.WithCaller(rpc.CastRequest{FormID: role}, figaro, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = rpc.WithAria(raw, aria)
	if err != nil {
		t.Fatal(err)
	}
	_, err = hs.Map[rpc.MethodCast](inProcess(context.Background()), raw)
	return err
}

func castUnclaimed(t *testing.T, hs *Handlers, aria, role string) error {
	t.Helper()
	raw, err := rpc.WithCaller(rpc.CastRequest{FormID: role}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = rpc.WithAria(raw, aria)
	if err != nil {
		t.Fatal(err)
	}
	_, err = hs.Map[rpc.MethodCast](inProcess(context.Background()), raw)
	return err
}

func roleTarget(t *testing.T, hs *Handlers, role string) string {
	t.Helper()
	snap, err := hs.h.angelus.Backend.FormState(role)
	if err != nil {
		t.Fatal(err)
	}
	v := snap.Lookup("target-aria")
	if v == nil {
		return ""
	}
	return *v
}

// Once a role has a holder, a labeled figaro that is neither the holder
// nor a descendant of it in the drawn tree cannot take it: the daemon
// trusts the label (no box here to verify it) and refuses on it, the
// same honour C3 gives fanout.
func TestCastRefusesToHandAHeldRoleToAnUnrelatedAria(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	holder, _ := b.CreateConversation(outfit)
	_, unrelated, err := b.Fork(holder)
	if err != nil {
		t.Fatal(err)
	}
	outsider, _ := b.CreateConversation(outfit)
	role := mintRoleFor(t, hs, holder)

	if err := castAs(t, hs, outsider, outsider, role); err == nil ||
		!strings.Contains(err.Error(), "placement") || !strings.Contains(err.Error(), holder) {
		t.Fatalf("an unrelated aria was cast into a held role: %v", err)
	}
	if got := roleTarget(t, hs, role); got != holder {
		t.Fatalf("role's target after the refused cast = %q, want unchanged %q", got, holder)
	}
	_ = unrelated
}

// The holder itself may always recast into its own role.
func TestCastAllowsTheRolesOwnHolderToRecast(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	holder, _ := b.CreateConversation(outfit)
	role := mintRoleFor(t, hs, holder)

	if err := castAs(t, hs, holder, holder, role); err != nil {
		t.Fatalf("the holder could not recast itself into its own role: %v", err)
	}
}

// A descendant of the holder, in the tree `ls` draws, may take the role:
// this is what lets a succession hand it to a forked (or demoted-under)
// successor.
func TestCastAllowsADescendantOfTheHolderInTheDrawnTree(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	holder, _ := b.CreateConversation(outfit)
	_, child, err := b.Fork(holder)
	if err != nil {
		t.Fatal(err)
	}
	role := mintRoleFor(t, hs, holder)

	if err := castAs(t, hs, child, child, role); err != nil {
		t.Fatalf("a descendant of the holder was refused the role: %v", err)
	}
	if got := roleTarget(t, hs, role); got != child {
		t.Fatalf("role's target after a legitimate recast = %q, want %q", got, child)
	}
}

// A successor that forks its predecessor, then demotes the predecessor
// under it, sits directly over the predecessor and descends from it by
// forking: succeed casts it into every role the predecessor held after
// the demote, so this shape must pass.
func TestCastAllowsTheSuccessorOfADemoteSwapToTakeTheHoldersRole(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	holder, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, holder, 1)
	role := mintRoleFor(t, hs, holder)

	out, err := callForkAsFigaro(t, hs, holder, rpc.ForkRequest{FigaroID: holder})
	if err != nil {
		t.Fatal(err)
	}
	succ := out.Alternative

	raw, _ := json.Marshal(rpc.DemoteRequest{FigaroID: holder, Prefer: succ})
	if _, err := hs.Map[rpc.MethodDemote](inProcess(context.Background()), raw); err != nil {
		t.Fatalf("demote: %v", err)
	}

	if err := castAs(t, hs, holder, succ, role); err != nil {
		t.Fatalf("the successor of a demote swap was refused the holder's role: %v", err)
	}
	if got := roleTarget(t, hs, role); got != succ {
		t.Fatalf("role's target after the swap's recast = %q, want %q", got, succ)
	}
}

// A figaro holding two roles, capped by its own max_children, succeeds:
// the claimed fork is born at 0, the demote moves the predecessor's limit
// to it, and both roles recast to the successor now over the holder.
func TestSucceedCompletesForAHolderOfTwoRolesUnderAFanoutLimit(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	pred, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, pred, 2)
	role1 := mintRoleFor(t, hs, pred)
	role2 := mintRoleFor(t, hs, pred)

	out, err := callForkAsFigaro(t, hs, pred, rpc.ForkRequest{FigaroID: pred})
	if err != nil {
		t.Fatalf("predecessor forking its successor: %v", err)
	}
	succ := out.Alternative

	raw, _ := json.Marshal(rpc.DemoteRequest{FigaroID: pred, Prefer: succ})
	if _, err := hs.Map[rpc.MethodDemote](inProcess(context.Background()), raw); err != nil {
		t.Fatalf("demoting the predecessor under its successor: %v", err)
	}

	for _, role := range []string{role1, role2} {
		if err := castAs(t, hs, pred, succ, role); err != nil {
			t.Fatalf("recasting %s onto the successor: %v", role, err)
		}
		if got := roleTarget(t, hs, role); got != succ {
			t.Fatalf("%s target after succeed = %q, want %q", role, got, succ)
		}
	}
}

// The owner acting with no figaro label is exempt from the placement
// check entirely, the same as the fanout grant's self/ancestor rule.
func TestCastPlacementExemptsTheUnlabeledOwner(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	holder, _ := b.CreateConversation(outfit)
	outsider, _ := b.CreateConversation(outfit)
	role := mintRoleFor(t, hs, holder)

	if err := castUnclaimed(t, hs, outsider, role); err != nil {
		t.Fatalf("the unlabeled owner was refused a placement it should be exempt from: %v", err)
	}
}

// A brand-new, unnamed role (minted as part of the cast) has no prior
// holder: nothing to be under yet.
func TestCastPlacementDoesNotApplyToAFreshlyMintedRole(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	aria, _ := b.CreateConversation(outfit)
	nameRaw, _ := json.Marshal("fresh-role")
	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{"name": nameRaw}, nil)
	raw, err := rpc.WithCaller(rpc.CastRequest{RolePatch: &patch}, aria, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = rpc.WithAria(raw, aria)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hs.Map[rpc.MethodCast](inProcess(context.Background()), raw); err != nil {
		t.Fatalf("minting a fresh role during cast was refused on placement grounds: %v", err)
	}
}

// An ancestor of the holder in the drawn tree is not its successor: the
// King and the holder's own lead are refused its role.
func TestCastRefusesAnAncestorOfTheHolder(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	king, _ := b.CreateConversation(outfit)
	_, lead, err := b.Fork(king)
	if err != nil {
		t.Fatal(err)
	}
	_, worker, err := b.Fork(lead)
	if err != nil {
		t.Fatal(err)
	}
	role := mintRoleFor(t, hs, worker)
	for name, id := range map[string]string{"the King": king, "the lead": lead} {
		if err := castAs(t, hs, id, id, role); err == nil || !strings.Contains(err.Error(), "placement") {
			t.Fatalf("%s took a subordinate's role: %v", name, err)
		}
	}
	if got := roleTarget(t, hs, role); got != worker {
		t.Fatalf("role target = %q, want the worker %q", got, worker)
	}
}

// Sitting over the holder is not enough: the aria over it must also have
// been forked from it. A stranger demoted over the holder is refused.
func TestCastRefusesANonForkDemotedOverTheHolder(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	gp, _ := b.CreateConversation(outfit)
	_, holder, err := b.Fork(gp)
	if err != nil {
		t.Fatal(err)
	}
	stranger, _ := b.CreateConversation(outfit)
	if err := b.HierarchyEngine().WriteDrawn(map[string]string{stranger: holder}, nil); err != nil {
		t.Fatal(err)
	}
	role := mintRoleFor(t, hs, holder)
	raw, _ := json.Marshal(rpc.DemoteRequest{FigaroID: holder, Prefer: stranger})
	if _, err := hs.Map[rpc.MethodDemote](inProcess(context.Background()), raw); err != nil {
		t.Fatalf("demote: %v", err)
	}
	if err := castAs(t, hs, stranger, stranger, role); err == nil || !strings.Contains(err.Error(), "placement") {
		t.Fatalf("a stranger demoted over the holder took its role: %v", err)
	}
}
