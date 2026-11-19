package angelus_test

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

type kingRig struct {
	licenseRig
	king, child, grandchild, outsider string
}

var kingScopes = []string{
	"read:*", "send:*", "create:*", "use:*",
	"fork:lineage:self", "set:lineage:self", "delete:lineage:self",
	"kill:lineage:self", "adopt:lineage:self",
	"cast:*", "succeed:*",
}

func newKingRig(t *testing.T) kingRig {
	t.Helper()
	l := newLicenseRig(t, defaultPolicy)
	keyGrants(l, true)
	r := kingRig{licenseRig: l, king: l.x}
	r.child = ariasFork(t, l.grantRig, r.king)
	r.grandchild = ariasFork(t, l.grantRig, r.child)
	r.outsider = ariasFork(t, l.grantRig, l.root)
	r.scope(t, r.king, kingScopes...)
	r.scope(t, r.outsider, kingScopes...)
	return r
}

func (r kingRig) forkSeatAs(t *testing.T, figaro, holder, role string) (string, error) {
	t.Helper()
	out, err := r.h[rpc.MethodFork](localOwner(), claimed(t, figaro, mustJSON(t, rpc.ForkRequest{FigaroID: holder, Seat: role, Abdicates: true})))
	if err != nil {
		return "", err
	}
	return out.(rpc.ForkResponse).Alternative, nil
}

func (r kingRig) seatAs(t *testing.T, figaro, holder, successor string) error {
	t.Helper()
	out, err := r.h[rpc.MethodSeat](localOwner(), claimed(t, figaro, withAria(t, holder, rpc.SeatRequest{FigaroID: holder, Successor: successor})))
	if err != nil {
		return err
	}
	if s := out.(rpc.SeatResponse); s.FailedAt != "" {
		return &seatFailure{s.FailedAt, s.Error}
	}
	return nil
}

type seatFailure struct{ at, why string }

func (e *seatFailure) Error() string { return e.at + ": " + e.why }

func (r kingRig) holder(t *testing.T, role string) string {
	t.Helper()
	v, _ := keyOn(t, r.grantRig, role, "target-aria")
	return strings.Trim(string(v), `"`)
}

// A King's role forms are minted at the top, outside its subtree; it
// succeeds a descendant's seat on its ancestry of the holder, not on reach
// over the form.
func TestAKingSucceedsAGrandchildsTopLevelRole(t *testing.T) {
	r := newKingRig(t)
	role := r.role(t, r.grandchild)
	successor, err := r.forkSeatAs(t, r.king, r.grandchild, role)
	if err != nil {
		t.Fatalf("the king could not fork its grandchild's successor: %v", err)
	}
	if err := r.seatAs(t, r.king, r.grandchild, successor); err != nil {
		t.Fatalf("the king could not seat the successor: %v", err)
	}
	if err := r.castAs(t, r.king, successor, role); err != nil {
		t.Fatalf("the king could not cast the successor into its grandchild's role: %v", err)
	}
	if got := r.holder(t, role); got != successor {
		t.Fatalf("role holder = %q, want %q", got, successor)
	}
}

func TestAFigaroOutsideTheHoldersAncestryCannotSucceedIt(t *testing.T) {
	r := newKingRig(t)
	role := r.role(t, r.grandchild)
	successor, err := r.forkSeatAs(t, r.king, r.grandchild, role)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.castAs(t, r.outsider, successor, role); err == nil {
		t.Fatal("an outsider cast a successor into the grandchild's role")
	}
	r.scope(t, r.outsider, "*:*")
	if err := r.seatAs(t, r.outsider, r.grandchild, successor); err == nil || !strings.Contains(err.Error(), "seats a successor") {
		t.Fatalf("an outsider holding *:* seated a successor in the grandchild's place: %v", err)
	}
	if got := r.holder(t, role); got != r.grandchild {
		t.Fatalf("role holder = %q after refusals, want %q", got, r.grandchild)
	}
}

func TestAKingCannotCastAnAriaOutsideItsSubtreeIntoADescendantsRole(t *testing.T) {
	r := newKingRig(t)
	role := r.role(t, r.grandchild)
	err := r.castAs(t, r.king, r.outsider, role)
	if err == nil || !strings.Contains(err.Error(), "placement") && !strings.Contains(err.Error(), "do not reach") {
		t.Fatalf("the king cast an aria outside its subtree into its grandchild's role: %v", err)
	}
	if got := r.holder(t, role); got != r.grandchild {
		t.Fatalf("role holder = %q, want %q", got, r.grandchild)
	}
}

func TestAKingCannotCastIntoARoleHeldOutsideItsSubtree(t *testing.T) {
	r := newKingRig(t)
	role := r.role(t, r.outsider)
	stray := ariasFork(t, r.grantRig, r.outsider)
	if err := r.castAs(t, r.king, stray, role); err == nil || !strings.Contains(err.Error(), "do not reach") {
		t.Fatalf("the king cast into a role whose holder is not its descendant: %v", err)
	}
	if err := r.castAs(t, r.king, r.grandchild, role); err == nil {
		t.Fatal("the king cast its own grandchild into a role held outside its subtree")
	}
	if got := r.holder(t, role); got != r.outsider {
		t.Fatalf("role holder = %q, want %q", got, r.outsider)
	}
}

func TestTheOwnerStillCastsAnyAriaIntoAnyRole(t *testing.T) {
	r := newKingRig(t)
	role := r.role(t, r.grandchild)
	if err := r.castAs(t, "", r.outsider, role); err != nil {
		t.Fatalf("the owner was refused: %v", err)
	}
}

func TestAKingCannotTakeItsParentsRoleForItsOwnDescendant(t *testing.T) {
	r := newKingRig(t)
	role := r.role(t, r.root)
	if err := r.castAs(t, r.king, r.grandchild, role); err == nil {
		t.Fatal("the king handed its parent's role to its own grandchild")
	}
	if got := r.holder(t, role); got != r.root {
		t.Fatalf("role holder = %q, want %q", got, r.root)
	}
}

func TestAHolderGainsNoReachOverItsOwnRoleFromAncestry(t *testing.T) {
	r := newKingRig(t)
	r.scope(t, r.child, kingScopes...)
	role := r.role(t, r.child)
	if err := r.castAs(t, r.child, r.grandchild, role); err == nil || !strings.Contains(err.Error(), "do not reach") {
		t.Fatalf("the holder passed its role down without reach over the role form: %v", err)
	}
}
