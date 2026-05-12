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

func TestFigwalStream_EmptyStart(t *testing.T) {
	s, err := OpenFigwalStream[message.Message](t.TempDir())
	require.NoError(t, err)
	defer s.Close()
	assert.Empty(t, s.Read())
	_, ok := s.PeekTail()
	assert.False(t, ok)
}

func TestFigwalStream_AppendPersists(t *testing.T) {
	dir := t.TempDir()

	s, err := OpenFigwalStream[message.Message](dir)
	require.NoError(t, err)

	entry, err := s.Append(Entry[message.Message]{
		Payload: message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("hello")}},
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), entry.LT)
	assert.Equal(t, uint64(1), entry.FigaroLT, "canonical stream: LT == FigaroLT")
	require.NoError(t, s.Close())

	// Reload from disk and confirm the entry comes back.
	s2, err := OpenFigwalStream[message.Message](dir)
	require.NoError(t, err)
	defer s2.Close()
	d := s2.Read()
	require.Len(t, d, 1)
	assert.Equal(t, "hello", d[0].Payload.Content[0].Text)
	assert.Equal(t, uint64(1), d[0].LT)
}

func TestFigwalStream_LogicalTimeContinuity(t *testing.T) {
	dir := t.TempDir()

	s, err := OpenFigwalStream[message.Message](dir)
	require.NoError(t, err)
	for _, text := range []string{"one", "two", "three"} {
		_, err := s.Append(Entry[message.Message]{
			Payload: message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent(text)}},
		})
		require.NoError(t, err)
	}
	require.NoError(t, s.Close())

	s2, err := OpenFigwalStream[message.Message](dir)
	require.NoError(t, err)
	defer s2.Close()
	e4, err := s2.Append(Entry[message.Message]{
		Payload: message.Message{Role: message.RoleAssistant, Content: []message.Content{message.TextContent("four")}},
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(4), e4.LT, "LT continues across reopen")
}

func TestFigwalStream_Lookup(t *testing.T) {
	s, err := OpenFigwalStream[message.Message](t.TempDir())
	require.NoError(t, err)
	defer s.Close()

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

func TestFigwalStream_ScanFromEnd(t *testing.T) {
	s, err := OpenFigwalStream[message.Message](t.TempDir())
	require.NoError(t, err)
	defer s.Close()

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

	all := s.ScanFromEnd(10)
	assert.Len(t, all, 4)
}

func TestFigwalStream_Clear(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenFigwalStream[message.Message](dir)
	require.NoError(t, err)
	defer s.Close()

	_, err = s.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleUser}})
	require.NoError(t, err)
	require.NoError(t, s.Clear())
	assert.Empty(t, s.Read())

	// After Clear we can still append; LT restarts at 1.
	e, err := s.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleUser}})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), e.LT)
}

func TestFigwalStream_PeekTail(t *testing.T) {
	s, err := OpenFigwalStream[message.Message](t.TempDir())
	require.NoError(t, err)
	defer s.Close()

	for _, text := range []string{"alpha", "beta"} {
		_, err := s.Append(Entry[message.Message]{
			Payload: message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent(text)}},
		})
		require.NoError(t, err)
	}
	tail, ok := s.PeekTail()
	require.True(t, ok)
	assert.Equal(t, "beta", tail.Payload.Content[0].Text)
}

func TestFigwalStream_Translation_FK(t *testing.T) {
	s, err := OpenFigwalStream[[]json.RawMessage](t.TempDir())
	require.NoError(t, err)
	defer s.Close()

	entry, err := s.Append(Entry[[]json.RawMessage]{
		FigaroLT:    7,
		Payload:     []json.RawMessage{json.RawMessage(`{"role":"assistant"}`)},
		Fingerprint: "anth/v1",
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), entry.LT, "translator LT is stream-local")
	assert.Equal(t, uint64(7), entry.FigaroLT, "FK preserved")

	got, ok := s.Lookup(7)
	require.True(t, ok)
	assert.Equal(t, "anth/v1", got.Fingerprint)
	require.Len(t, got.Payload, 1)
}

func TestFigwalStream_OnDiskFormatIsJSONL(t *testing.T) {
	// Sanity: the figwal-backed stream writes JSONL segments, not a
	// single .jsonl file.
	dir := t.TempDir()
	s, err := OpenFigwalStream[message.Message](dir)
	require.NoError(t, err)
	_, err = s.Append(Entry[message.Message]{
		Payload: message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("on-disk")}},
	})
	require.NoError(t, err)
	require.NoError(t, s.Close())

	// At least one segment file with the .jsonl extension is present.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	found := false
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".jsonl" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected at least one .jsonl segment in %s", dir)
}
