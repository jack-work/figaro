package angelus_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/store"
)

const guardPolicy = `
[roles.owner]
grants = ["*:*"]

[[bind]]
role = "owner"
principals = ["user:SELF"]
`

func guardChain(t *testing.T) (r grantRig, p, x, c string) {
	t.Helper()
	r = newRigWith(t, guardPolicy)
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	root, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	p = root.FigaroID
	x = ariasFork(t, r, p)
	c = ariasFork(t, r, x)
	return r, p, x, c
}

func grantTo(t *testing.T, r grantRig, principal string, grants ...string) {
	t.Helper()
	quoted := make([]string, len(grants))
	for i, g := range grants {
		quoted[i] = strconv.Quote(g)
	}
	text := guardPolicy + "\n[roles.guest]\ngrants = [" + strings.Join(quoted, ", ") + "]\n\n[[bind]]\nrole = \"guest\"\nprincipals = [\"" + principal + "\"]\n"
	text = strings.ReplaceAll(text, "SELF", strconv.Itoa(os.Getuid()))
	if err := os.WriteFile(r.policy, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.a.Policies.Reload(r.policy); err != nil {
		t.Fatal(err)
	}
}

func mallory() context.Context { return as("user", "mallory") }

// El Inspector XIV's kill floor: set and kill on X alone do not reach P, the
// parent X climbs past, so the promote is refused and nothing moves.
func TestSetAndKillOnXAloneCannotPromoteXOverItsParent(t *testing.T) {
	r, p, x, _ := guardChain(t)
	grantTo(t, r, "user:mallory", "read:aria:"+x, "set:aria:"+x, "kill:aria:"+x)

	_, err := r.call(mallory(), rpc.MethodPromote, rpc.PromoteRequest{FigaroID: x, Levels: 1})
	if err == nil || !strings.Contains(err.Error(), "kill on aria:"+p) {
		t.Fatalf("promote of X over P with no right on P = %v, want refused on kill for P", err)
	}
	if up := presentedUnder(t, r, x); up != p {
		t.Fatalf("a refused promote drew X under %q, want P %s", up, p)
	}

	grantTo(t, r, "user:mallory", "read:aria:"+x, "set:aria:"+x, "kill:aria:"+x, "kill:aria:"+p, "adopt:*")
	if _, err := r.call(mallory(), rpc.MethodPromote, rpc.PromoteRequest{FigaroID: x, Levels: 1}); err != nil {
		t.Fatalf("promote with kill on X and P: %v", err)
	}
	if up := presentedUnder(t, r, p); up != x {
		t.Fatalf("after the promote P is drawn under %q, want X %s", up, x)
	}
}

// Demote re-homes C: the caller needs kill on C as well as on X.
func TestSetAndKillOnXAloneCannotDemoteXOntoItsChild(t *testing.T) {
	r, p, x, c := guardChain(t)
	grantTo(t, r, "user:mallory", "read:aria:"+x, "set:aria:"+x, "kill:aria:"+x)

	_, err := r.call(mallory(), rpc.MethodDemote, rpc.DemoteRequest{FigaroID: x, Prefer: c})
	if err == nil || !strings.Contains(err.Error(), "kill on aria:"+c) {
		t.Fatalf("demote of X onto C with no right on C = %v, want refused on kill for C", err)
	}
	if up := presentedUnder(t, r, c); up != x {
		t.Fatalf("a refused demote drew C under %q, want X %s", up, x)
	}

	grantTo(t, r, "user:mallory", "read:aria:"+x, "set:aria:"+x, "kill:aria:"+x, "kill:aria:"+c, "adopt:*")
	if _, err := r.call(mallory(), rpc.MethodDemote, rpc.DemoteRequest{FigaroID: x, Prefer: c}); err != nil {
		t.Fatalf("demote with kill on X and C: %v", err)
	}
	if up := presentedUnder(t, r, c); up != p {
		t.Fatalf("after the demote C is drawn under %q, want P %s", up, p)
	}
}

// The succession-tree drawn override goes through the same floor: set on
// both ends is not enough to draw X elsewhere.
func TestATopologyDrawNeedsKillOnTheMovedAria(t *testing.T) {
	r, p, x, c := guardChain(t)
	other, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	grantTo(t, r, "user:mallory", "read:*", "set:*", "kill:aria:"+p)
	if err := setTopology(t, r, mallory(), map[string]string{c: other.FigaroID}); err == nil || !strings.Contains(err.Error(), "kill on aria:"+c) {
		t.Fatalf("drawing C with set but no kill on C = %v, want refused", err)
	}
	if up := presentedUnder(t, r, c); up != x {
		t.Fatalf("a refused draw moved C under %q", up)
	}
}

// G1 and rule 2: the receiving parent was minted by another principal, so even the
// owner's move under it changes only the drawn tree. A parent of his own
// takes the storage move.
func TestAStorageMoveUnderAParentAnotherMintedIsDrawnOnly(t *testing.T) {
	r := newRigWith(t, guardPolicy)
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	mint := func() string {
		t.Helper()
		resp, err := r.create(asOwner(), "mock")
		if err != nil {
			t.Fatal(err)
		}
		return resp.FigaroID
	}
	grantTo(t, r, "user:other", "create:*", "use:sandbox:local")
	a, own := mint(), mint()
	other, err := r.create(as("user", "other"), "mock")
	if err != nil {
		t.Fatal(err)
	}
	boxed := other.FigaroID
	before, _ := r.backend.Node(a)

	if err := setTopology(t, r, asOwner(), map[string]string{a: boxed}); err != nil {
		t.Fatalf("the owner drawing A under a box-minted parent: %v", err)
	}
	if n, _ := r.backend.Node(a); n.Parent != before.Parent {
		t.Fatalf("A's history moved under %q; a box-minted parent must take a drawn move only", n.Parent)
	}
	if up := presentedUnder(t, r, a); up != boxed {
		t.Fatalf("A is drawn under %q, want the box-minted parent %s", up, boxed)
	}

	if err := setTopology(t, r, asOwner(), map[string]string{a: own}); err != nil {
		t.Fatal(err)
	}
	if n, _ := r.backend.Node(a); n.Parent != own {
		t.Fatalf("the owner's move under his own parent left A's history under %q, want %s", n.Parent, own)
	}
}

// S1 parity: the owner over the unix socket re-parents; an admin holding
// every grant on the web endpoint only draws.
func TestTheWebAdminOnlyDrawsWhereTheOwnerReparents(t *testing.T) {
	r := newRigWith(t, guardPolicy)
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	mint := func() string {
		t.Helper()
		resp, err := r.create(asOwner(), "mock")
		if err != nil {
			t.Fatal(err)
		}
		return resp.FigaroID
	}
	a, b, c := mint(), mint(), mint()
	grantTo(t, r, "user:admin", "*:*")
	admin := authz.WithEndpoint(as("user", "admin"), authz.EndpointWeb)
	before, _ := r.backend.Node(a)
	if err := setTopology(t, r, admin, map[string]string{a: b}); err != nil {
		t.Fatalf("the web admin drawing A under B: %v", err)
	}
	if n, _ := r.backend.Node(a); n.Parent != before.Parent {
		t.Fatalf("the web admin re-parented A's history under %q", n.Parent)
	}
	if up := presentedUnder(t, r, a); up != b {
		t.Fatalf("A is drawn under %q, want B %s", up, b)
	}
	owner := authz.WithEndpoint(asOwner(), authz.EndpointLocal)
	if err := setTopology(t, r, owner, map[string]string{a: c}); err != nil {
		t.Fatal(err)
	}
	if n, _ := r.backend.Node(a); n.Parent != c {
		t.Fatalf("the owner on the unix socket left A's history under %q, want %s", n.Parent, c)
	}
}

// kill -r authorizes every node it takes, through the guard.
func TestKillRecursiveNeedsKillOnEveryNodeItTakes(t *testing.T) {
	r, p, x, c := guardChain(t)
	grantTo(t, r, "user:mallory", "read:*", "kill:aria:"+p, "kill:aria:"+x)
	_, err := r.call(mallory(), rpc.MethodKill, rpc.KillRequest{FigaroID: p, Recursive: true, Now: true})
	if err == nil || !strings.Contains(err.Error(), "kill on aria:"+c) {
		t.Fatalf("kill -r with no right on C = %v, want refused", err)
	}
	for _, id := range []string{p, x, c} {
		if _, ok := r.backend.Node(id); !ok {
			t.Fatalf("a refused kill -r took %s", id)
		}
	}
}

// El Inspector XIV, C1: a kill with grace reaps exactly the set it
// authorized. An aria drawn under X after the check is not reaped, and the
// kill says the set changed.
func TestAKillWithGraceReapsOnlyWhatItAuthorized(t *testing.T) {
	r, _, x, c := guardChain(t)
	y, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	r.hs.AfterKillCheckForTest(func() {
		if err := setTopology(t, r, asOwner(), map[string]string{y.FigaroID: x}); err != nil {
			t.Error(err)
		}
	})
	_, err = r.call(asOwner(), rpc.MethodKill, rpc.KillRequest{FigaroID: x, Recursive: true})
	r.hs.AfterKillCheckForTest(nil)
	if !errors.Is(err, store.ErrDeleteSetChanged) {
		t.Fatalf("kill -r with grace after the set grew = %v, want ErrDeleteSetChanged", err)
	}
	for _, id := range []string{x, c, y.FigaroID} {
		if m, _ := r.backend.Meta(id); m != nil && m.ReapedAtMS > 0 {
			t.Fatalf("%s was reaped by a kill whose authorized set did not hold it", id)
		}
	}
}

// C2: the sweep's delete re-reads its holds when it commits. A live agent
// holds an unreaped aria against the daemon's delete.
func TestTheDaemonsDeleteRereadsItsHolds(t *testing.T) {
	r := newRigWith(t, guardPolicy)
	live, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	r.a.SweepLifetimesNow()
	err = r.hs.RemoveAriaIf(live.FigaroID, []string{live.FigaroID})
	if err == nil || !strings.Contains(err.Error(), "an agent is live") {
		t.Fatalf("the daemon's delete of a live aria = %v, want held by the live agent", err)
	}
	if _, ok := r.backend.Node(live.FigaroID); !ok {
		t.Fatal("the daemon deleted an aria a live agent held")
	}
}
