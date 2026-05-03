package figaro_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
)

// translogProvider stamps the figaro fingerprint and replies "ok".
type translogProvider struct{}

func (translogProvider) Name() string                                           { return "tlp" }
func (translogProvider) Fingerprint() string                                    { return "tlp/v0" }
func (translogProvider) SetModel(string)                                        {}
func (translogProvider) Models(_ context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (translogProvider) Decode(raw []json.RawMessage) ([]message.Message, error) {
	return mockDecodeNative(raw)
}
func (translogProvider) EncodeMessage(_ message.Message, _ chalkboard.Snapshot) ([]json.RawMessage, error) {
	return []json.RawMessage{json.RawMessage(`{"role":"user","content":[]}`)}, nil
}
func (translogProvider) AssembleRequest(_ [][]json.RawMessage, _ chalkboard.Snapshot, _ []provider.Tool, _ int) ([]byte, error) {
	return nil, nil
}
func (translogProvider) DecodeDelta(payload []json.RawMessage) (string, message.ContentType, bool) {
	return mockDecodeDelta(payload)
}
func (translogProvider) Assemble(deltas [][]json.RawMessage) ([]json.RawMessage, error) {
	return mockAssemble(deltas)
}
func (translogProvider) Send(_ context.Context, _ []byte, bus provider.Bus) error {
	mockPushAssistant(bus, "ok")
	return nil
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

	// One turn ends with two durable entries: the user tic encoded
	// by catchUpTranslation and the assembled assistant condensed
	// from the live tail.
	all := log.Durable()
	require.Len(t, all, 2, "one prompt produces user tic + assistant entries")
	for _, e := range all {
		assert.Equal(t, "spy/v0", e.Fingerprint)
	}
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
	require.Len(t, all, 2, "user tic + assistant translation")
	for _, e := range all {
		assert.NotZero(t, e.FigaroLT)
		assert.Equal(t, "tlp/v0", e.Fingerprint)
		require.NotEmpty(t, e.Payload)

		got, ok := log.Lookup(e.FigaroLT)
		require.True(t, ok)
		assert.Equal(t, e.LT, got.LT)
	}
}
