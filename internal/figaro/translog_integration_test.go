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
func (translogProvider) Send(_ context.Context, _ *message.Block, _ chalkboard.Snapshot, _ causal.Slice[message.ProviderTranslation], _ []provider.Tool, _ int) (<-chan provider.StreamEvent, provider.ProjectionSummary, error) {
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
	return ch, provider.ProjectionSummary{}, nil
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

// TestTranslog_StaleEntriesClearedOnOpen verifies that translog
// entries with a non-matching fingerprint are wiped at NewAgent
// time. Setup: pre-populate a log with a "old/v0" entry, then open
// an Agent whose provider returns "tlp/v0" — the entry should be
// gone before the agent serves anything.
func TestTranslog_StaleEntriesClearedOnOpen(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "translations", "tlp.jsonl")

	pre, err := store.OpenFileTranslationLog(logPath)
	require.NoError(t, err)
	_, err = pre.Append([]uint64{1}, []json.RawMessage{json.RawMessage(`{"x":1}`)}, "old/v0")
	require.NoError(t, err)
	require.NoError(t, pre.Close())

	log, err := store.OpenFileTranslationLog(logPath)
	require.NoError(t, err)
	require.Len(t, log.All(), 1, "preloaded entry should be on disk")

	a := figaro.NewAgent(figaro.Config{
		ID:             "translog-stale",
		SocketPath:     dir + "/sock",
		Provider:       translogProvider{}, // Fingerprint() == "tlp/v0"
		Model:          "tlp-model",
		Cwd:            "/tmp",
		Root:           "/tmp",
		MaxTokens:      1024,
		Tools:          tool.NewRegistry(),
		TranslationLog: log,
	})
	t.Cleanup(func() { a.Kill() })

	assert.Empty(t, log.All(),
		"NewAgent must clear translog entries whose fingerprint disagrees with the provider's")
}

// TestTranslog_MatchingEntriesPreserved verifies the symmetric case:
// fingerprints match the provider's current value, log is left alone.
func TestTranslog_MatchingEntriesPreserved(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "translations", "tlp.jsonl")

	pre, err := store.OpenFileTranslationLog(logPath)
	require.NoError(t, err)
	_, err = pre.Append([]uint64{1}, []json.RawMessage{json.RawMessage(`{"x":1}`)}, "tlp/v0")
	require.NoError(t, err)
	require.NoError(t, pre.Close())

	log, err := store.OpenFileTranslationLog(logPath)
	require.NoError(t, err)

	a := figaro.NewAgent(figaro.Config{
		ID:             "translog-fresh",
		SocketPath:     dir + "/sock",
		Provider:       translogProvider{}, // Fingerprint() == "tlp/v0"
		Model:          "tlp-model",
		Cwd:            "/tmp",
		Root:           "/tmp",
		MaxTokens:      1024,
		Tools:          tool.NewRegistry(),
		TranslationLog: log,
	})
	t.Cleanup(func() { a.Kill() })

	assert.Len(t, log.All(), 1,
		"matching fingerprints must leave the log untouched")
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
