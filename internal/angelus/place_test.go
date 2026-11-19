package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

// P draws X and S side by side; the owner minted all three, so drawing S
// under X asks nobody, and the history under P stays where it was.
func TestPlaceDrawsAFigaroUnderItsSibling(t *testing.T) {
	r, p, x, _ := guardChain(t)
	s := ariasFork(t, r, p)

	out, err := r.call(asOwner(), rpc.MethodPlace, rpc.PlaceRequest{FigaroID: s, Under: x})
	if err != nil {
		t.Fatalf("placing S under its sibling X: %v", err)
	}
	resp := out.(rpc.PlaceResponse)
	if resp.Parked != nil || !resp.Moved {
		t.Fatalf("place = %+v, want moved at once", resp)
	}
	if up := presentedUnder(t, r, s); up != x {
		t.Fatalf("S is drawn under %q, want X %s", up, x)
	}
	if n, _ := r.backend.Node(s); n.Parent != p {
		t.Fatalf("S's history moved to %q, want it under P %s", n.Parent, p)
	}
}

// Mallory may move her own X, but Q is the owner's: the place parks on Q
// and moves only when the owner accepts.
func TestPlaceUnderAForeignParentWaitsForItsConsent(t *testing.T) {
	r, q, x := adoptRig(t)
	grantTo(t, r, "user:mallory", "read:*", "set:aria:"+x, "kill:aria:"+x)

	out, err := r.call(mallory(), rpc.MethodPlace, rpc.PlaceRequest{FigaroID: x, Under: q})
	if err != nil {
		t.Fatalf("place under the owner's Q: %v", err)
	}
	resp := out.(rpc.PlaceResponse)
	if resp.Parked == nil || resp.Parked.Parent != q {
		t.Fatalf("place = %+v, want parked on Q %s", resp, q)
	}
	if up := presentedUnder(t, r, x); up == q {
		t.Fatal("a parked place was applied")
	}
	if err := consent(r, asOwner(), rpc.MethodAccept, q, resp.Parked.ID); err != nil {
		t.Fatalf("the owner accepting: %v", err)
	}
	if up := presentedUnder(t, r, x); up != q {
		t.Fatalf("after accept X is drawn under %q, want Q %s", up, q)
	}
}

func TestPlaceUnderItsOwnDescendantIsRefused(t *testing.T) {
	r, p, x, c := guardChain(t)
	_, err := r.call(asOwner(), rpc.MethodPlace, rpc.PlaceRequest{FigaroID: x, Under: c})
	if err == nil || !strings.Contains(err.Error(), "drawn under it") {
		t.Fatalf("placing X under its child C = %v, want a cycle refused", err)
	}
	if up := presentedUnder(t, r, x); up != p {
		t.Fatalf("a refused place drew X under %q, want P %s", up, p)
	}
	if _, err := r.call(asOwner(), rpc.MethodPlace, rpc.PlaceRequest{FigaroID: x, Under: x}); err == nil {
		t.Fatal("placing X under itself was accepted")
	}
}

func TestPlaceThatOverfillsTheParentIsRefused(t *testing.T) {
	r, p, x, _ := guardChain(t)
	s := ariasFork(t, r, p)
	snap, err := r.backend.FormState(x)
	if err != nil {
		t.Fatal(err)
	}
	one, _ := json.Marshal(1)
	if _, err := r.backend.ApplyForm(x, form.Build(snap, map[string]json.RawMessage{form.FanoutMaxChildrenKey: one}, nil)); err != nil {
		t.Fatal(err)
	}

	_, err = r.call(asOwner(), rpc.MethodPlace, rpc.PlaceRequest{FigaroID: s, Under: x})
	if err == nil || !strings.Contains(err.Error(), "fanout") {
		t.Fatalf("placing S under X, already holding C at max_children 1 = %v, want fanout refused", err)
	}
	if up := presentedUnder(t, r, s); up != p {
		t.Fatalf("a refused place drew S under %q, want P %s", up, p)
	}
}

func TestPlaceResolvesItsParentByPrefix(t *testing.T) {
	r, p, x, _ := guardChain(t)
	s := ariasFork(t, r, p)
	if _, err := r.call(asOwner(), rpc.MethodPlace, rpc.PlaceRequest{FigaroID: s, Under: x[:8]}); err != nil {
		t.Fatalf("placing S under a prefix of X: %v", err)
	}
	if up := presentedUnder(t, r, s); up != x {
		t.Fatalf("S is drawn under %q, want X %s", up, x)
	}
}

// A seat redraws: the named successor keeps its history where it was, and
// one with branches of its own takes them along.
func TestASeatDrawsTheSuccessorWithoutMovingItsHistory(t *testing.T) {
	r, p, x, _ := guardChain(t)
	other, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	n := ariasFork(t, r, other.FigaroID)
	branch := ariasFork(t, r, n)
	before, _ := r.backend.Node(n)

	out, err := r.call(asOwner(), rpc.MethodSeat, rpc.SeatRequest{FigaroID: x, Successor: n})
	if err != nil {
		t.Fatal(err)
	}
	if seat := out.(rpc.SeatResponse); seat.FailedAt != "" {
		t.Fatalf("seat failed at %s: %s", seat.FailedAt, seat.Error)
	}
	if up := presentedUnder(t, r, n); up != p {
		t.Fatalf("N is drawn under %q, want X's place under P %s", up, p)
	}
	if up := presentedUnder(t, r, x); up != n {
		t.Fatalf("X is drawn under %q, want N %s", up, n)
	}
	if up := presentedUnder(t, r, branch); up != n {
		t.Fatalf("N's branch is drawn under %q, want N", up)
	}
	if after, _ := r.backend.Node(n); after.Parent != before.Parent {
		t.Fatalf("N's history moved from %q to %q", before.Parent, after.Parent)
	}
}

// A seat that would wait on a parent's consent moves nothing and leaves no
// proposal behind: a seat lands whole or not at all.
func TestASeatThatWouldWaitOnConsentMovesNothing(t *testing.T) {
	r, p, _, _ := guardChain(t)
	other, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	n := ariasFork(t, r, other.FigaroID)
	grantTo(t, r, "user:mallory", "read:*", "set:*", "kill:*", "adopt:aria:"+p)

	out, err := r.call(mallory(), rpc.MethodSeat, rpc.SeatRequest{FigaroID: p, Successor: n})
	if err != nil {
		t.Fatal(err)
	}
	seat := out.(rpc.SeatResponse)
	if seat.FailedAt != "place" || seat.Placed || !strings.Contains(seat.Error, "moved nothing") {
		t.Fatalf("seat = %+v, want a refusal that moved nothing", seat)
	}
	if seat.Demote.Parked != nil || seat.PlaceParked != nil {
		t.Fatalf("the reply names a proposal: %+v", seat)
	}
	if up := presentedUnder(t, r, n); up != other.FigaroID {
		t.Fatalf("N is drawn under %q, want still under %s", up, other.FigaroID)
	}
	if left := append(parkedOn(t, r, n), parkedOn(t, r, p)...); len(left) != 0 {
		t.Fatalf("proposals wait after a refused seat: %+v", left)
	}
}
