package formdelta

import (
	"encoding/json"
	"github.com/jack-work/figaro/api/form"
	"reflect"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"
)

func backend(t *testing.T) *store.XwalBackend {
	t.Helper()
	be, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { be.Close() })
	return be
}

func patchOf(kv map[string]string) message.Patch {
	set := map[string]json.RawMessage{}
	for k, v := range kv {
		set[k] = json.RawMessage(v)
	}
	return form.Build(form.Snapshot{}, set, nil)
}

func appendRecord(t *testing.T, log store.Log[message.Message], role message.Role) store.Entry[message.Message] {
	t.Helper()
	e, err := log.Append(store.Entry[message.Message]{Payload: message.Message{Role: role}})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func waitFold(t *testing.T, lib *store.Libretto, v uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if lib.At() >= v {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("libretto did not fold source version %d (at %d)", v, lib.At())
}

func TestBoundBoardDeltasBracketTheWindows(t *testing.T) {
	be := backend(t)
	aria, _, err := be.ForkWith("", 0, patchOf(map[string]string{"aria_id": `"a1"`}))
	if err != nil {
		t.Fatal(err)
	}
	log, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	e1 := appendRecord(t, log, message.RoleInput)
	if _, err := be.ApplyForm(aria, patchOf(map[string]string{"phase": `"canary"`})); err != nil {
		t.Fatal(err)
	}
	e2 := appendRecord(t, log, message.RoleOutput)

	entries := log.Read()
	deltas := PerRecord(be, aria, entries)

	// The store mints a genesis record at fork, before e1.
	var first uint64
	for _, e := range entries {
		if e.Payload.Role != message.RoleGenesis {
			first = e.LT
			break
		}
	}
	if d, ok := deltas[first][aria+".aria_id"]; !ok || d.Event != livedoc.FormSet || d.Kind != livedoc.FormBound {
		t.Fatalf("the first record should carry the birth board: %+v", deltas[first])
	}
	d, ok := deltas[e2.LT][aria+".phase"]
	if !ok || string(d.Value) != `"canary"` || d.Kind != livedoc.FormBound || d.Form != aria {
		t.Fatalf("second record should carry phase=canary: %+v", deltas[e2.LT])
	}
	if _, leaked := deltas[e2.LT][aria+".aria_id"]; leaked {
		t.Fatal("the birth patch rendered twice: the windows overlap")
	}
	if _, leaked := deltas[e1.LT][aria+".phase"]; e1.LT != e2.LT && leaked {
		t.Fatal("a later window leaked backwards")
	}
}

func TestStudiedFormDeltas(t *testing.T) {
	be := backend(t)
	src, _, err := be.CreateForm("", patchOf(map[string]string{"brief": `"the studied thing"`}))
	if err != nil {
		t.Fatal(err)
	}
	aria, _, err := be.ForkWith("", 0, patchOf(map[string]string{"aria_id": `"a1"`}))
	if err != nil {
		t.Fatal(err)
	}
	studiesDecl, err := be.StudyForm(aria, src)
	studies := studiesDecl.Studies
	if err != nil {
		t.Fatal(err)
	}
	be.SetObservedForms(aria, studies)
	lib, err := be.Libretto(src)
	if err != nil {
		t.Fatal(err)
	}
	log, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	appendRecord(t, log, message.RoleInput)

	v, err := be.ApplyForm(src, form.Build(form.Snapshot{}, map[string]json.RawMessage{"status": json.RawMessage(`"merged"`)}, []string{"brief"}))
	if err != nil {
		t.Fatal(err)
	}
	waitFold(t, lib, v)
	e2 := appendRecord(t, log, message.RoleOutput)

	deltas := PerRecord(be, aria, log.Read())
	got := deltas[e2.LT]
	if d, ok := got[src+".status"]; !ok || string(d.Value) != `"merged"` || d.Kind != livedoc.FormStudied {
		t.Fatalf("status=merged should land on the closing record: %+v", got)
	}
	if d, ok := got[src+".brief"]; !ok || d.Event != livedoc.FormRemoved || d.Value != nil {
		t.Fatalf("brief should be REMOVED, valueless: %+v", got)
	}
	for k := range got {
		if k == src+".system.libretto.at" || k == src+".system.libretto.refs" {
			t.Fatalf("machinery leaked: %s", k)
		}
	}
}

func TestRoleKindIsDecidedServerSide(t *testing.T) {
	be := backend(t)
	role, _, err := be.CreateForm("", patchOf(map[string]string{"target-aria": `"a1"`}))
	if err != nil {
		t.Fatal(err)
	}
	aria, _, err := be.ForkWith("", 0, patchOf(map[string]string{"aria_id": `"a1"`}))
	if err != nil {
		t.Fatal(err)
	}
	studiesDecl, err := be.StudyForm(aria, role)
	studies := studiesDecl.Studies
	if err != nil {
		t.Fatal(err)
	}
	be.SetObservedForms(aria, studies)
	lib, err := be.Libretto(role)
	if err != nil {
		t.Fatal(err)
	}
	log, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	appendRecord(t, log, message.RoleInput)
	v, err := be.ApplyForm(role, patchOf(map[string]string{"target-aria": `"a2"`}))
	if err != nil {
		t.Fatal(err)
	}
	waitFold(t, lib, v)
	e2 := appendRecord(t, log, message.RoleOutput)

	deltas := PerRecord(be, aria, log.Read())
	d, ok := deltas[e2.LT][role+".target-aria"]
	if !ok || d.Kind != livedoc.FormRole || string(d.Value) != `"a2"` {
		t.Fatalf("the recast should render as a ROLE delta: %+v", deltas[e2.LT])
	}
}

func TestDeletedSourceRendersAsDeleted(t *testing.T) {
	be := backend(t)
	src, _, err := be.CreateForm("", patchOf(map[string]string{"brief": `"doomed"`}))
	if err != nil {
		t.Fatal(err)
	}
	aria, _, err := be.ForkWith("", 0, patchOf(map[string]string{"aria_id": `"a1"`}))
	if err != nil {
		t.Fatal(err)
	}
	studiesDecl, err := be.StudyForm(aria, src)
	studies := studiesDecl.Studies
	if err != nil {
		t.Fatal(err)
	}
	be.SetObservedForms(aria, studies)
	lib, err := be.Libretto(src)
	if err != nil {
		t.Fatal(err)
	}
	log, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	appendRecord(t, log, message.RoleInput)
	if err := be.Remove(src, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if raw, ok := lib.State().Get(store.KeyLibrettoAlive); ok && string(raw) == "false" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the death never reached the copy")
		}
		time.Sleep(2 * time.Millisecond)
	}
	e2 := appendRecord(t, log, message.RoleOutput)

	deltas := PerRecord(be, aria, log.Read())
	d, ok := deltas[e2.LT][src]
	if !ok || d.Event != livedoc.FormDeleted {
		t.Fatalf("the death should arrive as FormDeleted on the form id: %+v", deltas[e2.LT])
	}
}

// Deltas are a pure function of durable stamps and logs, never the provider
// cache.
func TestAssemblyIsDeterministic(t *testing.T) {
	be := backend(t)
	src, _, err := be.CreateForm("", patchOf(map[string]string{"brief": `"b"`}))
	if err != nil {
		t.Fatal(err)
	}
	aria, _, err := be.ForkWith("", 0, patchOf(map[string]string{"aria_id": `"a1"`}))
	if err != nil {
		t.Fatal(err)
	}
	studiesDecl, err := be.StudyForm(aria, src)
	studies := studiesDecl.Studies
	if err != nil {
		t.Fatal(err)
	}
	be.SetObservedForms(aria, studies)
	lib, err := be.Libretto(src)
	if err != nil {
		t.Fatal(err)
	}
	log, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	appendRecord(t, log, message.RoleInput)
	v, err := be.ApplyForm(src, patchOf(map[string]string{"phase": `"ga"`}))
	if err != nil {
		t.Fatal(err)
	}
	waitFold(t, lib, v)
	appendRecord(t, log, message.RoleOutput)

	entries := log.Read()
	first := PerRecord(be, aria, entries)
	second := PerRecord(be, aria, entries)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("two renders disagree:\n%+v\n%+v", first, second)
	}
	if len(first) == 0 {
		t.Fatal("the fixture rendered nothing at all: the test is not testing")
	}
}

func TestAttachPlacesDeltasDeliberately(t *testing.T) {
	turns := []aria.Turn{{
		ID:  1,
		LTs: []uint64{10, 14},
		Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Src: []livedoc.Src{{LT: 11, Block: 0}}},
			{Type: livedoc.NodeTool, Src: []livedoc.Src{{LT: 12, Block: 1}, {LT: 13, Block: 0}}},
		},
	}}
	d := func(k string) map[string]livedoc.FormDelta {
		return map[string]livedoc.FormDelta{k: {Kind: livedoc.FormBound, Event: livedoc.FormSet, Form: "a1"}}
	}
	Attach(turns, map[uint64]map[string]livedoc.FormDelta{
		10: d("a1.inquiry-window"),
		11: d("a1.on-prose"),
		13: d("a1.on-tool-result"),
		14: d("a1.unclaimed"),
	})
	if _, ok := turns[0].FormDeltas["a1.inquiry-window"]; !ok {
		t.Fatalf("the inquiry window should land on the TURN: %+v", turns[0].FormDeltas)
	}
	if _, ok := turns[0].Nodes[0].FormDeltas["a1.on-prose"]; !ok {
		t.Fatalf("the prose record's delta should land on the prose node: %+v", turns[0].Nodes[0].FormDeltas)
	}
	if _, ok := turns[0].Nodes[1].FormDeltas["a1.on-tool-result"]; !ok {
		t.Fatalf("the tool result's delta should land on the tool node: %+v", turns[0].Nodes[1].FormDeltas)
	}
	if _, ok := turns[0].FormDeltas["a1.unclaimed"]; ok {
		t.Fatalf("a trailing seam must not attach to the turn before it: %+v", turns[0].FormDeltas)
	}
	if len(turns[0].Nodes[0].FormDeltas) != 1 || len(turns[0].Nodes[1].FormDeltas) != 1 {
		t.Fatal("a delta rendered on more than one unit")
	}
}

func TestCollapsedTransitionIsChronological(t *testing.T) {
	rec := func(prev, val string) map[string]livedoc.FormDelta {
		return map[string]livedoc.FormDelta{"a1.mantra": {
			Kind: livedoc.FormBound, Event: livedoc.FormSet, Form: "a1",
			Prev: json.RawMessage(prev), Value: json.RawMessage(val),
		}}
	}
	window := func() map[uint64]map[string]livedoc.FormDelta {
		return map[uint64]map[string]livedoc.FormDelta{
			11: rec(`"seed"`, `"old"`),
			12: rec(`"old"`, `"new"`),
		}
	}
	check := func(where string, got livedoc.FormDelta) {
		t.Helper()
		if string(got.Value) != `"new"` || string(got.Prev) != `"seed"` {
			t.Fatalf("%s folded %s -> %s; want \"seed\" -> \"new\"", where, got.Prev, got.Value)
		}
	}
	for i := 0; i < 200; i++ {
		live := aria.Turn{ID: 1, LTs: []uint64{10, 12}}
		AttachOne(&live, window())
		check("AttachOne", live.FormDeltas["a1.mantra"])

		durable := []aria.Turn{{
			ID:    1,
			LTs:   []uint64{10, 12},
			Nodes: []livedoc.Node{{Type: livedoc.NodeTool, Src: []livedoc.Src{{LT: 11}, {LT: 12}}}},
		}}
		Attach(durable, window())
		check("Attach", durable[0].Nodes[0].FormDeltas["a1.mantra"])
	}
}

// A fork's birth record projects no node and trails every node of the
// parent's last turn; it belongs to the turn after it.
func TestSeamDeltasAttachToTheTurnTheyOpen(t *testing.T) {
	turns := []aria.Turn{
		{
			ID:    1,
			LTs:   []uint64{4, 6},
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Src: []livedoc.Src{{LT: 5, Block: 0}}}},
		},
		{
			ID:    2,
			LTs:   []uint64{7, 8},
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Src: []livedoc.Src{{LT: 8, Block: 0}}}},
		},
	}
	d := func(k string) map[string]livedoc.FormDelta {
		return map[string]livedoc.FormDelta{k: {Kind: livedoc.FormBound, Event: livedoc.FormSet, Form: "a1"}}
	}
	Attach(turns, map[uint64]map[string]livedoc.FormDelta{
		4: d("a1.mantra"),
		6: d("a1.system.forked_from"),
	})
	if _, ok := turns[0].FormDeltas["a1.mantra"]; !ok {
		t.Fatalf("the inquiry window should stay on its own turn: %+v", turns[0].FormDeltas)
	}
	if _, ok := turns[0].FormDeltas["a1.system.forked_from"]; ok {
		t.Fatalf("the fork seam drew on the turn before it: %+v", turns[0].FormDeltas)
	}
	if _, ok := turns[1].FormDeltas["a1.system.forked_from"]; !ok {
		t.Fatalf("the fork seam should open turn 2: %+v", turns[1].FormDeltas)
	}
}

func TestSeamWithNoFollowingTurnWaits(t *testing.T) {
	turns := []aria.Turn{{
		ID:    1,
		LTs:   []uint64{4, 6},
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Src: []livedoc.Src{{LT: 5, Block: 0}}}},
	}}
	Attach(turns, map[uint64]map[string]livedoc.FormDelta{
		6: {"a1.system.forked_from": {Kind: livedoc.FormBound, Event: livedoc.FormSet, Form: "a1"}},
	})
	if _, ok := turns[0].FormDeltas["a1.system.forked_from"]; ok {
		t.Fatalf("a trailing seam must wait for the turn it opens: %+v", turns[0].FormDeltas)
	}
}
