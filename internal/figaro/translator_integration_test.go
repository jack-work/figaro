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

// translatorMockProvider stamps a fixed fingerprint and replies "ok".
type translatorMockProvider struct{}

func (translatorMockProvider) Name() string                                           { return "tlp" }
func (translatorMockProvider) Fingerprint() string                                    { return "tlp/v0" }
func (translatorMockProvider) SetModel(string)                                        {}
func (translatorMockProvider) Models(_ context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (translatorMockProvider) Decode(payload []json.RawMessage) ([]message.Message, error) {
	return mockDecode(payload)
}
func (translatorMockProvider) Encode(_ message.Message, _ chalkboard.Snapshot) ([]json.RawMessage, error) {
	return []json.RawMessage{json.RawMessage(`{"role":"user","content":[]}`)}, nil
}
func (translatorMockProvider) Assemble(deltas [][]json.RawMessage) ([]json.RawMessage, error) {
	return mockAssemble(deltas)
}
func (translatorMockProvider) Send(_ context.Context, _ provider.SendInput, bus provider.Bus) error {
	mockPushAssistant(bus, "ok")
	return nil
}

// TestTranslator_AssistantResponseAppendsEntry verifies that one
// turn writes both the user tic (encoded by catchUpTranslator) and
// the assembled assistant (condensed from the live tail) to the
// translator stream.
func TestTranslator_AssistantResponseAppendsEntry(t *testing.T) {
	dir := t.TempDir()
	stream, err := store.OpenFileStream[[]json.RawMessage](filepath.Join(dir, "translations", "spy.jsonl"))
	require.NoError(t, err)

	prov := &chalkSpyProvider{}
	a := figaro.NewAgent(figaro.Config{
		ID:               "translator-test",
		SocketPath:       dir + "/sock",
		Provider:         prov,
		Model:            "claude-test",
		Cwd:              "/tmp",
		Root:             "/tmp",
		MaxTokens:        1024,
		Tools:            tool.NewRegistry(),
		TranslatorStream: stream,
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

	all := stream.Durable()
	require.Len(t, all, 2, "one prompt produces user tic + assistant entries")
	for _, e := range all {
		assert.Equal(t, "spy/v0", e.Fingerprint)
	}
}

// TestTranslator_StaleEntriesClearedOnOpen verifies that translator
// entries with a non-matching fingerprint are wiped at NewAgent time.
func TestTranslator_StaleEntriesClearedOnOpen(t *testing.T) {
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

	stream, err := store.OpenFileStream[[]json.RawMessage](logPath)
	require.NoError(t, err)
	require.Len(t, stream.Durable(), 1, "preloaded entry should be on disk")

	a := figaro.NewAgent(figaro.Config{
		ID:               "translator-stale",
		SocketPath:       dir + "/sock",
		Provider:         translatorMockProvider{}, // Fingerprint() == "tlp/v0"
		Model:            "tlp-model",
		Cwd:              "/tmp",
		Root:             "/tmp",
		MaxTokens:        1024,
		Tools:            tool.NewRegistry(),
		TranslatorStream: stream,
	})
	t.Cleanup(func() { a.Kill() })

	assert.Empty(t, stream.Durable(),
		"NewAgent must clear translator entries whose fingerprint disagrees with the provider's")
}

// TestTranslator_MatchingEntriesPreserved verifies the symmetric
// case: matching fingerprints leave the stream alone.
func TestTranslator_MatchingEntriesPreserved(t *testing.T) {
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

	stream, err := store.OpenFileStream[[]json.RawMessage](logPath)
	require.NoError(t, err)

	a := figaro.NewAgent(figaro.Config{
		ID:               "translator-fresh",
		SocketPath:       dir + "/sock",
		Provider:         translatorMockProvider{}, // Fingerprint() == "tlp/v0"
		Model:            "tlp-model",
		Cwd:              "/tmp",
		Root:             "/tmp",
		MaxTokens:        1024,
		Tools:            tool.NewRegistry(),
		TranslatorStream: stream,
	})
	t.Cleanup(func() { a.Kill() })

	assert.Len(t, stream.Durable(), 1,
		"matching fingerprints must leave the stream untouched")
}

// TestTranslator_PopulatedTranslationLands verifies that an
// assistant turn lands its assembled bytes (and the user tic before
// it) on the translator stream, each linked by FigaroLT.
func TestTranslator_PopulatedTranslationLands(t *testing.T) {
	dir := t.TempDir()
	stream, err := store.OpenFileStream[[]json.RawMessage](filepath.Join(dir, "translations", "tlp.jsonl"))
	require.NoError(t, err)

	a := figaro.NewAgent(figaro.Config{
		ID:               "translator-test-2",
		SocketPath:       dir + "/sock",
		Provider:         translatorMockProvider{},
		Model:            "tlp-model",
		Cwd:              "/tmp",
		Root:             "/tmp",
		MaxTokens:        1024,
		Tools:            tool.NewRegistry(),
		TranslatorStream: stream,
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

	all := stream.Durable()
	require.Len(t, all, 2, "user tic + assistant translation")
	for _, e := range all {
		assert.NotZero(t, e.FigaroLT)
		assert.Equal(t, "tlp/v0", e.Fingerprint)
		require.NotEmpty(t, e.Payload)

		got, ok := stream.Lookup(e.FigaroLT)
		require.True(t, ok)
		assert.Equal(t, e.LT, got.LT)
	}
}
