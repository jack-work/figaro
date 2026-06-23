package anthropicsdk

import (
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// Anthropic requires alternating roles; consecutive same-role tics (from a
// turn erroring then the prompt being resent) must merge, not reach the wire
// as a non-alternating request (which deterministically 500s).
func TestCoalesceMessages(t *testing.T) {
	msgs := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock("a")),
		anthropic.NewUserMessage(anthropic.NewTextBlock("b")), // dup user
		anthropic.NewUserMessage(anthropic.NewTextBlock("c")), // another
		anthropic.NewAssistantMessage(anthropic.NewTextBlock("r")),
		anthropic.NewUserMessage(anthropic.NewTextBlock("d")),
	}
	lts := []uint64{1, 2, 3, 4, 5}
	out, outLTs := coalesceMessages(msgs, lts)

	if len(out) != 3 {
		t.Fatalf("want 3 messages (user, assistant, user); got %d", len(out))
	}
	if out[0].Role != "user" || out[1].Role != "assistant" || out[2].Role != "user" {
		t.Fatalf("roles not alternating: %v %v %v", out[0].Role, out[1].Role, out[2].Role)
	}
	if len(out[0].Content) != 3 {
		t.Fatalf("merged user should hold 3 content blocks; got %d", len(out[0].Content))
	}
	if len(outLTs) != 3 || outLTs[0] != 3 { // merged user keeps the latest tic's LT
		t.Fatalf("lts misaligned: %v", outLTs)
	}

	// No adjacent same-role pairs remain.
	for i := 1; i < len(out); i++ {
		if out[i].Role == out[i-1].Role {
			t.Fatalf("adjacent same-role survived at %d", i)
		}
	}
}
