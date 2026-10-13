package turns

import (
	"testing"

	"github.com/jack-work/figaro/api/message"
)

func userMsg(cs ...message.Content) message.Message {
	return message.Message{Role: message.RoleInput, Content: cs}
}

func toolResult(id string) message.Content {
	return message.Content{Type: message.ContentToolResult, ToolCallID: id, Text: "out"}
}

func turnIDs(msgs []message.Message) []uint64 {
	got := make([]uint64, len(msgs))
	for i, m := range msgs {
		got[i] = m.TurnID
	}
	return got
}

func eq(t *testing.T, what string, got, want []uint64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: len %d, want %d (%v vs %v)", what, len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: %v, want %v", what, got, want)
		}
	}
}

func conversation() []message.Message {
	return []message.Message{
		{Role: message.RoleInput},
		userMsg(prose("quick test")),
		asstLT(0, think("hm"), tool("t1", "bash")),
		userMsg(toolResult("t1"), prose("actually, check X")),
		asstLT(0, prose("done")),
		userMsg(prose("next question")),
		asstLT(0, prose("answer")),
	}
}

func TestStampIDs(t *testing.T) {
	msgs := conversation()
	last := StampIDs(msgs)
	eq(t, "ids", turnIDs(msgs), []uint64{0, 1, 1, 1, 1, 2, 2})
	if last != 2 {
		t.Fatalf("last = %d, want 2", last)
	}
}

func TestStampIDsEmpty(t *testing.T) {
	if got := StampIDs(nil); got != 0 {
		t.Fatalf("empty log seed = %d, want 0", got)
	}
}

// Deriving on read must reproduce what minting would have written.
func TestLegacyDerivationMatchesMinting(t *testing.T) {
	minted := conversation()
	StampIDs(minted)

	legacy := conversation()
	StampIDs(legacy)

	eq(t, "legacy vs minted", turnIDs(legacy), turnIDs(minted))
}

func TestMixedLegacyAndStampedAgree(t *testing.T) {
	want := conversation()
	StampIDs(want)

	mixed := conversation()
	for i := 5; i < len(mixed); i++ {
		mixed[i].TurnID = want[i].TurnID
	}
	StampIDs(mixed)

	eq(t, "mixed", turnIDs(mixed), turnIDs(want))
}

// Stamping a prefix yields the same ids as stamping the whole log.
func TestStampIsPureUnderOpenTail(t *testing.T) {
	full := conversation()
	StampIDs(full)

	for n := 0; n <= len(full); n++ {
		prefix := conversation()[:n]
		StampIDs(prefix)
		eq(t, "open tail prefix", turnIDs(prefix), turnIDs(full[:n]))
	}
}

func TestForkChildContinuesParentNumbering(t *testing.T) {
	parent := conversation()
	seed := StampIDs(parent)

	const forkAt = 5
	child := append(conversation()[:forkAt:forkAt],
		userMsg(prose("a different second question")),
		asstLT(0, prose("a different answer")),
	)
	childSeed := StampIDs(child)

	eq(t, "shared prefix", turnIDs(child[:forkAt]), turnIDs(parent[:forkAt]))
	if child[forkAt].TurnID != parent[forkAt].TurnID {
		t.Fatalf("sibling turn ids diverge: child %d, parent %d",
			child[forkAt].TurnID, parent[forkAt].TurnID)
	}
	if childSeed != seed {
		t.Fatalf("child seed = %d, parent seed = %d: siblings must conflict piecewise",
			childSeed, seed)
	}
}

func TestSteeringDoesNotOpenTurn(t *testing.T) {
	steer := userMsg(toolResult("t1"), prose("actually, check X"))
	if Opens(steer) {
		t.Fatal("a tool_result message with prose must not open a turn")
	}
	if !Opens(userMsg(prose("a real question"))) {
		t.Fatal("a pure-prose user message must open a turn")
	}
	if Opens(userMsg(toolResult("t1"))) {
		t.Fatal("a bare tool_result must not open a turn")
	}
	if Opens(asstLT(0, prose("assistant prose"))) {
		t.Fatal("an assistant message must not open a turn")
	}
}

func withLTs(msgs []message.Message) []message.Message {
	for i := range msgs {
		msgs[i].LogicalTime = uint64(i + 1)
	}
	return msgs
}

// first must be the prompt's LT: atMainLT is exclusive of the frozen prefix.
func TestSpan(t *testing.T) {
	msgs := withLTs(conversation())
	for _, c := range []struct{ turn, first, last uint64 }{
		{1, 2, 5},
		{2, 6, 7},
	} {
		first, last, ok := Span(msgs, c.turn)
		if !ok || first != c.first || last != c.last {
			t.Fatalf("turn %d: got (%d,%d,%v), want (%d,%d,true)", c.turn, first, last, ok, c.first, c.last)
		}
	}
}

// A fork boundary that is always a user message can never strand a
// tool_invoke without its result.
func TestSpanAlwaysStartsOnAPrompt(t *testing.T) {
	msgs := withLTs(conversation())
	last := StampIDs(msgs)
	for turn := uint64(1); turn <= last; turn++ {
		first, _, ok := Span(msgs, turn)
		if !ok {
			t.Fatalf("turn %d missing", turn)
		}
		for _, m := range msgs {
			if m.LogicalTime == first && !Opens(m) {
				t.Fatalf("turn %d starts at LT %d which does not open a turn", turn, first)
			}
		}
	}
}

func TestSpanUnknown(t *testing.T) {
	if _, _, ok := Span(withLTs(conversation()), 99); ok {
		t.Fatal("turn 99 should not resolve")
	}
}

func asstLT(lt uint64, cs ...message.Content) message.Message {
	return message.Message{Role: message.RoleOutput, Content: cs, LogicalTime: lt}
}

func think(s string) message.Content { return message.Content{Type: message.ContentThinking, Text: s} }
func prose(s string) message.Content { return message.Content{Type: message.ContentProse, Text: s} }
func tool(id, name string) message.Content {
	return message.Content{Type: message.ContentToolInvoke, ToolCallID: id, ToolName: name}
}
