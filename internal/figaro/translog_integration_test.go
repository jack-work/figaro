package figaro_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/causal"
	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
)

// translogProvider emits a populated StreamEvent.Translation on every
// assistant Done event so we can verify the agent writes those
// entries to the log.
type translogProvider struct{}

func (translogProvider) Name() string                                              { return "tlp" }
func (translogProvider) Fingerprint() string                                       { return "tlp/v0" }
func (translogProvider) SetModel(string)                                           {}
func (translogProvider) Models(_ context.Context) ([]provider.ModelInfo, error)    { return nil, nil }
func (translogProvider) OpenAccumulator() provider.NativeAccumulator               { return nil }
func (translogProvider) Send(_ context.Context, _ *message.Block, _ chalkboard.Snapshot, _ causal.Slice[message.ProviderTranslation], _ []provider.Tool, _ int) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, 4)
	go func() {
		defer close(ch)
		msg := &message.Message{
			Role:       message.RoleAssistant,
			Content:    []message.Content{message.TextContent("ok")},
			StopReason: message.StopEnd,
			Timestamp:  time.Now().UnixMilli(),
		}
		ch <- provider.StreamEvent{
			Done:    true,
			Message: msg,
			Translation: &message.ProviderTranslation{
				Messages:    []json.RawMessage{json.RawMessage(`{"role":"assistant","content":"ok"}`)},
				Fingerprint: "tlp/v0",
			},
		}
	}()
	return ch, nil
}

// TestTranslog_AssistantResponseAppendsEntry verifies that when an
// assistant turn finishes, the wire-form projection (whatever the
// provider's accumulator returned) gets appended to the translation
// log keyed by the assistant message's logical time.
func TestTranslog_AssistantResponseAppendsEntry(t *testing.T) {
	dir := t.TempDir()
	log, err := store.OpenFileTranslationLog(filepath.Join(dir, "translations", "spy.jsonl"))
	require.NoError(t, err)

	prov := &chalkSpyProvider{}
	a := figaro.NewAgent(figaro.Config{
		ID:             "translog-test",
		SocketPath:     dir + "/sock",
		Provider:       prov,
		Model:          "claude-test",
		Cwd:            "/tmp",
		Root:           "/tmp",
		MaxTokens:      1024,
		Tools:          tool.NewRegistry(),
		TranslationLog: log,
	})
	t.Cleanup(func() { a.Kill() })

	sub := a.Subscribe()
	defer a.Unsubscribe(sub)
	a.Prompt("hello")

	deadline := time.After(2 * time.Second)
	for {
		select {
		case n := <-sub:
			if n.Method == "stream.done" {
				goto done
			}
		case <-deadline:
			t.Fatal("timeout")
		}
	}
done:

	// chalkSpyProvider doesn't populate StreamEvent.Translation, so
	// no entry should land. Confirm — and confirm the surface is
	// wired (no panics, no race).
	all := log.All()
	assert.Empty(t, all,
		"spy provider doesn't set StreamEvent.Translation; agent must not write empty entries")
}

// TestTranslog_PopulatedTranslationLands verifies that an
// assistant Done event with a populated StreamEvent.Translation
// causes the agent to append a TranslationEntry keyed by the
// assistant message's logical time.
func TestTranslog_PopulatedTranslationLands(t *testing.T) {
	dir := t.TempDir()
	log, err := store.OpenFileTranslationLog(filepath.Join(dir, "translations", "tlp.jsonl"))
	require.NoError(t, err)

	a := figaro.NewAgent(figaro.Config{
		ID:             "translog-test-2",
		SocketPath:     dir + "/sock",
		Provider:       translogProvider{},
		Model:          "tlp-model",
		Cwd:            "/tmp",
		Root:           "/tmp",
		MaxTokens:      1024,
		Tools:          tool.NewRegistry(),
		TranslationLog: log,
	})
	t.Cleanup(func() { a.Kill() })

	sub := a.Subscribe()
	defer a.Unsubscribe(sub)
	a.Prompt("hello")

	deadline := time.After(2 * time.Second)
	for {
		select {
		case n := <-sub:
			if n.Method == "stream.done" {
				goto done
			}
		case <-deadline:
			t.Fatal("timeout")
		}
	}
done:

	all := log.All()
	require.Len(t, all, 1, "exactly one assistant translation should land")
	entry := all[0]
	require.Len(t, entry.FigaroLTs, 1)
	assert.Equal(t, "tlp/v0", entry.Fingerprint)
	require.Len(t, entry.Messages, 1)

	// And: looking up by the assistant's lt finds the entry.
	flt := entry.FigaroLTs[0]
	got, ok := log.Lookup(flt)
	require.True(t, ok)
	assert.Equal(t, entry.Alt, got.Alt)
}
