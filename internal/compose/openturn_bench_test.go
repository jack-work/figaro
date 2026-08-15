package compose

import (
	"fmt"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

// S6, priced. composeTurn reads the WHOLE open region and Nodes() rebuilds
// every node of it on EVERY frame; Server.Update then diffs the result and
// discards everything that did not change. The producer is O(turn size) per
// frame while the wire is already incremental.
//
// On the live daemon this was 157.7 MB, 44% of heap. These benchmarks are the
// same shape without a daemon: hold the frame count fixed, grow the turn, and
// watch one frame get more expensive for work it has already done.
//
// PERMANENT, NOT MIGRATION SCAFFOLDING.

// openTurn builds the messages of a turn that has run `rounds` tool rounds and
// is still open: the region composeTurn re-reads on every stream event.
func openTurn(rounds int) []message.Message {
	body := ""
	for i := 0; i < 200; i++ {
		body += fmt.Sprintf("tool output line %d, wide enough to cost something\n", i)
	}
	var msgs []message.Message
	lt := uint64(1)
	for r := 0; r < rounds; r++ {
		call := fmt.Sprintf("call-%d", r)
		msgs = append(msgs, message.Message{
			Role: message.RoleOutput, LogicalTime: lt, TurnID: 1,
			Content: []message.Content{
				message.TextContent(fmt.Sprintf("round %d reasoning, a paragraph of prose that the composer must walk", r)),
				{Type: message.ContentToolInvoke, ToolCallID: call, ToolName: "bash",
					Arguments: map[string]any{"command": "echo hi"}},
			},
		})
		lt++
		msgs = append(msgs, message.Message{
			Role: message.RoleInput, LogicalTime: lt, TurnID: 1,
			Content: []message.Content{message.ToolResultContent(call, "bash", body, false)},
		})
		lt++
	}
	return msgs
}

func benchFrame(b *testing.B, rounds int) {
	msgs := openTurn(rounds)
	tails := map[string]string{}
	args := map[string]string{}

	// Prove the fixture composes what it claims before timing it.
	got := Nodes(msgs, tails, args)
	if len(got) < rounds {
		b.Fatalf("fixture composed %d nodes for %d rounds; it is not building the turn", len(got), rounds)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Nodes(msgs, tails, args)
	}
}

// One FRAME of a turn that has run N rounds. The cost should be the newest
// round's; it is the whole turn's.
func BenchmarkOpenTurnFrame10(b *testing.B)  { benchFrame(b, 10) }
func BenchmarkOpenTurnFrame40(b *testing.B)  { benchFrame(b, 40) }
func BenchmarkOpenTurnFrame160(b *testing.B) { benchFrame(b, 160) }

// A whole turn's worth of frames, which is what a real turn actually costs:
// every round emits many stream events, and each one recomposes everything
// before it. This is the quadratic the open region hides.
func BenchmarkTurnAllFrames(b *testing.B) {
	const rounds = 40
	const framesPerRound = 8

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tails := map[string]string{}
		args := map[string]string{}
		for r := 1; r <= rounds; r++ {
			msgs := openTurn(r)
			for f := 0; f < framesPerRound; f++ {
				_ = Nodes(msgs, tails, args)
			}
		}
	}
}
