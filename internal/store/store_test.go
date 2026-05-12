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

// --- FileLog[T] tests (canonical figaro IR) ---

func TestFileLog_EmptyStart(t *testing.T) {
	s, err := OpenFileLog[message.Message](filepath.Join(t.TempDir(), "aria.jsonl"))
	require.NoError(t, err)
	assert.Empty(t, s.Read())
	_, ok := s.PeekTail()
	assert.False(t, ok)
}

func TestFileLog_AppendPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aria.jsonl")

	s, err := OpenFileLog[message.Message](path)
	require.NoError(t, err)

	entry, err := s.Append(Entry[message.Message]{
		Payload: message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("hello")}},
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), entry.LT)
	assert.Equal(t, uint64(1), entry.FigaroLT, "canonical log: LT == FigaroLT")

	// Reload from disk.
	s2, err := OpenFileLog[message.Message](path)
	require.NoError(t, err)
	d := s2.Read()
	require.Len(t, d, 1)
	assert.Equal(t, "hello", d[0].Payload.Content[0].Text)
	assert.Equal(t, uint64(1), d[0].LT)
}

func TestFileLog_LogicalTimeContinuity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aria.jsonl")

	s, err := OpenFileLog[message.Message](path)
	require.NoError(t, err)

	for _, text := range []string{"one", "two", "three"} {
		_, err := s.Append(Entry[message.Message]{
			Payload: message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent(text)}},
		})
		require.NoError(t, err)
	}

	s2, err := OpenFileLog[message.Message](path)
	require.NoError(t, err)
	e4, err := s2.Append(Entry[message.Message]{
		Payload: message.Message{Role: message.RoleAssistant, Content: []message.Content{message.TextContent("four")}},
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(4), e4.LT)
}

func TestFileLog_Lookup(t *testing.T) {
	s, err := OpenFileLog[message.Message](filepath.Join(t.TempDir(), "aria.jsonl"))
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		_, err := s.Append(Entry[message.Message]{
			Payload: message.Message{Role: message.RoleUser},
		})
		require.NoError(t, err)
	}

	got, ok := s.Lookup(2)
	require.True(t, ok)
	assert.Equal(t, uint64(2), got.LT)
	assert.Equal(t, uint64(2), got.FigaroLT)

	_, ok = s.Lookup(99)
	assert.False(t, ok)
}

func TestFileLog_ScanFromEnd(t *testing.T) {
	s, err := OpenFileLog[message.Message](filepath.Join(t.TempDir(), "aria.jsonl"))
	require.NoError(t, err)

	for _, text := range []string{"one", "two", "three", "four"} {
		_, err := s.Append(Entry[message.Message]{
			Payload: message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent(text)}},
		})
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

func TestFileLog_Clear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aria.jsonl")

	s, err := OpenFileLog[message.Message](path)
	require.NoError(t, err)
	_, _ = s.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleUser}})

	require.NoError(t, s.Clear())
	assert.Empty(t, s.Read())
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err))
}

// --- Translator log (Log[[]json.RawMessage]) tests ---

func TestFileLog_Translation_FK(t *testing.T) {
	s, err := OpenFileLog[[]json.RawMessage](filepath.Join(t.TempDir(), "anthropic.jsonl"))
	require.NoError(t, err)

	entry, err := s.Append(Entry[[]json.RawMessage]{
		FigaroLT:    7,
		Payload:     []json.RawMessage{json.RawMessage(`{"role":"assistant"}`)},
		Fingerprint: "anth/v1",
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), entry.LT, "alt is log-local, starts at 1")
	assert.Equal(t, uint64(7), entry.FigaroLT, "FK preserved")

	got, ok := s.Lookup(7)
	require.True(t, ok)
	assert.Equal(t, "anth/v1", got.Fingerprint)
	require.Len(t, got.Payload, 1)
}

// --- MemLog[T] tests (ephemeral) ---

func TestMemLog_Standalone(t *testing.T) {
	s := NewMemLog[message.Message]()

	assert.Empty(t, s.Read())

	entry, err := s.Append(Entry[message.Message]{
		Payload: message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("hello")}},
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), entry.LT)

	require.Len(t, s.Read(), 1)
	require.NoError(t, s.Clear())
	assert.Empty(t, s.Read())
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
	})
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
	})
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(dir, "abc", "translations", "anthropic.jsonl"))
	require.NoError(t, err)
}

func TestFileBackend_MetaPersistence(t *testing.T) {
	dir := t.TempDir()
	b, err := NewFileBackend(dir)
	require.NoError(t, err)

	meta := &AriaMeta{
		MessageCount: 7,
		TurnCount:    3,
		TokensIn:     1024,
		TokensOut:    256,
	}
	require.NoError(t, b.SetMeta("aria-1", meta))

	got, err := b.Meta("aria-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, meta.MessageCount, got.MessageCount)
	assert.Equal(t, meta.TurnCount, got.TurnCount)
	assert.Equal(t, meta.TokensIn, got.TokensIn)
	assert.Equal(t, meta.TokensOut, got.TokensOut)
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

	for i, id := range []string{"a", "b"} {
		s, err := b.Open(id)
		require.NoError(t, err)
		_, err = s.Append(Entry[message.Message]{
			Payload: message.Message{Role: message.RoleUser},
		})
		require.NoError(t, err)
		require.NoError(t, b.SetMeta(id, &AriaMeta{MessageCount: i + 1}))
	}

	arias, err := b.List()
	require.NoError(t, err)
	assert.Len(t, arias, 2)

	byID := make(map[string]AriaInfo)
	for _, a := range arias {
		byID[a.ID] = a
	}
	assert.Equal(t, 1, byID["a"].MessageCount)
	assert.Equal(t, 2, byID["b"].MessageCount)
}

func TestFileBackend_FigwalGate(t *testing.T) {
	// With the gate set, new arias open as figwal-backed; on-disk
	// shape matches (aria/ dir, not aria.jsonl file).
	t.Setenv("FIGARO_USE_FIGWAL", "1")
	dir := t.TempDir()
	b, err := NewFileBackend(dir)
	require.NoError(t, err)
	s, err := b.Open("aw")
	require.NoError(t, err)
	_, err = s.Append(Entry[message.Message]{
		Payload: message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("hi")}},
	})
	require.NoError(t, err)
	require.NoError(t, s.Close())

	// figwal layout on disk: arias/aw/aria/ dir, no aria.jsonl file.
	st, err := os.Stat(filepath.Join(dir, "aw", "aria"))
	require.NoError(t, err)
	assert.True(t, st.IsDir())
	_, err = os.Stat(filepath.Join(dir, "aw", "aria.jsonl"))
	assert.True(t, os.IsNotExist(err))
}

func TestFileBackend_DefaultIsLegacy(t *testing.T) {
	// Default (env unset): new arias use the legacy NDJSON FileLog.
	t.Setenv("FIGARO_USE_FIGWAL", "")
	dir := t.TempDir()
	b, err := NewFileBackend(dir)
	require.NoError(t, err)
	s, err := b.Open("legacy")
	require.NoError(t, err)
	_, err = s.Append(Entry[message.Message]{
		Payload: message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("hi")}},
	})
	require.NoError(t, err)
	require.NoError(t, s.Close())

	_, err = os.Stat(filepath.Join(dir, "legacy", "aria.jsonl"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "legacy", "aria"))
	assert.True(t, os.IsNotExist(err))
}

func TestFileBackend_OnDiskEvidenceWins(t *testing.T) {
	// If the figwal dir already exists, Open ignores the env and uses
	// figwal so we don't fork into a parallel format mid-aria.
	t.Setenv("FIGARO_USE_FIGWAL", "")
	dir := t.TempDir()
	b, err := NewFileBackend(dir)
	require.NoError(t, err)

	// Seed an aria with a figwal-backed log.
	t.Setenv("FIGARO_USE_FIGWAL", "1")
	s, err := b.Open("mixed")
	require.NoError(t, err)
	_, err = s.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleUser}})
	require.NoError(t, err)
	require.NoError(t, s.Close())

	// Now unset the gate and reopen; should still pick figwal because
	// the dir exists.
	t.Setenv("FIGARO_USE_FIGWAL", "")
	s2, err := b.Open("mixed")
	require.NoError(t, err)
	defer s2.Close()
	assert.Len(t, s2.Read(), 1, "figwal-backed entry visible after gate flip")
	// Type assertion would couple this test to internal types; instead
	// confirm no legacy file ever appeared.
	_, err = os.Stat(filepath.Join(dir, "mixed", "aria.jsonl"))
	assert.True(t, os.IsNotExist(err))
}

func TestFileBackend_Remove(t *testing.T) {
	dir := t.TempDir()
	b, _ := NewFileBackend(dir)

	s, _ := b.Open("doomed")
	_, _ = s.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleUser}})

	require.NoError(t, b.Remove("doomed"))
	_, err := os.Stat(filepath.Join(dir, "doomed"))
	assert.True(t, os.IsNotExist(err))

	// Idempotent.
	assert.NoError(t, b.Remove("doomed"))
	assert.NoError(t, b.Remove("ghost"))
}
