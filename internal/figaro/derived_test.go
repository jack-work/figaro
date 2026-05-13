package figaro

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// memLog is a tiny in-memory store.Log[message.Message] for derivation
// tests. Only Read is used; everything else is a no-op stub.
type memLog struct {
	entries []store.Entry[message.Message]
}

func (m *memLog) Read() []store.Entry[message.Message] { return m.entries }
func (m *memLog) Lookup(uint64) (store.Entry[message.Message], bool) {
	return store.Entry[message.Message]{}, false
}
func (m *memLog) PeekTail() (store.Entry[message.Message], bool) {
	return store.Entry[message.Message]{}, false
}
func (m *memLog) ScanFromEnd(int) []store.Entry[message.Message] { return nil }
func (m *memLog) Append(e store.Entry[message.Message]) (store.Entry[message.Message], error) {
	return e, nil
}
func (m *memLog) Clear() error { return nil }
func (m *memLog) Close() error { return nil }

func TestMetaDerivation_PopulatesContextAndChalkboardFields(t *testing.T) {
	mlog := &memLog{
		entries: []store.Entry[message.Message]{
			{LT: 1, Payload: message.Message{
				Role:    message.RoleUser,
				Content: []message.Content{{Type: message.ContentText, Text: "hi"}},
			}},
			{LT: 2, Payload: message.Message{
				Role:    message.RoleAssistant,
				Content: []message.Content{{Type: message.ContentText, Text: "hello"}},
				Usage: &message.Usage{
					InputTokens:     1234,
					OutputTokens:    56,
					CacheReadTokens: 7,
				},
			}},
		},
	}
	d := &metaDerivation{
		ariaID:       "test-aria",
		providerName: "anthropic",
		figLog:       mlog,
	}
	snap := chalkboard.Snapshot{
		"system.provider": json.RawMessage(`"anthropic"`),
		"system.model":    json.RawMessage(`"claude-sonnet-4-5"`),
	}
	var buf bytes.Buffer
	if err := d.OnTick(&buf, DerivationEvent{FigaroLT: 2, Snapshot: snap}); err != nil {
		t.Fatalf("OnTick: %v", err)
	}
	var got MetaSnapshot
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, buf.String())
	}
	if got.AriaID != "test-aria" {
		t.Errorf("AriaID = %q, want test-aria", got.AriaID)
	}
	if got.Provider != "anthropic" {
		t.Errorf("Provider = %q, want anthropic", got.Provider)
	}
	if got.Model != "claude-sonnet-4-5" {
		t.Errorf("Model = %q, want claude-sonnet-4-5", got.Model)
	}
	if got.MessageCount != 2 {
		t.Errorf("MessageCount = %d, want 2", got.MessageCount)
	}
	if got.TokensIn != 1234 || got.TokensOut != 56 || got.CacheReadTokens != 7 {
		t.Errorf("usage totals wrong: in=%d out=%d cacheRead=%d",
			got.TokensIn, got.TokensOut, got.CacheReadTokens)
	}
	// Last message has Usage → ContextSize is exact, equal to in+out.
	if got.ContextTokens != 1234+56 {
		t.Errorf("ContextTokens = %d, want %d", got.ContextTokens, 1234+56)
	}
	if !got.ContextExact {
		t.Errorf("ContextExact = false, want true (last msg has Usage watermark)")
	}
}

func TestMetaDerivation_EmptyLog(t *testing.T) {
	d := &metaDerivation{ariaID: "empty", figLog: &memLog{}}
	var buf bytes.Buffer
	if err := d.OnTick(&buf, DerivationEvent{Snapshot: chalkboard.Snapshot{}}); err != nil {
		t.Fatalf("OnTick: %v", err)
	}
	var got MetaSnapshot
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.MessageCount != 0 || got.ContextTokens != 0 {
		t.Errorf("empty log should be zero, got msgs=%d ctx=%d", got.MessageCount, got.ContextTokens)
	}
	if !got.ContextExact {
		t.Errorf("empty log ContextExact should be true")
	}
}
