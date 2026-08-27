package anthropicsdk

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

// The cache-miss fallback (encode from the provider-agnostic IR) must
// never emit a thinking block: the IR carries no signature, and an
// unsigned thinking block is a 400 once extended thinking is enabled.
// The signed wire form is cached at production via acc.ToParam instead.
func TestEncodeDropsUnsignedThinking(t *testing.T) {
	p := &Provider{}
	snap := form.Snapshot{}
	mp, ok := p.renderMessage(message.Message{
		Role: message.RoleOutput,
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

// The same rule on the SDK path: an unsigned thinking block never replays,
// so it never reaches the cache. See TestAssistantCacheNativeDropsUnsignedThinking.
func TestUnsignedThinkingIsNotCacheable(t *testing.T) {
	var acc anthropic.Message
	if err := json.Unmarshal([]byte(`{"role":"assistant","type":"message","content":[
		{"type":"thinking","thinking":"cut before the signature arrived"},
		{"type":"thinking","thinking":"","signature":"sig"},
		{"type":"thinking","thinking":"whole","signature":"sig"}]}`), &acc); err != nil {
		t.Fatal(err)
	}
	want := []bool{false, true, true}
	for i, b := range acc.Content {
		keep, fatal := cacheableAccumulatedBlock(b)
		if fatal {
			t.Fatalf("block %d: fatal", i)
		}
		if keep != want[i] {
			t.Errorf("block %d: keep=%v, want %v", i, keep, want[i])
		}
	}

	p := &Provider{CacheNamespace: "anthropic"}
	acc.Content = acc.Content[:1]
	cache, err := p.assistantCache(acc)
	if err != nil {
		t.Fatal(err)
	}
	if len(cache.Payload) != 0 {
		t.Fatalf("unsigned thinking reached the cache: %s", cache.Payload[0])
	}
}
