package cli

// THE ADDRESS GRAMMAR, ASSERTED.
//
// `state` is the reserved identity segment and it is IMPLIED, so `<id>` and
// `<id>/state` must be the same address in every respect. If those two ever
// diverge the shorthand becomes a lie, and every doc that says "the same in
// every respect" becomes wrong at once.

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func TestTheIdentitySegmentIsImplied(t *testing.T) {
	for _, spec := range []string{"abc123", "@form9", "abc123:12"} {
		bareHost, bareIntrinsic := splitIntrinsic(spec)
		explicitHost, explicitIntrinsic := splitIntrinsic(spec + "/state")
		if bareHost != explicitHost || bareIntrinsic != explicitIntrinsic {
			t.Fatalf("%q parsed as (%q,%q) but %q/state parsed as (%q,%q). The identity "+
				"segment is documented as implied: if the two spellings differ, the "+
				"shorthand is a lie", spec, bareHost, bareIntrinsic,
				spec, explicitHost, explicitIntrinsic)
		}
		if bareIntrinsic != "" {
			t.Fatalf("%q named a intrinsic %q; the host's own form is spelled \"\" on the "+
				"wire, which is what makes FormDelta backward compatible", spec, bareIntrinsic)
		}
	}
}

func TestIntrinsicAddressRoundTrips(t *testing.T) {
	for _, name := range append(intrinsicNames(), "", intrinsicState) {
		addr := formAddress("abc123", name)
		host, got := splitIntrinsic(addr)
		if host != "abc123" {
			t.Fatalf("%q lost its host: %q", addr, host)
		}
		want := name
		if want == intrinsicState {
			want = ""
		}
		if got != want {
			t.Fatalf("%q round-tripped to %q, wanted %q", addr, got, want)
		}
	}
}

// An unknown segment must be REFUSED, not silently treated as the board. A
// reader who types `<id>/qeueu` and is shown a board will believe it is a
// queue, and nothing on screen will contradict them.
func TestUnknownIntrinsicIsNotSilentlyTheBoard(t *testing.T) {
	if knownIntrinsic("qeueu") {
		t.Fatal("a misspelled intrinsic was accepted; the reader would be shown the " +
			"board and told nothing")
	}
	for _, name := range append(intrinsicNames(), "", intrinsicState) {
		if !knownIntrinsic(name) {
			t.Fatalf("%q is published but not recognised by the address parser", name)
		}
	}
}

// The client's projection of `<aria>/queue` must survive the shape the daemon
// actually publishes: flat dotted keys with `order` carrying FIFO.
func TestReadQueueProjectsTheDaemonsShape(t *testing.T) {
	snap := buildSnapshot(t, map[string]any{
		"epoch":         "e1",
		"len":           3,
		"order":         []uint64{7, 8},
		"items.7.text":  "first",
		"items.7.state": string(rpc.QueueStateCommitting),
		"items.8.text":  "second",
		"items.8.state": string(rpc.QueueStateQueued),
		"items.9.state": string(rpc.QueueStateCommitted),
		"items.9.turn":  42,
		"items.9.text":  "done one",
	})
	rows := readQueue(snap)
	if len(rows) != 3 {
		t.Fatalf("projected %d rows, wanted 3: %+v", len(rows), rows)
	}
	// order first, then whatever it does not name -- a departed message is
	// still in the form (that is the point) and must not be dropped merely for
	// being off the FIFO.
	if rows[0].ID != 7 || rows[1].ID != 8 || rows[2].ID != 9 {
		t.Fatalf("order was not honoured: %+v", rows)
	}
	if rows[0].State != rpc.QueueStateCommitting {
		t.Fatalf("row 7 is %q, wanted committing", rows[0].State)
	}
	if rows[2].Turn != 42 {
		t.Fatalf("a committed row lost the turn it became: %+v", rows[2])
	}
	if !rows[0].live() || !rows[1].live() || !rows[2].live() {
		t.Fatal("a queued, committing or committed row is still happening and belongs " +
			"in the drawer")
	}
	for _, st := range []rpc.QueueState{rpc.QueueStateDropped, rpc.QueueStateDrained, rpc.QueueStateMerged} {
		if (queueRow{State: st}).live() {
			t.Fatalf("state %q is history and must not hold a row in the drawer", st)
		}
	}
}

// The row's state is part of its identity for repaint purposes. Comparing only
// id and text made a message moving from queued to committing look like no
// change, so the gutter never repainted and the travel the feature exists to
// show was invisible.
func TestSameQueueNoticesAStateChange(t *testing.T) {
	a := []queuedItem{{id: 1, text: "x", state: rpc.QueueStateQueued}}
	b := []queuedItem{{id: 1, text: "x", state: rpc.QueueStateCommitting}}
	if sameQueue(a, b) {
		t.Fatal("a message moving from queued to committing compared EQUAL, so nothing " +
			"would repaint and the reader would never see it travel")
	}
	if a[0].mark() == b[0].mark() {
		t.Fatal("queued and committing wear the same gutter mark, so the state change " +
			"is invisible even when it does repaint")
	}
}

func buildSnapshot(t *testing.T, kv map[string]any) form.Snapshot {
	t.Helper()
	snap := form.Snapshot{}
	for k, v := range kv {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %s: %v", k, err)
		}
		snap = snap.SetPath(k, b)
	}
	return snap
}
