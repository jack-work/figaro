package angelus_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/store"
)

// P local, C minted by box judge and drawn under P by @topology only: C's
// history parent is elsewhere, so only the drawn tree puts it in P's set.
func drawnBoxChild(t *testing.T) (d doorRig, p, c string) {
	t.Helper()
	d = boxDoorRig(t, "true")
	if err := wire.Install(d.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	owned, err := d.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	var made rpc.CreateResponse
	if err := d.dial(t).Call(context.Background(), rpc.MethodCreate, rpc.CreateRequest{Outfits: []string{"mock"}}, &made); err != nil {
		t.Fatal(err)
	}
	p, c = owned.FigaroID, made.FigaroID
	if err := setTopology(t, d.grantRig, asOwner(), map[string]string{c: p}); err != nil {
		t.Fatal(err)
	}
	if n, _ := d.backend.Node(c); n.Parent == p {
		t.Fatal("the box's aria was re-parented in history; the test needs a drawn-only child")
	}
	if up := presentedUnder(t, d.grantRig, c); up != p {
		t.Fatalf("C is drawn under %q, want P", up)
	}
	return d, p, c
}

func killNow(t *testing.T, r grantRig, id string) rpc.KillResponse {
	t.Helper()
	out, err := r.call(asOwner(), rpc.MethodKill, rpc.KillRequest{FigaroID: id, Recursive: true, Now: true})
	if err != nil {
		t.Fatalf("kill -r --now %s: %v", id, err)
	}
	return out.(rpc.KillResponse)
}

func gone(t *testing.T, r grantRig, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, ok := r.backend.Node(id); ok {
			t.Errorf("%s survived its delete", id)
		}
		for _, other := range r.backend.ConversationIDs() {
			if other == id {
				t.Errorf("%s is still listed", id)
			}
		}
	}
}

// D1 (8): kill -r --now takes a drawn-only child by id, not P's history subtree.
func TestKillRecursiveTakesADrawnOnlyChild(t *testing.T) {
	d, p, c := drawnBoxChild(t)
	killNow(t, d.grantRig, p)
	gone(t, d.grantRig, p, c)
}

// D1 (4): purging a reaped root takes its whole group, and the reply names
// what was removed.
func TestPurgingAReapedRootTakesItsWholeGroup(t *testing.T) {
	d, p, c := drawnBoxChild(t)
	if _, err := d.call(asOwner(), rpc.MethodKill, rpc.KillRequest{FigaroID: p, Recursive: true, GraceMS: 3_600_000}); err != nil {
		t.Fatal(err)
	}
	group := map[string]bool{}
	for _, e := range d.backend.Reaped() {
		if e.Root == p {
			group[e.ID] = true
		}
	}
	if !group[p] || !group[c] || len(group) != 2 {
		t.Fatalf("reaped group of P = %v, want P and C", group)
	}
	resp := killNow(t, d.grantRig, p)
	gone(t, d.grantRig, p, c)
	if len(resp.Removed) != 2 || !group[resp.Removed[0]] || !group[resp.Removed[1]] {
		t.Fatalf("the purge reported %v removed, want P and C", resp.Removed)
	}
	for _, e := range d.backend.Reaped() {
		if e.ID == c {
			t.Fatal("C is still listed as reaped")
		}
	}
}

// D1 (1) and (2): when C has been drawn away since it was reaped with P, a
// purge of P would split the group; it is refused and both stay reaped.
func TestAPurgeThatWouldSplitItsGroupIsRefused(t *testing.T) {
	d, p, c := drawnBoxChild(t)
	if _, err := d.call(asOwner(), rpc.MethodKill, rpc.KillRequest{FigaroID: p, Recursive: true, GraceMS: 3_600_000}); err != nil {
		t.Fatal(err)
	}
	other, err := d.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if err := setTopology(t, d.grantRig, asOwner(), map[string]string{c: other.FigaroID}); err != nil {
		t.Fatal(err)
	}
	_, err = d.call(asOwner(), rpc.MethodKill, rpc.KillRequest{FigaroID: p, Recursive: true, Now: true})
	if !errors.Is(err, store.ErrDeleteSetChanged) {
		t.Fatalf("purging P without C = %v, want ErrDeleteSetChanged", err)
	}
	reaped := map[string]bool{}
	for _, e := range d.backend.Reaped() {
		if e.Root == p {
			reaped[e.ID] = true
		}
	}
	if !reaped[p] || !reaped[c] {
		t.Fatalf("after the refusal the group is %v, want P and C still reaped", reaped)
	}
	for _, id := range []string{p, c} {
		if _, ok := d.backend.Node(id); !ok {
			t.Fatalf("%s was taken by a refused purge", id)
		}
	}
}

// D1 (7): a node whose form carries a tombstone is never woken or prompted.
func TestABuriedNodeIsNeverPrompted(t *testing.T) {
	d, _, c := drawnBoxChild(t)
	if err := d.a.Registry.Hibernate(c); err != nil {
		t.Fatal(err)
	}
	if err := d.backend.SetMeta(c, &store.AriaMeta{TombstonedAtMS: 1}); err != nil {
		t.Fatal(err)
	}
	err := boxOnAria(t, d.dial(t), rpc.MethodQua, c, rpc.QuaRequest{Text: "hi"}, nil)
	if err == nil || !strings.Contains(err.Error(), "tombstone") {
		t.Fatalf("prompting a buried node = %v, want refused", err)
	}
	if d.a.Registry.Get(c) != nil {
		t.Fatal("a buried node was woken")
	}
}

// D1 (4), the other direction: a node drawn under P after P was reaped is not
// in P's group, so a purge of P that would take it is refused and nothing goes.
func TestAPurgeThatWouldTakeMoreThanItsGroupIsRefused(t *testing.T) {
	d, p, c := drawnBoxChild(t)
	if _, err := d.call(asOwner(), rpc.MethodKill, rpc.KillRequest{FigaroID: p, Recursive: true, GraceMS: 3_600_000}); err != nil {
		t.Fatal(err)
	}
	late, err := d.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if err := setTopology(t, d.grantRig, asOwner(), map[string]string{late.FigaroID: p}); err != nil {
		t.Fatalf("drawing a node under reaped P: %v", err)
	}
	_, err = d.call(asOwner(), rpc.MethodKill, rpc.KillRequest{FigaroID: p, Recursive: true, Now: true})
	if !errors.Is(err, store.ErrDeleteSetChanged) || !strings.Contains(err.Error(), "nothing was purged") {
		t.Fatalf("purging P with a late child = %v, want ErrDeleteSetChanged naming nothing purged", err)
	}
	for _, id := range []string{p, c, late.FigaroID} {
		if _, ok := d.backend.Node(id); !ok {
			t.Fatalf("%s was taken by a refused purge", id)
		}
	}
}
