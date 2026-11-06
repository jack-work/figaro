package angelus

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
)

// An aria's fate (mid-move, moved, or living on another node) is as private
// as the aria. A caller who may not read an id, nor use the verb it called on
// it, must hear exactly what it hears for an id that never existed: the same
// error, naming no node. The owner still hears the truth.

const (
	fateNode  = "f0f0f0f0-0000-4000-8000-00000000f0f0"
	neverID   = "0123456789abcdef0123456789abcdef"
	guestName = "guest"
)

type fateRig struct {
	t      *testing.T
	h      *handlers
	hs     *Handlers
	target string
	guests string
	other  string
	owner  context.Context
	guest  context.Context
}

func newFateRig(t *testing.T) *fateRig {
	t.Helper()
	h, hs := resolveHandlers(t)
	h.angelus.Hubs = newHubs()
	r := &fateRig{t: t, h: h, hs: hs, target: mintConversation(t, h), guests: mintConversation(t, h), other: mintConversation(t, h)}
	h.angelus.Policies = authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "owner"}, nil)
	policy := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(policy, []byte(`
[roles.owner]
grants = [{ verbs = ["*"], on = ["*"] }]
[roles.guest]
grants = [{ verbs = ["*"], on = ["aria:`+r.guests+`"] }]
[[bind]]
role = "owner"
principals = ["user:owner"]
[[bind]]
role = "guest"
principals = ["user:`+guestName+`"]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.angelus.Policies.Reload(policy); err != nil {
		t.Fatal(err)
	}
	r.owner = authz.WithPrincipal(inProcess(context.Background()), rpc.Principal{Kind: rpc.PrincipalUser, ID: "owner"})
	r.guest = authz.WithPrincipal(inProcess(context.Background()), rpc.Principal{Kind: rpc.PrincipalUser, ID: guestName})
	return r
}

func (r *fateRig) call(ctx context.Context, method string, params map[string]any) error {
	r.t.Helper()
	raw, _ := json.Marshal(params)
	_, err := r.hs.Map[method](ctx, raw)
	return err
}

// hidden asserts the guest hears the same for ref as for baseline, an id it
// may not read that has no fate to tell, and that the answer names no node.
func (r *fateRig) hidden(method string, params func(ref string) map[string]any, ref, baseline string) {
	r.t.Helper()
	got := r.call(r.guest, method, params(ref))
	want := r.call(r.guest, method, params(baseline))
	if got == nil && want == nil {
		return
	}
	if got == nil || want == nil || strings.ReplaceAll(want.Error(), baseline, ref) != got.Error() {
		r.t.Fatalf("%s: the guest tells the hidden aria from a missing one:\n  hidden:  %v\n  missing: %v", method, got, want)
	}
	if strings.Contains(got.Error(), fateNode) || strings.Contains(got.Error(), r.h.nodeID()) {
		r.t.Fatalf("%s: the guest's refusal names a node: %v", method, got)
	}
}

func attachTo(ref string) map[string]any { return map[string]any{"figaro_id": ref} }

func setOn(ref string) map[string]any {
	return map[string]any{"figaro_id": ref, "patch": map[string]any{"set": map[string]json.RawMessage{"mantra": json.RawMessage(`"peek"`)}}}
}

func (r *fateRig) bothVerbs(ref, baseline string) {
	r.t.Helper()
	r.hidden(rpc.MethodAttach, attachTo, ref, baseline)
	r.hidden(rpc.MethodSet, setOn, ref, baseline)
}

func (r *fateRig) depart(id string) string {
	r.t.Helper()
	be, ok := r.h.angelus.Backend.(*store.XwalBackend)
	if !ok {
		r.t.Fatal("the test backend cannot move arias")
	}
	comp, err := be.Component(id)
	if err != nil {
		r.t.Fatal(err)
	}
	token := "0000000000000000000000000000beef"
	if err := be.Moves().Depart(store.Departure{Token: token, To: fateNode, AtMS: 1, Component: comp}); err != nil {
		r.t.Fatal(err)
	}
	return token
}

func (r *fateRig) dropLocalCopy(id string) {
	r.t.Helper()
	if err := r.h.angelus.Backend.Remove(id, true); err != nil {
		r.t.Fatal(err)
	}
}

func TestAnAriaMidMoveIsNotFoundToACallerWhoCannotReadIt(t *testing.T) {
	r := newFateRig(t)
	if err := r.h.moving.claim([]string{r.target}, fateNode); err != nil {
		t.Fatal(err)
	}
	r.bothVerbs(r.target, r.other)

	if err := r.call(r.owner, rpc.MethodAttach, attachTo(r.target)); err == nil || !strings.Contains(err.Error(), "moving to node "+fateNode) {
		t.Fatalf("the owner must still hear the move: %v", err)
	}
}

func TestAPendingTombstoneIsNotFoundToACallerWhoCannotReadIt(t *testing.T) {
	r := newFateRig(t)
	r.depart(r.target)
	r.dropLocalCopy(r.target)
	r.bothVerbs(r.target, neverID)

	if err := r.call(r.owner, rpc.MethodAttach, attachTo(r.target)); err == nil || !strings.Contains(err.Error(), "moving to node "+fateNode) {
		t.Fatalf("the owner must still hear the move: %v", err)
	}
}

func TestAFinalTombstoneIsNotFoundToACallerWhoCannotReadIt(t *testing.T) {
	r := newFateRig(t)
	token := r.depart(r.target)
	r.dropLocalCopy(r.target)
	be := r.h.angelus.Backend.(*store.XwalBackend)
	if err := be.Moves().Settle(token); err != nil {
		t.Fatal(err)
	}
	r.bothVerbs(r.target, neverID)

	if err := r.call(r.owner, rpc.MethodAttach, attachTo(r.target)); err == nil || !strings.Contains(err.Error(), "moved to node "+fateNode) {
		t.Fatalf("the owner must still hear where it went: %v", err)
	}
}

// A secondary id (here the form a study names) is checked the same way.
func TestASecondaryIDMidMoveIsHiddenToo(t *testing.T) {
	r := newFateRig(t)
	if err := r.h.moving.claim([]string{r.target}, fateNode); err != nil {
		t.Fatal(err)
	}
	study := func(ref string) map[string]any { return map[string]any{"figaro_id": r.guests, "form_id": ref} }
	r.hidden(rpc.MethodStudy, study, r.target, r.other)
}

type fateMembers struct{}

func (fateMembers) Version() uint64                         { return 1 }
func (fateMembers) PolicyVersion() uint64                   { return 1 }
func (fateMembers) Nodes() []string                         { return []string{fateNode} }
func (fateMembers) Member(node string) (*peer.Member, bool) { return nil, node == fateNode }
func (fateMembers) NodeOf(*x509.Certificate) (string, bool) { return "", false }
func (fateMembers) Left(*x509.Certificate) (string, bool)   { return "", false }

type fateDir struct {
	owners map[string]string
	locate error
}

func (d fateDir) Owner(id string) (string, bool) { n, ok := d.owners[id]; return n, ok }
func (d fateDir) Learn(string, string)           {}
func (d fateDir) Forget(string)                  {}
func (d fateDir) Locate(_ context.Context, ref string) (string, string, error) {
	if d.locate != nil && ref != neverID {
		return "", "", d.locate
	}
	return "", "", fmt.Errorf("%s: %w", ref, peer.ErrNoOwner)
}

const remoteID = "fedcba9876543210fedcba9876543210"

// A caller without a bearer cannot leave this node; telling it which node an
// aria lives on is the aria's business, not the caller's.
func TestTheNodeOfARemoteAriaIsHiddenFromACallerWhoCannotReadIt(t *testing.T) {
	r := newFateRig(t)
	r.h.fed = &federation{members: fateMembers{}, dir: fateDir{owners: map[string]string{remoteID: fateNode}}}
	r.bothVerbs(remoteID, neverID)

	if err := r.call(r.owner, rpc.MethodAttach, attachTo(remoteID)); err == nil || !strings.Contains(err.Error(), "is on node "+fateNode) {
		t.Fatalf("the owner must still hear where it lives: %v", err)
	}
}

// With a bearer the directory is asked; what it fails with names nodes, and a
// caller who cannot read the id hears not found instead.
func TestALookupFailureIsHiddenFromABearerWhoCannotReadIt(t *testing.T) {
	r := newFateRig(t)
	r.h.fed = &federation{members: fateMembers{}, dir: fateDir{locate: fmt.Errorf("%s is on nodes %s and another: name it as <node>/%s", remoteID, fateNode, remoteID)}}
	r.guest = authz.WithVerifiedBearer(r.guest, jkauthz.Subject{Principal: jkauthz.Principal{Kind: jkauthz.KindUser, ID: guestName}}, "bearer-of-the-guest")
	r.bothVerbs(remoteID, neverID)
}

// A node-qualified id names its node; the answer to a caller who may not read
// it must not say whether this node peers with that one, or what this node is.
func TestANodeQualifiedIDIsHiddenFromACallerWhoCannotReadIt(t *testing.T) {
	r := newFateRig(t)
	stranger := "abababab-0000-4000-8000-00000000abab"
	err := r.call(r.guest, rpc.MethodAttach, attachTo(stranger+"/"+remoteID))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("a node-qualified id the guest cannot read answered %v, want not found", err)
	}
	if strings.Contains(err.Error(), r.h.nodeID()) {
		t.Fatalf("the refusal names this node: %v", err)
	}
	if err := r.call(r.owner, rpc.MethodAttach, attachTo(stranger+"/"+remoteID)); err == nil || !strings.Contains(err.Error(), "does not peer") {
		t.Fatalf("the owner must still hear the node is not a peer: %v", err)
	}
}
