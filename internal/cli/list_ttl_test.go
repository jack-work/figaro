package cli

// Which rows the listing withholds. The deadline itself is the store's
// (internal/store/ttl.go) and the deletion is the daemon's; this covers the
// client's reading of both.

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

var listTTLNow = time.Date(2026, 9, 3, 23, 0, 0, 0, time.UTC).UnixMilli()

// ttlRow is a dormant conversation with no lifetime, at the given vector, drawn
// beneath parent.
func ttlRow(id, parent string, vec ...int) rpc.FigaroInfoResponse {
	return rpc.FigaroInfoResponse{ID: id, State: "dormant", Parent: parent, Vector: vec}
}

// expiredAt returns the row with a lifetime that ended ago before now.
func expiredAt(f rpc.FigaroInfoResponse, ago time.Duration) rpc.FigaroInfoResponse {
	f.TTLMS = time.Hour.Milliseconds()
	f.ExpiresAt = listTTLNow - ago.Milliseconds()
	return f
}

func ids(figs []rpc.FigaroInfoResponse) []string {
	out := make([]string, 0, len(figs))
	for _, f := range figs {
		out = append(out, f.ID)
	}
	return out
}

func requireIDs(t *testing.T, got []rpc.FigaroInfoResponse, want ...string) {
	t.Helper()
	have := ids(got)
	if len(have) != len(want) {
		t.Fatalf("rows = %v, want %v", have, want)
	}
	for i := range want {
		if have[i] != want[i] {
			t.Fatalf("rows = %v, want %v", have, want)
		}
	}
}

func TestDropExpiredHidesAnExpiredDormantAria(t *testing.T) {
	figs := []rpc.FigaroInfoResponse{
		ttlRow("keeper", "", 0),
		expiredAt(ttlRow("scratch", "", 1), time.Minute),
	}
	kept, n := dropExpired(figs, listTTLNow)
	requireIDs(t, kept, "keeper")
	if n != 1 {
		t.Errorf("hidden = %d, want 1", n)
	}
}

// The same fixture one second BEFORE the deadline: without this the test above
// would pass on a filter that hid every row carrying system.ttl.
func TestDropExpiredKeepsALifetimeStillRunning(t *testing.T) {
	figs := []rpc.FigaroInfoResponse{
		ttlRow("keeper", "", 0),
		expiredAt(ttlRow("scratch", "", 1), -time.Second),
	}
	kept, n := dropExpired(figs, listTTLNow)
	requireIDs(t, kept, "keeper", "scratch")
	if n != 0 {
		t.Errorf("hidden = %d, want 0", n)
	}
}

// A turn in flight and an attending shell are the two holds on an expired row.
func TestDropExpiredKeepsAHeldRow(t *testing.T) {
	live := expiredAt(ttlRow("busy", "", 0), time.Hour)
	live.State = "active"
	attended := expiredAt(ttlRow("mine", "", 1), time.Hour)
	attended.BoundPIDs = []int{4242}
	kept, n := dropExpired([]rpc.FigaroInfoResponse{live, attended}, listTTLNow)
	requireIDs(t, kept, "busy", "mine")
	if n != 0 {
		t.Errorf("hidden = %d, want 0", n)
	}
}

// Residency is not a hold. The daemon keeps an idle agent in memory for
// fifteen minutes by default and will not delete the node until it lets go;
// the listing does not wait for that.
func TestDropExpiredHidesAnIdleResidentAria(t *testing.T) {
	idle := expiredAt(ttlRow("scratch", "", 0), time.Minute)
	idle.State = "idle"
	kept, n := dropExpired([]rpc.FigaroInfoResponse{ttlRow("keeper", "", 1), idle}, listTTLNow)
	requireIDs(t, kept, "keeper")
	if n != 1 {
		t.Errorf("hidden = %d, want 1", n)
	}
}

// An expired trunk with a branch that survives keeps its row: the branch is
// drawn beneath it, and a hidden parent takes the whole subtree off the tree.
func TestDropExpiredKeepsAnExpiredAncestorOfASurvivor(t *testing.T) {
	parent := expiredAt(ttlRow("trunk", "", 0), time.Hour)
	child := ttlRow("branch", "trunk", 0, 0)
	child.State = "idle"
	kept, n := dropExpired([]rpc.FigaroInfoResponse{parent, child}, listTTLNow)
	requireIDs(t, kept, "trunk", "branch")
	if n != 0 {
		t.Errorf("hidden = %d, want 0", n)
	}
}

// A promote moves where a row is drawn, so the reprieve has to follow Present
// rather than Parent: the branch below is drawn under an ancestor its expired
// forking parent no longer holds.
func TestDropExpiredFollowsThePresentedParent(t *testing.T) {
	root := ttlRow("root", "", 0)
	forkedFrom := expiredAt(ttlRow("trunk", "root", 0, 0), time.Hour)
	promoted := ttlRow("branch", "trunk", 0, 0, 0)
	promoted.Present = "root"
	kept, n := dropExpired([]rpc.FigaroInfoResponse{root, forkedFrom, promoted}, listTTLNow)
	requireIDs(t, kept, "root", "branch")
	if n != 1 {
		t.Errorf("hidden = %d, want 1", n)
	}
}

func TestDropExpiredHidesAnExpiredUnboundForm(t *testing.T) {
	form := expiredAt(ttlRow("@note", "root", 0, 0), time.Hour)
	form.State = "form"
	kept, n := dropExpired([]rpc.FigaroInfoResponse{ttlRow("root", "", 0), form}, listTTLNow)
	requireIDs(t, kept, "root")
	if n != 1 {
		t.Errorf("hidden = %d, want 1", n)
	}
}

// Anchors and ordinary arias state no lifetime, and the common listing is
// entirely such rows: it must come back byte for byte, and untouched.
func TestDropExpiredReturnsTheInputWhenNothingExpired(t *testing.T) {
	figs := []rpc.FigaroInfoResponse{ttlRow("a", "", 0), ttlRow("b", "a", 0, 0)}
	kept, n := dropExpired(figs, listTTLNow)
	requireIDs(t, kept, "a", "b")
	if n != 0 {
		t.Errorf("hidden = %d, want 0", n)
	}
}

// A parent link pointing back into its own subtree must not spin the reprieve
// walk. The rows are nonsense; the termination is the assertion.
func TestDropExpiredTerminatesOnACycle(t *testing.T) {
	a := expiredAt(ttlRow("a", "b", 0), time.Hour)
	b := ttlRow("b", "a", 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		dropExpired([]rpc.FigaroInfoResponse{a, b}, listTTLNow)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dropExpired did not terminate on a parent cycle")
	}
}
