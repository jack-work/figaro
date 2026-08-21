package angelus

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"
	fwtree "github.com/jack-work/figaro/internal/store/tree"
	"github.com/jack-work/figaro/internal/uiir"
)

// A FORK BASE IS AN LT AND CAN FALL INSIDE A TURN. The child's log then
// holds that turn's opening records and its own continuation: SAME TURN
// ID, DIFFERENT CONTENT. Reading it out of the ancestor's node serves
// another aria's history as this one's -- the failure a composed cache
// with lineage can make and a flat one could not, and the one no
// single-lineage fixture sees.
//
// The oracle is a fresh composition of the CHILD'S OWN log, so a defect
// in the shared prefix cannot corrupt what it is checked against.
func TestAForkBelowATurnBoundaryServesItsOwnContent(t *testing.T) {
	be, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()

	parent, _, err := be.ForkWith("", 0, message.Patch{Set: map[string]json.RawMessage{
		"aria_id": json.RawMessage(`"p"`)}})
	if err != nil {
		t.Fatal(err)
	}
	plog, err := be.OpenFigIR(parent)
	if err != nil {
		t.Fatal(err)
	}
	ask := func(log store.Log[message.Message], text string) uint64 {
		e, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent(text)}}})
		if err != nil {
			t.Fatal(err)
		}
		return e.LT
	}
	say := func(log store.Log[message.Message], text string) uint64 {
		e, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleOutput, Content: []message.Content{message.TextContent(text)}}})
		if err != nil {
			t.Fatal(err)
		}
		return e.LT
	}

	ask(plog, "turn one")
	say(plog, "parent answers one")
	openedAt := ask(plog, "turn two") // the turn the fork will cut through
	say(plog, "PARENT ANSWERS TWO")
	ask(plog, "turn three")
	say(plog, "parent answers three")

	// The fork lands ON the opening record of turn two: everything after
	// it is the child's, so turn two exists in both lineages with
	// different content.
	_, child, err := be.ForkAt(parent, openedAt)
	if err != nil {
		t.Fatal(err)
	}
	clog, err := be.OpenFigIR(child)
	if err != nil {
		t.Fatal(err)
	}
	say(clog, "CHILD ANSWERS TWO")
	// One more whole turn, so the straddling turn is DISPLACED out of the
	// staging slot and into the cache: the tail is never read through the
	// lineage, so a fixture that leaves it there tests nothing.
	ask(clog, "turn three, the child's own")
	say(clog, "child answers three")

	a := &Angelus{Backend: be, uiProj: uiir.New(nil)}
	cache := aria.NewComposedCache(fwtree.NewBudget(8<<20), a.composeTurns, a.uiLineage)

	srv := aria.NewServer()
	srv.BindCache(child, cache)
	srv.Restore(a.composeTurns(child, 1, ^uint64(0)))

	// The oracle: the child's own log, composed with nothing shared.
	oracle := a.composeTurns(child, 1, ^uint64(0))
	if len(oracle) < 2 {
		t.Fatalf("the fixture built %d turns; it must build the straddling one", len(oracle))
	}
	want := oracle[len(oracle)-2] // the straddling turn, not the tail

	got := srv.Turns()
	if len(got) != len(oracle) {
		t.Fatalf("child sees %d turns, its own log composes %d", len(got), len(oracle))
	}
	straddle := got[len(got)-2]
	if straddle.ID != want.ID {
		t.Fatalf("straddling turn id %d, want %d", straddle.ID, want.ID)
	}
	if len(straddle.Nodes) != len(want.Nodes) {
		t.Fatalf("the straddling turn came back with %d nodes, its own log gives %d",
			len(straddle.Nodes), len(want.Nodes))
	}
	for i := range straddle.Nodes {
		if straddle.Nodes[i].Markdown != want.Nodes[i].Markdown {
			t.Fatalf("the straddling turn served the ANCESTOR's content:\n got %q\nwant %q",
				straddle.Nodes[i].Markdown, want.Nodes[i].Markdown)
		}
	}
}

// A BRACKET THAT ENDS INSIDE A TURN STILL COMPOSES THAT TURN WHOLE. The
// cache cuts runs where its byte target and its gap chunking fall, which
// is nowhere near a turn boundary, and it asks for the span it is
// filling. A turn composed from the records that happen to be below the
// cut is a turn with content missing, cached as if it were complete --
// and a short turn is indistinguishable from a quiet one.
//
// The other end needs no repair and this asserts that too: composition
// drops records until one OPENS a turn, so a bracket that begins
// mid-turn yields nothing for it and the run below owns it.
func TestABracketThatCutsATurnComposesItWhole(t *testing.T) {
	be, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	id, _, err := be.ForkWith("", 0, message.Patch{Set: map[string]json.RawMessage{
		"aria_id": json.RawMessage(`"a"`)}})
	if err != nil {
		t.Fatal(err)
	}
	log, err := be.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	put := func(role message.Role, text string) uint64 {
		e, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: role, Content: []message.Content{message.TextContent(text)}}})
		if err != nil {
			t.Fatal(err)
		}
		return e.LT
	}
	put(message.RoleInput, "one")
	put(message.RoleOutput, "answer one")
	opened := put(message.RoleInput, "two")
	put(message.RoleOutput, "answer two a")
	put(message.RoleOutput, "answer two b")
	last := put(message.RoleOutput, "answer two c")

	a := &Angelus{Backend: be, uiProj: uiir.New(nil)}
	whole := a.composeTurns(id, 1, last)
	if len(whole) != 2 || len(whole[1].Nodes) != 3 {
		t.Fatalf("fixture: %d turns, tail has %d nodes", len(whole), len(whole[1].Nodes))
	}

	// CUT AT THE TURN'S SECOND RECORD: its opener is inside the bracket,
	// two of its three answers are not.
	cut := a.composeTurns(id, 1, opened+1)
	if len(cut) != 2 {
		t.Fatalf("a cut bracket returned %d turns, want 2", len(cut))
	}
	if len(cut[1].Nodes) != len(whole[1].Nodes) {
		t.Fatalf("the cut turn came back with %d of %d nodes: it was cached truncated",
			len(cut[1].Nodes), len(whole[1].Nodes))
	}

	// AND THE OTHER END: a bracket starting after the opener yields the
	// turn to nobody, which is what lets the run below own it whole.
	after := a.composeTurns(id, opened+1, last)
	for _, tn := range after {
		if tn.ID == whole[1].ID {
			t.Fatalf("a bracket that begins mid-turn claimed turn %d: %+v", tn.ID, tn)
		}
	}
}
