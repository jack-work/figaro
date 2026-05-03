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
// entries to the stream.
type translogProvider struct{}

func (translogProvider) Name() string                                           { return "tlp" }
func (translogProvider) Fingerprint() string                                    { return "tlp/v0" }
func (translogProvider) SetModel(string)                                        {}
func (translogProvider) Models(_ context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (translogProvider) Decode(raw []json.RawMessage) ([]message.Message, error) {
	return mockDecodeNative(raw)
}
func (translogProvider) Send(_ context.Context, _ []message.Message, _ chalkboard.Snapshot, _ causal.Slice[message.ProviderTranslation], _ []provider.Tool, _ int, bus provider.Bus) (provider.ProjectionSummary, error) {
	assembled := mockPushAssistant(bus, "ok")
	return provider.ProjectionSummary{
		Fingerprint: "tlp/v0",
		Assistant:   []json.RawMessage{assembled},
	}, nil
}

// TestTranslog_AssistantResponseAppendsEntry verifies that when an
// assistant turn finishes, the wire-form projection (whatever the
// provider's accumulator returned) gets appended to the translation
// stream keyed by the assistant message's logical time.
func TestTranslog_AssistantResponseAppendsEntry(t *testing.T) {
	dir := t.TempDir()
	log, err := store.OpenFileStream[[]json.RawMessage](filepath.Join(dir, "translations", "spy.jsonl"))
	require.NoError(t, err)

	prov := &chalkSpyProvider{}
	a := figaro.NewAgent(figaro.Config{
		ID:                "translog-test",
		SocketPath:        dir + "/sock",
		Provider:          prov,
		Model:             "claude-test",
		Cwd:               "/tmp",
		Root:              "/tmp",
		MaxTokens:         1024,
		Tools:             tool.NewRegistry(),
		TranslationStream: log,
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

	// Every assistant turn writes one durable native entry to the
	// translog (the provider's transport role).
	all := log.Durable()
	require.Len(t, all, 1, "one assistant turn = one durable native translog entry")
	assert.Equal(t, "spy/v0", all[0].Fingerprint)
}

// TestTranslog_StaleEntriesClearedOnOpen verifies that translog
// entries with a non-matching fingerprint are wiped at NewAgent
// time. Setup: pre-populate a stream with a "old/v0" entry, then
// open an Agent whose provider returns "tlp/v0" — the entry should
// be gone before the agent serves anything.
func TestTranslog_StaleEntriesClearedOnOpen(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "translations", "tlp.jsonl")

	pre, err := store.OpenFileStream[[]json.RawMessage](logPath)
	require.NoError(t, err)
	_, err = pre.Append(store.Entry[[]json.RawMessage]{
		FigaroLT:    1,
		Payload:     []json.RawMessage{json.RawMessage(`{"x":1}`)},
		Fingerprint: "old/v0",
	}, true)
	require.NoError(t, err)
	require.NoError(t, pre.Close())

	log, err := store.OpenFileStream[[]json.RawMessage](logPath)
	require.NoError(t, err)
	require.Len(t, log.Durable(), 1, "preloaded entry should be on disk")

	a := figaro.NewAgent(figaro.Config{
		ID:                "translog-stale",
		SocketPath:        dir + "/sock",
		Provider:          translogProvider{}, // Fingerprint() == "tlp/v0"
		Model:             "tlp-model",
		Cwd:               "/tmp",
		Root:              "/tmp",
		MaxTokens:         1024,
		Tools:             tool.NewRegistry(),
		TranslationStream: log,
	})
	t.Cleanup(func() { a.Kill() })

	assert.Empty(t, log.Durable(),
		"NewAgent must clear translation entries whose fingerprint disagrees with the provider's")
}

// TestTranslog_MatchingEntriesPreserved verifies the symmetric case:
// fingerprints match the provider's current value, stream is left alone.
func TestTranslog_MatchingEntriesPreserved(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "translations", "tlp.jsonl")

	pre, err := store.OpenFileStream[[]json.RawMessage](logPath)
	require.NoError(t, err)
	_, err = pre.Append(store.Entry[[]json.RawMessage]{
		FigaroLT:    1,
		Payload:     []json.RawMessage{json.RawMessage(`{"x":1}`)},
		Fingerprint: "tlp/v0",
	}, true)
	require.NoError(t, err)
	require.NoError(t, pre.Close())

	log, err := store.OpenFileStream[[]json.RawMessage](logPath)
	require.NoError(t, err)

	a := figaro.NewAgent(figaro.Config{
		ID:                "translog-fresh",
		SocketPath:        dir + "/sock",
		Provider:          translogProvider{}, // Fingerprint() == "tlp/v0"
		Model:             "tlp-model",
		Cwd:               "/tmp",
		Root:              "/tmp",
		MaxTokens:         1024,
		Tools:             tool.NewRegistry(),
		TranslationStream: log,
	})
	t.Cleanup(func() { a.Kill() })

	assert.Len(t, log.Durable(), 1,
		"matching fingerprints must leave the stream untouched")
}

// TestTranslog_PopulatedTranslationLands verifies that an assistant
// Done event with a populated StreamEvent.Translation causes the agent
// to append a translation Entry keyed by the assistant message's
// logical time.
func TestTranslog_PopulatedTranslationLands(t *testing.T) {
	dir := t.TempDir()
	log, err := store.OpenFileStream[[]json.RawMessage](filepath.Join(dir, "translations", "tlp.jsonl"))
	require.NoError(t, err)

	a := figaro.NewAgent(figaro.Config{
		ID:                "translog-test-2",
		SocketPath:        dir + "/sock",
		Provider:          translogProvider{},
		Model:             "tlp-model",
		Cwd:               "/tmp",
		Root:              "/tmp",
		MaxTokens:         1024,
		Tools:             tool.NewRegistry(),
		TranslationStream: log,
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

	all := log.Durable()
	require.Len(t, all, 1, "exactly one assistant translation should land")
	entry := all[0]
	assert.NotZero(t, entry.FigaroLT)
	assert.Equal(t, "tlp/v0", entry.Fingerprint)
	require.Len(t, entry.Payload, 1)

	// And: looking up by the assistant's lt finds the entry.
	got, ok := log.Lookup(entry.FigaroLT)
	require.True(t, ok)
	assert.Equal(t, entry.LT, got.LT)
}
