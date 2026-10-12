package angelus

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

// A HEAD FORK SHARES EVERYTHING BUT THE LAST TURN.
//
// The branch is cut at the tail, so it owns no turn the parent has and a
// reader hopping to it keeps the window. The one turn held back is the turn
// the parent's log STOPS INSIDE, because that is the turn the two can still
// disagree about: a fork taken while a tool is in flight leaves the child to
// repair the hanging call with an error result, while the parent goes on to
// complete the same call successfully. Same turn id, different content.
//
// It costs one turn of re-read, and it is right whether or not that turn was
// still running, which is what a log cannot tell us. Raised by 27068b2c from a
// tool-gated fork in a real pane; the read floor fixed the missing data, this
// fixes the claim.
func TestLineage_AHeadForkSharesAllButTheLastTurn(t *testing.T) {
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
	plog, _ := be.OpenFigIR(parent)
	var last uint64
	for i := uint64(1); i <= 5; i++ {
		e, _ := plog.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, TurnID: i, Content: []message.Content{message.TextContent("q")}}})
		_ = e
		e2, _ := plog.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleOutput, TurnID: i, Content: []message.Content{message.TextContent("a")}}})
		last = e2.LT
	}
	_, branch, err := be.ForkAt(parent, last+1)
	if err != nil {
		t.Fatal(err)
	}
	a := &Angelus{Backend: be, uiProj: uiir.New(nil)}
	h := &handlers{angelus: a}
	params, _ := json.Marshal(rpc.LineageRequest{FigaroID: branch, Against: parent})
	out, err := h.lineage(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(rpc.LineageResponse)
	if got.Divergence != 5 {
		t.Fatalf("divergence %d, want 5: everything below the turn the parent's log stops inside is shared, and that turn is not", got.Divergence)
	}
}

// AND A RECORDED ANSWER THAT CAN STILL CHANGE IS NOT TRUSTED FOREVER. "Past
// the end of the log" is not a coordinate, it is a statement about a log that
// grows: the parent writes the record that coordinate names a second later and
// the true answer moves. A head fork records the conservative answer at birth
// (the turn the parent's log stops inside) and marks it inexact; the first
// lineage read after the parent writes AT the base must see the new turn, and
// must record it, so the next one reads no log.
//
// This used to be a test of an in-process memo that declined to cache the
// unsealed answer. The answer is data on disk now; the property is the same.
func TestLineage_AnUnsealedBaseIsRefinedOnceTheParentWritesThere(t *testing.T) {
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
	plog, _ := be.OpenFigIR(parent)
	for i := uint64(1); i <= 3; i++ {
		if _, err := plog.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, TurnID: i, Content: []message.Content{message.TextContent("q")}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := plog.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleOutput, TurnID: i, Content: []message.Content{message.TextContent("a")}}}); err != nil {
			t.Fatal(err)
		}
	}
	// A head fork through the birth verb, which records its base turn.
	branch, _, err := be.ForkWith(parent, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"aria_id": json.RawMessage(`"b"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	link := lastLink(t, be, branch)
	if !link.BaseTurnKnown || link.BaseTurnExact || link.BaseTurn != 3 {
		t.Fatalf("a head fork recorded %+v, want turn 3, known, inexact", link)
	}

	a := &Angelus{Backend: be, uiProj: uiir.New(nil)}
	h := &handlers{angelus: a}
	divergence := func() uint64 {
		t.Helper()
		params, _ := json.Marshal(rpc.LineageRequest{FigaroID: branch, Against: parent})
		out, err := h.lineage(t.Context(), params)
		if err != nil {
			t.Fatal(err)
		}
		return out.(rpc.LineageResponse).Divergence
	}
	if got := divergence(); got != 3 {
		t.Fatalf("a base past the end answers %d, want the turn the log stops inside", got)
	}
	// The parent writes the record that coordinate names. The answer is now a
	// fact about a real record, and it must be the new one.
	if _, err := plog.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, TurnID: 4, Content: []message.Content{message.TextContent("q4")}}}); err != nil {
		t.Fatal(err)
	}
	if got := divergence(); got != 4 {
		t.Fatalf("after the parent wrote at that coordinate the answer is %d, want 4: a recorded claim outlived the log it was true of", got)
	}
	if link := lastLink(t, be, branch); !link.BaseTurnExact || link.BaseTurn != 4 {
		t.Fatalf("the refined answer was not recorded: %+v", link)
	}
}

func lastLink(t *testing.T, be *store.XwalBackend, id string) store.Link {
	t.Helper()
	links := be.LineageLinks(id)
	if len(links) == 0 {
		t.Fatalf("no lineage for %s", id)
	}
	return links[len(links)-1]
}
