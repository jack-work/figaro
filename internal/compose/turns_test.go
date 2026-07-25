package compose

import (
	"testing"

	"github.com/jack-work/figaro/internal/message"
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

// conversation is the canonical shape: a boot tic that belongs to no turn,
// then two turns, the first of which runs a tool round and carries a steering
// interjection riding on the tool_result message.
func conversation() []message.Message {
	return []message.Message{
		{Role: message.RoleInput},                             // boot / state-only: no turn
		userMsg(prose("quick test")),                          // opens turn 1
		asstLT(0, think("hm"), tool("t1", "bash")),            // turn 1
		userMsg(toolResult("t1"), prose("actually, check X")), // turn 1 — steering, not a new turn
		asstLT(0, prose("done")),                              // turn 1
		userMsg(prose("next question")),                       // opens turn 2
		asstLT(0, prose("answer")),                            // turn 2
	}
}

func TestStampTurnIDs(t *testing.T) {
	msgs := conversation()
	last := StampTurnIDs(msgs)
	eq(t, "ids", turnIDs(msgs), []uint64{0, 1, 1, 1, 1, 2, 2})
	if last != 2 {
		t.Fatalf("last = %d, want 2", last)
	}
}

func TestStampTurnIDsEmpty(t *testing.T) {
	if got := StampTurnIDs(nil); got != 0 {
		t.Fatalf("empty log seed = %d, want 0", got)
	}
}

// A legacy aria carries no turn_id at all. Deriving on read must reproduce
// exactly what minting would have written, or `send <trunk>:<turn>` would name
// different exchanges depending on when the aria was created.
func TestLegacyDerivationMatchesMinting(t *testing.T) {
	minted := conversation()
	StampTurnIDs(minted) // as if written by the agent, ids now durable

	legacy := conversation() // same log, never stamped
	StampTurnIDs(legacy)

	eq(t, "legacy vs minted", turnIDs(legacy), turnIDs(minted))
}

// A log that is half legacy and half stamped must agree end to end: the
// stored ids resynchronise the counter rather than fighting it.
func TestMixedLegacyAndStampedAgree(t *testing.T) {
	want := conversation()
	StampTurnIDs(want)

	mixed := conversation()
	for i := 5; i < len(mixed); i++ { // stamp only the tail, as an upgrade would
		mixed[i].TurnID = want[i].TurnID
	}
	StampTurnIDs(mixed)

	eq(t, "mixed", turnIDs(mixed), turnIDs(want))
}

// Purity: the ids a message gets must not depend on whether the tail is still
// open. Stamping a prefix yields the same ids as stamping the whole log and
// truncating — which is what lets a live turn be addressed before it seals.
func TestStampIsPureUnderOpenTail(t *testing.T) {
	full := conversation()
	StampTurnIDs(full)

	for n := 0; n <= len(full); n++ {
		prefix := conversation()[:n]
		StampTurnIDs(prefix)
		eq(t, "open tail prefix", turnIDs(prefix), turnIDs(full[:n]))
	}
}

// Fork seeding needs no machinery: a child shares its parent's prefix
// verbatim, so counting reproduces the parent's ids and the child continues
// from there. Siblings then conflict piecewise, exactly as LT already does.
func TestForkChildContinuesParentNumbering(t *testing.T) {
	parent := conversation()
	seed := StampTurnIDs(parent)

	const forkAt = 5 // everything below the second prompt is shared
	child := append(conversation()[:forkAt:forkAt],
		userMsg(prose("a different second question")),
		asstLT(0, prose("a different answer")),
	)
	childSeed := StampTurnIDs(child)

	eq(t, "shared prefix", turnIDs(child[:forkAt]), turnIDs(parent[:forkAt]))
	if child[forkAt].TurnID != parent[forkAt].TurnID {
		t.Fatalf("sibling turn ids diverge: child %d, parent %d",
			child[forkAt].TurnID, parent[forkAt].TurnID)
	}
	if childSeed != seed {
		t.Fatalf("child seed = %d, parent seed = %d — siblings must conflict piecewise",
			childSeed, seed)
	}
}

// Steering is the one case that looks like a new turn and is not: a user
// message bearing tool_result stays inside the turn even when it also carries
// prose.
func TestSteeringDoesNotOpenTurn(t *testing.T) {
	steer := userMsg(toolResult("t1"), prose("actually, check X"))
	if OpensTurn(steer) {
		t.Fatal("a tool_result message with prose must not open a turn")
	}
	if !OpensTurn(userMsg(prose("a real question"))) {
		t.Fatal("a pure-prose user message must open a turn")
	}
	if OpensTurn(userMsg(toolResult("t1"))) {
		t.Fatal("a bare tool_result must not open a turn")
	}
	if OpensTurn(asstLT(0, prose("assistant prose"))) {
		t.Fatal("an assistant message must not open a turn")
	}
}

// withLTs assigns sequential logical times the way the store does on read:
// LT is the frame index, not a payload field.
func withLTs(msgs []message.Message) []message.Message {
	for i := range msgs {
		msgs[i].LogicalTime = uint64(i + 1)
	}
	return msgs
}

// TurnSpan is THE resolver: it is what turns the number a human types in
// `fig send <aria>:<turn>` into the atMainLT a fork takes. first must be the
// PROMPT's LT — atMainLT is exclusive of the frozen prefix, so forking there
// shares everything strictly before the question and replaces the question
// onward.
func TestTurnSpan(t *testing.T) {
	msgs := withLTs(conversation())
	// conversation(): [boot, prompt1, asst, toolresult+steering, asst, prompt2, asst]
	//        LT:         1      2       3         4               5      6       7
	for _, c := range []struct{ turn, first, last uint64 }{
		{1, 2, 5},
		{2, 6, 7},
	} {
		first, last, ok := TurnSpan(msgs, c.turn)
		if !ok || first != c.first || last != c.last {
			t.Fatalf("turn %d: got (%d,%d,%v), want (%d,%d,true)", c.turn, first, last, ok, c.first, c.last)
		}
	}
}

// The span's first LT must be a user prompt for every turn. That is the whole
// safety argument for turn addressing: a fork boundary that is always a user
// message can never strand a tool_invoke without its result, so interrupted-
// tool synthesis is unreachable for a user-initiated fork.
func TestTurnSpanAlwaysStartsOnAPrompt(t *testing.T) {
	msgs := withLTs(conversation())
	last := StampTurnIDs(msgs)
	for turn := uint64(1); turn <= last; turn++ {
		first, _, ok := TurnSpan(msgs, turn)
		if !ok {
			t.Fatalf("turn %d missing", turn)
		}
		for _, m := range msgs {
			if m.LogicalTime == first && !OpensTurn(m) {
				t.Fatalf("turn %d starts at LT %d which does not open a turn", turn, first)
			}
		}
	}
}

func TestTurnSpanUnknown(t *testing.T) {
	if _, _, ok := TurnSpan(withLTs(conversation()), 99); ok {
		t.Fatal("turn 99 should not resolve")
	}
}
