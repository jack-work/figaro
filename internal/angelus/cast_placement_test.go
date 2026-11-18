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
// under it, becomes the predecessor's ancestor in the drawn tree: the
// placement check accepts that direction too, which is what lets a
// succession recast every role the predecessor held, not only the named
// one.
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
// the fork inherits its own limit (room for the swap), the demote fits
// under it, and both roles recast to the successor, including the one
// the law of ancestry (not descent) now has to cover.
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
