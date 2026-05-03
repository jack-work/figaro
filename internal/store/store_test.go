package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
)

// --- FileStream[T] tests (canonical figaro IR) ---

func TestFileStream_EmptyStart(t *testing.T) {
	s, err := OpenFileStream[message.Message](filepath.Join(t.TempDir(), "aria.jsonl"))
	require.NoError(t, err)
	assert.Empty(t, s.Durable())
	_, ok := s.PeekTail()
	assert.False(t, ok)
}

func TestFileStream_AppendPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aria.jsonl")

	s, err := OpenFileStream[message.Message](path)
	require.NoError(t, err)

	entry, err := s.Append(Entry[message.Message]{
		Payload: message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("hello")}},
	}, true)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), entry.LT)
	assert.Equal(t, uint64(1), entry.FigaroLT, "canonical stream: LT == FigaroLT")

	// Reload from disk.
	s2, err := OpenFileStream[message.Message](path)
	require.NoError(t, err)
	d := s2.Durable()
	require.Len(t, d, 1)
	assert.Equal(t, "hello", d[0].Payload.Content[0].Text)
	assert.Equal(t, uint64(1), d[0].LT)
}

func TestFileStream_LogicalTimeContinuity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aria.jsonl")

	s, err := OpenFileStream[message.Message](path)
	require.NoError(t, err)

	for _, text := range []string{"one", "two", "three"} {
		_, err := s.Append(Entry[message.Message]{
			Payload: message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent(text)}},
		}, true)
		require.NoError(t, err)
	}

	s2, err := OpenFileStream[message.Message](path)
	require.NoError(t, err)
	e4, err := s2.Append(Entry[message.Message]{
		Payload: message.Message{Role: message.RoleAssistant, Content: []message.Content{message.TextContent("four")}},
	}, true)
	require.NoError(t, err)
	assert.Equal(t, uint64(4), e4.LT)
}

func TestFileStream_Lookup(t *testing.T) {
	s, err := OpenFileStream[message.Message](filepath.Join(t.TempDir(), "aria.jsonl"))
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		_, err := s.Append(Entry[message.Message]{
			Payload: message.Message{Role: message.RoleUser},
		}, true)
		require.NoError(t, err)
	}

	got, ok := s.Lookup(2)
	require.True(t, ok)
	assert.Equal(t, uint64(2), got.LT)
	assert.Equal(t, uint64(2), got.FigaroLT)

	_, ok = s.Lookup(99)
	assert.False(t, ok)
}

func TestFileStream_ScanFromEnd(t *testing.T) {
	s, err := OpenFileStream[message.Message](filepath.Join(t.TempDir(), "aria.jsonl"))
	require.NoError(t, err)

	for _, text := range []string{"one", "two", "three", "four"} {
		_, err := s.Append(Entry[message.Message]{
			Payload: message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent(text)}},
		}, true)
		require.NoError(t, err)
	}

	tail := s.ScanFromEnd(2)
	require.Len(t, tail, 2)
	assert.Equal(t, "four", tail[0].Payload.Content[0].Text, "newest first")
	assert.Equal(t, "three", tail[1].Payload.Content[0].Text)

	// n > len returns all.
	all := s.ScanFromEnd(10)
	assert.Len(t, all, 4)
}

func TestFileStream_LiveAndCondense(t *testing.T) {
	s, err := OpenFileStream[string](filepath.Join(t.TempDir(), "live.jsonl"))
	require.NoError(t, err)

	for _, chunk := range []string{"hel", "lo, ", "world"} {
		_, err := s.Append(Entry[string]{Payload: chunk}, false)
		require.NoError(t, err)
	}
	assert.Len(t, s.Live(), 3)

	entry, err := s.Condense(Entry[string]{Payload: "hello, world"})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), entry.LT)
	assert.Empty(t, s.Live(), "live tail empty after condense")
	assert.Equal(t, "hello, world", entry.Payload)
}

func TestFileStream_DiscardLive(t *testing.T) {
	s, err := OpenFileStream[string](filepath.Join(t.TempDir(), "live.jsonl"))
	require.NoError(t, err)

	_, _ = s.Append(Entry[string]{Payload: "partial"}, false)
	_, _ = s.Append(Entry[string]{Payload: "more partial"}, false)
	require.Len(t, s.Live(), 2)

	require.NoError(t, s.DiscardLive())
	assert.Empty(t, s.Live())
	assert.Empty(t, s.Durable(), "discard must not produce a durable entry")
}

func TestFileStream_Clear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aria.jsonl")

	s, err := OpenFileStream[message.Message](path)
	require.NoError(t, err)
	_, _ = s.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleUser}}, true)

	require.NoError(t, s.Clear())
	assert.Empty(t, s.Durable())
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err))
}

// --- Translator stream (Stream[[]json.RawMessage]) tests ---

func TestFileStream_Translation_FK(t *testing.T) {
	s, err := OpenFileStream[[]json.RawMessage](filepath.Join(t.TempDir(), "anthropic.jsonl"))
	require.NoError(t, err)

	entry, err := s.Append(Entry[[]json.RawMessage]{
		FigaroLT:    7,
		Payload:     []json.RawMessage{json.RawMessage(`{"role":"assistant"}`)},
		Fingerprint: "anth/v1",
	}, true)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), entry.LT, "alt is stream-local, starts at 1")
	assert.Equal(t, uint64(7), entry.FigaroLT, "FK preserved")

	got, ok := s.Lookup(7)
	require.True(t, ok)
	assert.Equal(t, "anth/v1", got.Fingerprint)
	require.Len(t, got.Payload, 1)
}

// --- MemStream[T] tests (ephemeral) ---

func TestMemStream_Standalone(t *testing.T) {
	s := NewMemStream[message.Message]()

	assert.Empty(t, s.Durable())

	entry, err := s.Append(Entry[message.Message]{
		Payload: message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("hello")}},
	}, true)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), entry.LT)

	require.Len(t, s.Durable(), 1)
	require.NoError(t, s.Clear())
	assert.Empty(t, s.Durable())
}

// --- FileBackend tests ---

func TestFileBackend_OpenAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	b, err := NewFileBackend(dir)
	require.NoError(t, err)

	s, err := b.Open("abc")
	require.NoError(t, err)

	_, err = s.Append(Entry[message.Message]{
		Payload: message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("hi")}},
	}, true)
	require.NoError(t, err)

	// Check file landed in the expected per-aria layout.
	_, err = os.Stat(filepath.Join(dir, "abc", "aria.jsonl"))
	require.NoError(t, err)
}

func TestFileBackend_OpenTranslation(t *testing.T) {
	dir := t.TempDir()
	b, err := NewFileBackend(dir)
	require.NoError(t, err)

	s, err := b.OpenTranslation("abc", "anthropic")
	require.NoError(t, err)

	_, err = s.Append(Entry[[]json.RawMessage]{
		FigaroLT: 1,
		Payload:  []json.RawMessage{json.RawMessage(`{}`)},
	}, true)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(dir, "abc", "translations", "anthropic.jsonl"))
	require.NoError(t, err)
}

func TestFileBackend_MetaPersistence(t *testing.T) {
	dir := t.TempDir()
	b, err := NewFileBackend(dir)
	require.NoError(t, err)

	meta := &AriaMeta{
		Provider: "anthropic",
		Model:    "claude-sonnet-4-20250514",
		Cwd:      "/home/test",
		Root:     "/home/test/project",
	}
	require.NoError(t, b.SetMeta("aria-1", meta))

	got, err := b.Meta("aria-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, meta.Provider, got.Provider)
	assert.Equal(t, meta.Model, got.Model)
}

func TestFileBackend_Meta_Missing(t *testing.T) {
	dir := t.TempDir()
	b, _ := NewFileBackend(dir)
	got, err := b.Meta("ghost")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestFileBackend_List(t *testing.T) {
	dir := t.TempDir()
	b, err := NewFileBackend(dir)
	require.NoError(t, err)

	for _, id := range []string{"a", "b"} {
		s, err := b.Open(id)
		require.NoError(t, err)
		_, err = s.Append(Entry[message.Message]{
			Payload: message.Message{Role: message.RoleUser},
		}, true)
		require.NoError(t, err)
	}

	arias, err := b.List()
	require.NoError(t, err)
	assert.Len(t, arias, 2)

	byID := make(map[string]AriaInfo)
	for _, a := range arias {
		byID[a.ID] = a
	}
	assert.Equal(t, 1, byID["a"].MessageCount)
	assert.Equal(t, 1, byID["b"].MessageCount)
}

func TestFileBackend_Remove(t *testing.T) {
	dir := t.TempDir()
	b, _ := NewFileBackend(dir)

	s, _ := b.Open("doomed")
	_, _ = s.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleUser}}, true)

	require.NoError(t, b.Remove("doomed"))
	_, err := os.Stat(filepath.Join(dir, "doomed"))
	assert.True(t, os.IsNotExist(err))

	// Idempotent.
	assert.NoError(t, b.Remove("doomed"))
	assert.NoError(t, b.Remove("ghost"))
}
