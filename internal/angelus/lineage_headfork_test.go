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

// A head fork shares everything but the turn the parent's log stops inside: a
// fork taken mid-tool leaves the child repairing a call the parent completes.
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
	out, err := h.lineage(inProcess(t.Context()), params)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(rpc.LineageResponse)
	if got.Divergence != 5 {
		t.Fatalf("divergence %d, want 5: everything below the turn the parent's log stops inside is shared, and that turn is not", got.Divergence)
	}
}

// A base recorded past the end is inexact; the first read after the parent
// writes at the base must see and record the new turn.
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
		out, err := h.lineage(inProcess(t.Context()), params)
		if err != nil {
			t.Fatal(err)
		}
		return out.(rpc.LineageResponse).Divergence
	}
	if got := divergence(); got != 3 {
		t.Fatalf("a base past the end answers %d, want the turn the log stops inside", got)
	}
	// The parent writes at the base; the answer must be the new one.
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
