package angelus_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/store"
)

func (r licenseRig) seatFrom(ctx context.Context, pred, successor string) (rpc.SeatResponse, error) {
	out, err := r.call(ctx, rpc.MethodSeat, rpc.SeatRequest{FigaroID: pred, Successor: successor})
	if err != nil {
		return rpc.SeatResponse{}, err
	}
	return out.(rpc.SeatResponse), nil
}

func (r licenseRig) bornWithTTL(t *testing.T, parent, ttl string) string {
	t.Helper()
	out, err := r.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: parent, Patch: heldPatch(t, map[string]any{store.SystemTTLKey: ttl})})
	if err != nil {
		t.Fatal(err)
	}
	return out.(rpc.ForkResponse).Alternative
}

// succeed --successor seats an existing figaro with figaro.seat. It takes the
// holder's lifetime only when the holder or the owner asks and the holder
// has not retired, so a successor born under a finite ttl does not die in
// the seat, and no other figaro can hand one its holder's lifetime.
func TestSeatingAnExistingSuccessorCarriesTheHoldersLifetime(t *testing.T) {
	g := newRigWith(t, defaultPolicy)
	if err := wire.Install(g.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	root, err := g.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	r := licenseRig{grantRig: g, root: root.FigaroID, x: ariasFork(t, g, root.FigaroID)}
	ttl := func(id string) string {
		v, _ := keyOn(t, r.grantRig, id, store.SystemTTLKey)
		return string(v)
	}
	if err := r.setFrom(localOwner(), t, r.x, map[string]any{store.SystemTTLKey: "0"}); err != nil {
		t.Fatal(err)
	}

	stranger := ariasFork(t, r.grantRig, r.root)
	succ := r.bornWithTTL(t, r.x, "2h")
	if _, err := r.seatFrom(toolOf(stranger), r.x, succ); err == nil || !strings.Contains(err.Error(), "only "+r.x+" itself, an aria it is drawn under, or the owner") {
		t.Fatalf("another figaro's tool seated a successor in %s's place: %v", r.x, err)
	}
	if got := ttl(succ); got != `"2h"` {
		t.Fatalf("a refused seat changed the successor's ttl to %s", got)
	}

	unrelated := r.bornWithTTL(t, r.root, "2h")
	if res, err := r.seatFrom(toolOf(r.x), r.x, unrelated); err != nil || res.FailedAt != "place" {
		t.Fatalf("the holder's tool seated a figaro that is not its fork: %v %+v", err, res)
	}
	if got := ttl(unrelated); got != `"2h"` {
		t.Fatalf("a seat refused up front changed the named successor's ttl to %s", got)
	}

	res, err := r.seatFrom(toolOf(r.x), r.x, succ)
	if err != nil || res.FailedAt != "" {
		t.Fatalf("the holder's tool seating its successor: %v %+v", err, res)
	}
	if got := ttl(succ); got != `"0"` {
		t.Fatalf("the seated successor's system.ttl = %s, want the holder's \"0\"", got)
	}

	retired := ariasFork(t, r.grantRig, r.root)
	if err := r.setFrom(localOwner(), t, retired, map[string]any{store.SystemRetiredKey: store.RetiredManual, store.SystemTTLKey: "10m"}); err != nil {
		t.Fatal(err)
	}
	late := r.bornWithTTL(t, r.root, "2h")
	if _, err := r.seatFrom(localOwner(), retired, late); err != nil {
		t.Fatal(err)
	}
	if got := ttl(late); got != `"2h"` {
		t.Fatalf("a retired holder's lifetime reached its successor: %s", got)
	}
}
