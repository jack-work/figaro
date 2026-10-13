package compose

import (
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// openTurn builds the messages of a still-open turn of `rounds` tool rounds.
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

func BenchmarkOpenTurnFrame10(b *testing.B)  { benchFrame(b, 10) }
func BenchmarkOpenTurnFrame40(b *testing.B)  { benchFrame(b, 40) }
func BenchmarkOpenTurnFrame160(b *testing.B) { benchFrame(b, 160) }

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
