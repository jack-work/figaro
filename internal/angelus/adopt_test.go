package angelus_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro/wire"
)

func adoptRig(t *testing.T) (r grantRig, q, x string) {
	t.Helper()
	r = newRigWith(t, guardPolicy)
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	owned, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	grantTo(t, r, "user:mallory", "create:*", "use:sandbox:local")
	hers, err := r.create(mallory(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	return r, owned.FigaroID, hers.FigaroID
}

func parkedOn(t *testing.T, r grantRig, parent string) []rpc.Proposal {
	t.Helper()
	out, err := r.call(asOwner(), rpc.MethodParked, rpc.ParkedRequest{FigaroID: parent})
	if err != nil {
		t.Fatal(err)
	}
	return out.(rpc.ParkedResponse).Proposals
}

func consent(r grantRig, ctx context.Context, method, parent, id string) error {
	_, err := r.call(ctx, method, rpc.ConsentRequest{FigaroID: parent, ID: id})
	return err
}

// Rule 3: mallory may move her own X, but Q is the owner's. Without adopt on
// Q the move waits on Q; she cannot accept it herself, and the owner can.
func TestAMoveUnderAnotherOwnersAriaWaitsForItsConsent(t *testing.T) {
	r, q, x := adoptRig(t)
	grantTo(t, r, "user:mallory", "read:*", "set:aria:"+x, "kill:aria:"+x)

	err := setTopology(t, r, mallory(), map[string]string{x: q})
	if err == nil || !strings.Contains(err.Error(), "waits on "+q) {
		t.Fatalf("mallory drawing X under the owner's Q = %v, want it parked on Q", err)
	}
	if up := presentedUnder(t, r, x); up == q {
		t.Fatal("a parked move was applied")
	}
	props := parkedOn(t, r, q)
	if len(props) != 1 || props[0].By != "user:mallory" || props[0].Moves[0].Node != x {
		t.Fatalf("parked on Q: %+v, want mallory's move of X", props)
	}
	id := props[0].ID

	if err := consent(r, mallory(), rpc.MethodAccept, q, id); err == nil || !strings.Contains(err.Error(), "adopt") {
		t.Fatalf("mallory accepting on Q = %v, want refused for adopt", err)
	}
	if len(parkedOn(t, r, q)) != 1 {
		t.Fatal("a refused accept consumed the proposal")
	}
	if err := consent(r, asOwner(), rpc.MethodAccept, q, id); err != nil {
		t.Fatalf("the owner accepting: %v", err)
	}
	if up := presentedUnder(t, r, x); up != q {
		t.Fatalf("after accept X is drawn under %q, want Q %s", up, q)
	}
	if len(parkedOn(t, r, q)) != 0 {
		t.Fatal("an accepted proposal still waits")
	}
}

// G6: accept runs as mallory with the grants she holds now. Her kill on X
// was revoked after she proposed, so the owner's accept is refused.
func TestAcceptChecksTheProposersCurrentGrants(t *testing.T) {
	r, q, x := adoptRig(t)
	grantTo(t, r, "user:mallory", "read:*", "set:aria:"+x, "kill:aria:"+x)
	if err := setTopology(t, r, mallory(), map[string]string{x: q}); err == nil {
		t.Fatal("the move did not park")
	}
	id := parkedOn(t, r, q)[0].ID

	grantTo(t, r, "user:mallory", "read:*")
	err := consent(r, asOwner(), rpc.MethodAccept, q, id)
	if err == nil || !strings.Contains(err.Error(), "kill on aria:"+x) {
		t.Fatalf("accepting a revoked proposer's move = %v, want refused on kill for X", err)
	}
	if up := presentedUnder(t, r, x); up == q {
		t.Fatal("a revoked proposer's move was applied")
	}
}

func TestARefusedProposalIsGone(t *testing.T) {
	r, q, x := adoptRig(t)
	grantTo(t, r, "user:mallory", "read:*", "set:aria:"+x, "kill:aria:"+x)
	if err := setTopology(t, r, mallory(), map[string]string{x: q}); err == nil {
		t.Fatal("the move did not park")
	}
	id := parkedOn(t, r, q)[0].ID
	if err := consent(r, asOwner(), rpc.MethodRefuse, q, id); err != nil {
		t.Fatal(err)
	}
	if err := consent(r, asOwner(), rpc.MethodAccept, q, id); err == nil {
		t.Fatal("a refused proposal was accepted")
	}
	if up := presentedUnder(t, r, x); up == q {
		t.Fatal("a refused move was applied")
	}
}

func TestAdoptOnTheReceivingParentMovesAtOnce(t *testing.T) {
	r, q, x := adoptRig(t)
	grantTo(t, r, "user:mallory", "read:*", "set:aria:"+x, "kill:aria:"+x, "adopt:aria:"+q)
	if err := setTopology(t, r, mallory(), map[string]string{x: q}); err != nil {
		t.Fatalf("a move with adopt on Q: %v", err)
	}
	if up := presentedUnder(t, r, x); up != q {
		t.Fatalf("X is drawn under %q, want Q %s", up, q)
	}
}

// Both sides one owner: no consent is asked, though the caller holds no
// adopt. The kill floor still applies.
func TestAMoveBetweenOneOwnersAriasNeedsNoConsent(t *testing.T) {
	r, q, _ := adoptRig(t)
	a, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	grantTo(t, r, "user:mallory", "read:*", "set:aria:"+a.FigaroID, "kill:aria:"+a.FigaroID)
	if err := setTopology(t, r, mallory(), map[string]string{a.FigaroID: q}); err != nil {
		t.Fatalf("drawing the owner's A under the owner's Q: %v", err)
	}
	if up := presentedUnder(t, r, a.FigaroID); up != q {
		t.Fatalf("A is drawn under %q, want Q %s", up, q)
	}
	if len(parkedOn(t, r, q)) != 0 {
		t.Fatal("a same-owner move was parked")
	}
}
