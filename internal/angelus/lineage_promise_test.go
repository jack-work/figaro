package angelus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

// Every turn below the divergence must be the same bytes in both arias; a
// claim one turn too broad looks like a conversation, not a bug.

func composedFingerprint(t *testing.T, a *Angelus, node string, turn uint64) (string, bool) {
	t.Helper()
	for _, got := range a.composeTurns(node, 1, ^uint64(0)) {
		if got.ID != turn {
			continue
		}
		b, err := json.Marshal(got.Nodes)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		return hex.EncodeToString(sum[:8]), true
	}
	return "", false
}

func TestLineage_EveryTurnItCallsSharedIsByteIdentical(t *testing.T) {
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
	put := func(log store.Log[message.Message], m message.Message) uint64 {
		e, err := log.Append(store.Entry[message.Message]{Payload: m})
		if err != nil {
			t.Fatal(err)
		}
		return e.LT
	}

	for i := uint64(1); i <= 3; i++ {
		put(plog, message.Message{Role: message.RoleInput, TurnID: i,
			Content: []message.Content{message.TextContent("question")}})
		put(plog, message.Message{Role: message.RoleOutput, TurnID: i,
			Content: []message.Content{message.TextContent("answer")}})
	}
	// Turn 4 is in flight: a tool call with nothing back yet.
	put(plog, message.Message{Role: message.RoleInput, TurnID: 4,
		Content: []message.Content{message.TextContent("run the thing")}})
	last := put(plog, message.Message{Role: message.RoleOutput, TurnID: 4, Content: []message.Content{
		{Type: message.ContentToolInvoke, ToolCallID: "call-1", ToolName: "bash"},
	}})

	_, child, err := be.ForkAt(parent, last+1)
	if err != nil {
		t.Fatal(err)
	}
	clog, err := be.OpenFigIR(child)
	if err != nil {
		t.Fatal(err)
	}
	// The child repairs what it inherited: the call never returns to it.
	put(clog, message.Message{Role: message.RoleInput, TurnID: 4, Content: []message.Content{
		{Type: message.ContentToolResult, ToolCallID: "call-1", Text: "process died mid-turn", IsError: true},
	}})
	// The claim is made while the parent is still working.
	a := &Angelus{Backend: be, uiProj: uiir.New(nil)}
	h := &handlers{angelus: a}
	params, _ := json.Marshal(rpc.LineageRequest{FigaroID: child, Against: parent})
	out, err := h.lineage(inProcess(t.Context()), params)
	if err != nil {
		t.Fatal(err)
	}
	d := out.(rpc.LineageResponse).Divergence
	if d == 0 {
		t.Fatal("the read found no common ancestry at all")
	}

	// Through a fresh handle: the fork sealed the node the old one was opened on.
	pcont, err := be.OpenFigIR(parent)
	if err != nil {
		t.Fatal(err)
	}
	put(pcont, message.Message{Role: message.RoleInput, TurnID: 4, Content: []message.Content{
		{Type: message.ContentToolResult, ToolCallID: "call-1", Text: "TOOL_DONE"},
	}})
	put(pcont, message.Message{Role: message.RoleOutput, TurnID: 4,
		Content: []message.Content{message.TextContent("all done")}})

	// Below the divergence both sides must match; at it they must differ, or the
	// fixture proves nothing.
	for turn := uint64(1); turn < d; turn++ {
		pf, pok := composedFingerprint(t, a, parent, turn)
		cf, cok := composedFingerprint(t, a, child, turn)
		if !pok || !cok {
			t.Fatalf("turn %d is called shared but is missing from %s", turn,
				map[bool]string{true: "the child", false: "the parent"}[pok])
		}
		if pf != cf {
			t.Fatalf("turn %d is called shared and is not: parent %s, child %s", turn, pf, cf)
		}
	}
	pf, _ := composedFingerprint(t, a, parent, d)
	cf, _ := composedFingerprint(t, a, child, d)
	if pf == cf {
		t.Fatalf("the fixture's turn %d is identical in both arias, so it proves nothing", d)
	}
}
