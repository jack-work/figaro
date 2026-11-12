package angelus_test

import (
	"context"
	"os"
	"strconv"
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

// El Inspector XIV, P1: one minter on both sides is not the parent's
// consent. Mallory holds kill on the owner's A and only read on the owner's
// Q; drawing A under Q waits on Q. The owner's own move asks nothing.
func TestOneMinterOnBothSidesIsNotTheParentsConsent(t *testing.T) {
	r, q, _ := adoptRig(t)
	a, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	grantTo(t, r, "user:mallory", "read:*", "set:aria:"+a.FigaroID, "kill:aria:"+a.FigaroID)
	err = setTopology(t, r, mallory(), map[string]string{a.FigaroID: q})
	if err == nil || !strings.Contains(err.Error(), "waits on "+q) {
		t.Fatalf("mallory drawing the owner's A under the owner's Q = %v, want it parked on Q", err)
	}
	if up := presentedUnder(t, r, a.FigaroID); up == q {
		t.Fatal("a parked move was applied")
	}
	b, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if err := setTopology(t, r, asOwner(), map[string]string{b.FigaroID: q}); err != nil {
		t.Fatalf("the owner drawing his B under his Q: %v", err)
	}
	if up := presentedUnder(t, r, b.FigaroID); up != q {
		t.Fatalf("B is drawn under %q, want Q %s", up, q)
	}
	if n := len(parkedOn(t, r, q)); n != 1 {
		t.Fatalf("%d proposals wait on Q, want mallory's one", n)
	}
}

// El Inspector XIV, P3: a form receives a node as an aria does, so drawing
// mallory's X under the owner's role form waits on the form.
func TestAFormThatReceivesANodeIsAskedForConsent(t *testing.T) {
	r, _, x := adoptRig(t)
	role := ownerMintsForm(t, r, "a role")
	grantTo(t, r, "user:mallory", "read:*", "set:aria:"+x, "kill:aria:"+x)

	err := setTopology(t, r, mallory(), map[string]string{x: role})
	if err == nil || !strings.Contains(err.Error(), "waits on "+role) {
		t.Fatalf("mallory drawing X under the owner's form = %v, want it parked on the form", err)
	}
	if up := presentedUnder(t, r, x); up == role {
		t.Fatal("a parked move was applied")
	}
	props := parkedOn(t, r, role)
	if len(props) != 1 {
		t.Fatalf("parked on the form: %+v, want mallory's move", props)
	}
	if err := consent(r, asOwner(), rpc.MethodAccept, role, props[0].ID); err != nil {
		t.Fatalf("the owner accepting on his form: %v", err)
	}
	if up := presentedUnder(t, r, x); up != role {
		t.Fatalf("after accept X is drawn under %q, want the form %s", up, role)
	}
}

func grantEach(t *testing.T, r grantRig, grants map[string][]string) {
	t.Helper()
	text := guardPolicy
	for principal, gs := range grants {
		quoted := make([]string, len(gs))
		for i, g := range gs {
			quoted[i] = strconv.Quote(g)
		}
		role := strings.NewReplacer(":", "-", "@", "-").Replace(principal)
		text += "\n[roles." + role + "]\ngrants = [" + strings.Join(quoted, ", ") + "]\n\n[[bind]]\nrole = \"" + role + "\"\nprincipals = [\"" + principal + "\"]\n"
	}
	text = strings.ReplaceAll(text, "SELF", strconv.Itoa(os.Getuid()))
	if err := os.WriteFile(r.policy, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.a.Policies.Reload(r.policy); err != nil {
		t.Fatal(err)
	}
}

// El Inspector XIV, P5: the proposals on a parent are shown whole only to
// its adopt holders; a proposer sees its own; read alone sees none. Every id
// shown is one the caller may read.
func TestParkedProposalsShowOnlyToTheirHoldersAndProposers(t *testing.T) {
	r, q, x := adoptRig(t)
	grantTo(t, r, "user:eve", "create:*", "use:sandbox:local")
	ev, err := r.create(as("user", "eve"), "mock")
	if err != nil {
		t.Fatal(err)
	}
	y := ev.FigaroID
	grantEach(t, r, map[string][]string{
		"user:mallory": {"read:*", "set:aria:" + x, "kill:aria:" + x},
		"user:eve":     {"read:*", "set:aria:" + y, "kill:aria:" + y},
		"user:ada":     {"read:aria:" + q, "adopt:aria:" + q},
		"user:zed":     {"read:aria:" + q},
	})
	if err := setTopology(t, r, mallory(), map[string]string{x: q}); err == nil {
		t.Fatal("mallory's move did not park")
	}
	if err := setTopology(t, r, as("user", "eve"), map[string]string{y: q}); err == nil {
		t.Fatal("eve's move did not park")
	}
	list := func(ctx context.Context) []rpc.Proposal {
		t.Helper()
		out, err := r.call(ctx, rpc.MethodParked, rpc.ParkedRequest{FigaroID: q})
		if err != nil {
			t.Fatal(err)
		}
		return out.(rpc.ParkedResponse).Proposals
	}

	if got := list(as("user", "zed")); len(got) != 0 {
		t.Fatalf("read on Q alone lists %+v, want nothing", got)
	}
	mine := list(mallory())
	if len(mine) != 1 || mine[0].By != "user:mallory" || len(mine[0].Moves) != 1 || mine[0].Moves[0].Node != x {
		t.Fatalf("mallory lists %+v, want her own move of X alone", mine)
	}
	held := list(as("user", "ada"))
	if len(held) != 2 {
		t.Fatalf("Q's adopt holder lists %d proposals, want both", len(held))
	}
	for _, p := range held {
		if len(p.Moves) != 0 || p.Withheld != 1 {
			t.Fatalf("ada, who cannot read the moved arias, is shown %+v; want them withheld", p)
		}
	}
	if all := list(asOwner()); len(all) != 2 || len(all[0].Moves) != 1 || all[0].Withheld != 0 {
		t.Fatalf("the owner lists %+v, want both in full", all)
	}
}
