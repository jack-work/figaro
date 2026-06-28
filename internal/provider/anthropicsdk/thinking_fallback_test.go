package anthropicsdk

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
)

// The cache-miss fallback (encode from the provider-agnostic IR) must
// never emit a thinking block: the IR carries no signature, and an
// unsigned thinking block is a 400 once extended thinking is enabled.
// The signed wire form is cached at production via acc.ToParam instead.
func TestEncodeDropsUnsignedThinking(t *testing.T) {
	p := &Provider{}
	snap := chalkboard.Snapshot{}
	mp, ok := p.renderMessage(message.Message{
		Role: message.RoleAssistant,
		Content: []message.Content{
			{Type: message.ContentThinking, Text: "internal reasoning"},
			{Type: message.ContentProse, Text: "the answer"},
			{Type: message.ContentToolInvoke, ToolCallID: "tu_1", ToolName: "read"},
		},
	}, &snap)
	if !ok {
		t.Fatal("renderMessage dropped the whole turn")
	}
	out, err := json.Marshal(mp)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("thinking")) {
		t.Fatalf("fallback must not emit a thinking block: %s", out)
	}
	if !bytes.Contains(out, []byte("the answer")) || !bytes.Contains(out, []byte("tool_use")) {
		t.Fatalf("text and tool_use must survive: %s", out)
	}
}
