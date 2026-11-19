package angelus

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

type seatFixture struct {
	hs                    *Handlers
	b                     *store.XwalBackend
	top, pred, kid, other string
	successor             string
}

func newSeatFixture(t *testing.T) seatFixture {
	t.Helper()
	hs, b, outfit := demoteHandlers(t)
	f := seatFixture{hs: hs, b: b}
	var err error
	f.top, _ = b.CreateConversation(outfit)
	if _, f.pred, err = b.Fork(f.top); err != nil {
		t.Fatal(err)
	}
	if _, f.kid, err = b.Fork(f.pred); err != nil {
		t.Fatal(err)
	}
	f.other, _ = b.CreateConversation(outfit)
	if _, f.successor, err = b.Fork(f.other); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f seatFixture) drawn(id string) string {
	p, _ := f.b.HierarchyEngine().Drawn(id)
	return p
}

func (f seatFixture) overrides(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(f.b.HierarchyEngine().Overrides())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f seatFixture) seatAs(t *testing.T, figaro, pred, successor string) rpc.SeatResponse {
	t.Helper()
	req := rpc.SeatRequest{FigaroID: pred, Successor: successor}
	raw, _ := json.Marshal(req)
	if figaro != "" {
		var err error
		if raw, err = rpc.WithCaller(req, figaro, nil); err != nil {
			t.Fatal(err)
		}
	}
	out, err := f.hs.Map[rpc.MethodSeat](inProcess(context.Background()), raw)
	if err != nil {
		t.Fatal(err)
	}
	return out.(rpc.SeatResponse)
}

func (f seatFixture) seat(t *testing.T) rpc.SeatResponse {
	t.Helper()
	return f.seatAs(t, "", f.pred, f.successor)
}

// H1 review F1(a): a seat the successor's own max_children 0 cannot hold
// is refused whole; the drawn tree is what it was, byte for byte.
func TestASeatRefusedByTheSuccessorsLimitMovesNothing(t *testing.T) {
	f := newSeatFixture(t)
	setMaxChildren(t, f.b, f.successor, 0)
	before := f.overrides(t)

	seat := f.seat(t)
	if seat.FailedAt != "place" || seat.Placed || !strings.Contains(seat.Error, "moved nothing") {
		t.Fatalf("seat = %+v, want a refusal that moved nothing", seat)
	}
	if got := f.drawn(f.successor); got != f.other {
		t.Fatalf("the successor is drawn under %q, want still under %s", got, f.other)
	}
	if after := f.overrides(t); !bytes.Equal(before, after) {
		t.Fatalf("drawn overrides changed: before %s, after %s", before, after)
	}
}

func TestARefusedSeatLeavesTheSuccessorsLifetime(t *testing.T) {
	f := newSeatFixture(t)
	applyKeys(t, f.b, f.pred, map[string]any{store.SystemTTLKey: "0"})
	applyKeys(t, f.b, f.successor, map[string]any{store.SystemTTLKey: "2h"})
	setMaxChildren(t, f.b, f.successor, 0)

	if seat := f.seat(t); seat.FailedAt != "place" || seat.Placed {
		t.Fatalf("seat = %+v, want a refusal that moved nothing", seat)
	}
	snap, err := f.b.FormState(f.successor)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := snap.Get(store.SystemTTLKey); string(got) != `"2h"` {
		t.Fatalf("a refused seat gave the successor system.ttl %s, want its own \"2h\"", got)
	}
}

// rel/v0.74.5 review F-C: a seat that has landed lands again with no moves,
// so a stopped succeed reruns straight to the cast.
func TestASeatThatHasLandedLandsAgainWithNoMoves(t *testing.T) {
	f := newSeatFixture(t)
	if seat := f.seat(t); seat.FailedAt != "" {
		t.Fatalf("seat failed at %s: %s", seat.FailedAt, seat.Error)
	}
	before := f.overrides(t)
	again := f.seat(t)
	if again.FailedAt != "" {
		t.Fatalf("a rerun failed at %s: %s", again.FailedAt, again.Error)
	}
	if again.Placed || again.Demote.Under != f.successor || !again.Demote.Preferred {
		t.Fatalf("a rerun reported %+v, want the landed seat under %s and no moves", again, f.successor)
	}
	if after := f.overrides(t); !bytes.Equal(before, after) {
		t.Fatalf("a rerun moved something: before %s, after %s", before, after)
	}
}

// rel/v0.74.5 review F-F: the successor never counts against its
// predecessor's max_children, and the parent's count is net zero, so a seat
// lands with the predecessor and its parent both at their caps.
func TestASeatLandsWithThePredecessorAndItsParentAtTheirCaps(t *testing.T) {
	f := newSeatFixture(t)
	setMaxChildren(t, f.b, f.pred, 1)
	setMaxChildren(t, f.b, f.top, 1)
	setMaxChildren(t, f.b, f.successor, 2)

	seat := f.seat(t)
	if seat.FailedAt != "" {
		t.Fatalf("seat at the caps failed at %s: %s", seat.FailedAt, seat.Error)
	}
	for id, want := range map[string]string{f.successor: f.top, f.pred: f.successor, f.kid: f.successor} {
		if got := f.drawn(id); got != want {
			t.Fatalf("%s is drawn under %q, want %s", id, got, want)
		}
	}
}

// The forked successor of a capped predecessor takes its place and its
// limit in one seat, with the grandparent at its cap too.
func TestASeatOfAForkAtTheCapsTakesThePlaceAndTheLimit(t *testing.T) {
	f := newSeatFixture(t)
	setMaxChildren(t, f.b, f.pred, 2)
	setMaxChildren(t, f.b, f.top, 1)
	out, err := callForkAsFigaro(t, f.hs, f.pred, rpc.ForkRequest{FigaroID: f.pred})
	if err != nil {
		t.Fatal(err)
	}
	succ := out.Alternative

	if seat := f.seatAs(t, "", f.pred, succ); seat.FailedAt != "" {
		t.Fatalf("seat failed at %s: %s", seat.FailedAt, seat.Error)
	}
	if got := f.drawn(succ); got != f.top {
		t.Fatalf("the successor is drawn under %q, want %s", got, f.top)
	}
	if v, _ := maxChildrenOnBackend(t, f.b, succ); v != "2" {
		t.Fatalf("successor max_children after the seat = %s, want the predecessor's 2", v)
	}
	if v, _ := maxChildrenOnBackend(t, f.b, f.pred); v != "0" {
		t.Fatalf("predecessor max_children after the seat = %s, want 0", v)
	}
}

func TestAFigaroSeatsOnlyItsOwnForkAndMovesNothingOtherwise(t *testing.T) {
	f := newSeatFixture(t)
	before := f.overrides(t)
	seat := f.seatAs(t, f.pred, f.pred, f.successor)
	if seat.FailedAt != "place" || !strings.Contains(seat.Error, "owner") {
		t.Fatalf("seat = %+v, want a refusal at place that names the owner", seat)
	}
	if after := f.overrides(t); !bytes.Equal(before, after) {
		t.Fatalf("a refused seat moved something: before %s, after %s", before, after)
	}
}
