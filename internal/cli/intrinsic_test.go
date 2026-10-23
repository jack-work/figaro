package cli

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

func TestUnknownIntrinsicIsNotSilentlyTheHostForm(t *testing.T) {
	if knownIntrinsic("qeueu") {
		t.Fatal("a misspelled intrinsic was accepted; the reader would be shown the " +
			"host's own form and told nothing")
	}
	for _, name := range append(intrinsicNames(), "", intrinsicState) {
		if !knownIntrinsic(name) {
			t.Fatalf("%q is published but not recognised by the address parser", name)
		}
	}
}

// The daemon publishes flat dotted keys with `order` carrying FIFO.
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
	// order first, then whatever it does not name.
	if rows[0].ID != 7 || rows[1].ID != 8 || rows[2].ID != 9 {
		t.Fatalf("order was not honoured: %+v", rows)
	}
	if rows[0].State != rpc.QueueStateCommitting {
		t.Fatalf("row 7 is %q, wanted committing", rows[0].State)
	}
	if rows[2].Turn != 42 {
		t.Fatalf("a committed row lost the turn it became: %+v", rows[2])
	}
	// The projection carries the committed row; the drawer does not draw it.
	if !rows[0].live() || !rows[1].live() {
		t.Fatal("a queued or committing row is not yet in the conversation and belongs " +
			"in the drawer")
	}
	if rows[2].live() {
		t.Fatal("a committed row is drawn in the drawer. It is already in the " +
			"transcript above it, so this is a duplicate, not reassurance")
	}
}

// The row's state is part of its identity for repaint purposes.
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

// Every queue mutation is a compare-and-set against the epoch its rows came from.
func TestQueueProjectionCarriesTheEpoch(t *testing.T) {
	snap := buildSnapshot(t, map[string]any{
		"epoch":         "gen-7",
		"order":         []uint64{1},
		"items.1.text":  "x",
		"items.1.state": string(rpc.QueueStateQueued),
	})
	if got, ok := lookupString(snap, "epoch"); !ok || got != "gen-7" {
		t.Fatalf("the queue projection does not expose the epoch (%q, ok=%v). A client "+
			"that can render a row it cannot delete is worse than one that renders "+
			"nothing", got, ok)
	}
	if rows := readQueue(snap); len(rows) != 1 {
		t.Fatalf("projected %d rows beside the epoch, wanted 1", len(rows))
	}
}

func TestCommittedRowsLeaveTheDrawer(t *testing.T) {
	for _, st := range []rpc.QueueState{
		rpc.QueueStateCommitted, rpc.QueueStateMerged,
		rpc.QueueStateDropped, rpc.QueueStateDrained,
	} {
		if (queueRow{State: st}).live() {
			t.Errorf("a %q row is still drawn in the drawer. It is already in the "+
				"conversation (or gone from it), and the drawer is for what has not "+
				"been asked yet", st)
		}
	}
	for _, st := range []rpc.QueueState{rpc.QueueStateQueued, rpc.QueueStateCommitting} {
		if !(queueRow{State: st}).live() {
			t.Errorf("a %q row is NOT drawn, so a message the daemon is holding is "+
				"visible nowhere at all", st)
		}
	}
	for _, st := range []rpc.QueueState{rpc.QueueStateQueued, rpc.QueueStateCommitting} {
		if m := (queuedItem{state: st}).mark(); m == "✓" {
			t.Errorf("state %q wears a checkmark; the drawer no longer has a completed "+
				"state to mark", st)
		}
	}
}

// "<node>/<id>" names an aria on a node; it must not be read as an intrinsic,
// while a misspelled intrinsic still is one.
func TestANodeQualifiedIDIsAHostNotAnIntrinsic(t *testing.T) {
	node := "aca6cfd2-f5f9-494f-9810-6009bdfcd428"
	cases := []struct{ spec, host, intrinsic string }{
		{node + "/00386cbe", node + "/00386cbe", ""},
		{node + "/@abcd", node + "/@abcd", ""},
		{node + "/00386cbe:12", node + "/00386cbe:12", ""},
		{node + "/00386cbe/runtime", node + "/00386cbe", intrinsicRuntime},
		{node + "/00386cbe/state", node + "/00386cbe", ""},
		{"00386cbe/qeueu", "00386cbe", "qeueu"},
	}
	for _, c := range cases {
		host, in := splitIntrinsic(c.spec)
		if host != c.host || in != c.intrinsic {
			t.Errorf("splitIntrinsic(%q) = (%q, %q), want (%q, %q)", c.spec, host, in, c.host, c.intrinsic)
		}
	}
}
