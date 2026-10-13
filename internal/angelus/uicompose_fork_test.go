package angelus

import (
	"encoding/json"
	"github.com/jack-work/figaro/api/form"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"
	fwtree "github.com/jack-work/figaro/internal/store/tree"
	"github.com/jack-work/figaro/internal/uiir"
)

// A fork base can fall inside a turn: the child holds that turn's opening
// records and its own continuation, same turn id, different content. The
// oracle is a fresh composition of the child's own log.
func TestAForkBelowATurnBoundaryServesItsOwnContent(t *testing.T) {
	be, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()

	parent, _, err := be.ForkWith("", 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"aria_id": json.RawMessage(`"p"`)}, nil))
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

	// The fork lands on the opening record of turn two.
	_, child, err := be.ForkAt(parent, openedAt)
	if err != nil {
		t.Fatal(err)
	}
	clog, err := be.OpenFigIR(child)
	if err != nil {
		t.Fatal(err)
	}
	say(clog, "CHILD ANSWERS TWO")
	// One more turn displaces the straddling turn out of the staging slot: the
	// tail is never read through the lineage.
	ask(clog, "turn three, the child's own")
	say(clog, "child answers three")

	a := &Angelus{Backend: be, uiProj: uiir.New(nil)}
	cache := aria.NewComposedCache(fwtree.NewBudget(8<<20), a.composeTurns, a.uiLineage)

	srv := aria.NewServer()
	srv.BindCache(child, cache)
	srv.Restore(a.composeTurns(child, 1, ^uint64(0)))

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

// A bracket that ends inside a turn still composes it whole; one that begins
// mid-turn yields nothing for it.
func TestABracketThatCutsATurnComposesItWhole(t *testing.T) {
	be, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	id, _, err := be.ForkWith("", 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"aria_id": json.RawMessage(`"a"`)}, nil))
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

	// Cut at the turn's second record: opener inside, two of three answers outside.
	cut := a.composeTurns(id, 1, opened+1)
	if len(cut) != 2 {
		t.Fatalf("a cut bracket returned %d turns, want 2", len(cut))
	}
	if len(cut[1].Nodes) != len(whole[1].Nodes) {
		t.Fatalf("the cut turn came back with %d of %d nodes: it was cached truncated",
			len(cut[1].Nodes), len(whole[1].Nodes))
	}

	// A bracket starting after the opener yields the turn to nobody.
	after := a.composeTurns(id, opened+1, last)
	for _, tn := range after {
		if tn.ID == whole[1].ID {
			t.Fatalf("a bracket that begins mid-turn claimed turn %d: %+v", tn.ID, tn)
		}
	}
}
