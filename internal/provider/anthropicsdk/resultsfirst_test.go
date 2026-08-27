package anthropicsdk

import (
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

// The SDK path's half of TestForkNoticeDoesNotDisplaceTheResults: a fork
// taken while the parent's tools are still running puts the branch's fork
// notice between the invoke and the results, and coalescing merges the two
// user messages with the notice in front. The API wants the results at the
// HEAD of the turn that answers a call, so the merge puts them there.
func TestCoalesceKeepsResultsAtTheHeadOfTheTurn(t *testing.T) {
	msgs := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock("do it")),
		anthropic.NewAssistantMessage(
			anthropic.NewToolUseBlock("toolu_A", map[string]any{"command": "sleep 1"}, "bash"),
			anthropic.NewToolUseBlock("toolu_B", map[string]any{"command": "sleep 2"}, "bash"),
		),
		anthropic.NewUserMessage(anthropic.NewTextBlock(`<system-reminder name="fork">…</system-reminder>`)),
		anthropic.NewUserMessage(
			anthropic.NewToolResultBlock("toolu_A", "ok", false),
			anthropic.NewToolResultBlock("toolu_B", "ok", false),
		),
		anthropic.NewUserMessage(anthropic.NewTextBlock("test")),
	}
	out, lts := coalesceMessages(msgs, []uint64{4, 5, 6, 7, 8})
	if len(out) != 3 || len(lts) != 3 {
		t.Fatalf("coalesce = %d messages, want 3", len(out))
	}
	answering := out[2].Content
	if len(answering) != 4 {
		t.Fatalf("the answering turn holds %d blocks, want 4", len(answering))
	}
	for i := 0; i < 2; i++ {
		if answering[i].OfToolResult == nil {
			t.Fatalf("block %d is not a result: the notice still displaces them", i)
		}
	}
	for i := 2; i < 4; i++ {
		if answering[i].OfToolResult != nil {
			t.Fatalf("block %d is a result behind a text block", i)
		}
	}
}

// A result the door appended behind prose is rendered in front of it.
func TestRenderedResultsLeadTheirTurn(t *testing.T) {
	p := &Provider{}
	snap := form.Snapshot{}
	mp, ok := p.renderMessage(message.Message{
		Role: message.RoleInput,
		Content: []message.Content{
			message.TextContent("carry on"),
			message.ToolResultContent("toolu_A", "bash", "closed", true),
		},
	}, &snap)
	if !ok {
		t.Fatal("renderMessage dropped the turn")
	}
	if len(mp.Content) != 2 || mp.Content[0].OfToolResult == nil || mp.Content[1].OfText == nil {
		t.Fatalf("results do not lead the turn: %+v", mp.Content)
	}
}
