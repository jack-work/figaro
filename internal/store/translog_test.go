package store_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/store"
)

func TestFileTranslationLog_AppendAndLookup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "translations", "anthropic.jsonl")

	log, err := store.OpenFileTranslationLog(path)
	require.NoError(t, err)
	defer log.Close()

	e1, err := log.Append([]uint64{1}, []json.RawMessage{json.RawMessage(`{"role":"user"}`)}, "anthropic/tag/v1")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), e1.Alt)

	e2, err := log.Append([]uint64{2}, nil, "anthropic/tag/v1") // state-only tic, empty messages
	require.NoError(t, err)
	assert.Equal(t, uint64(2), e2.Alt)
	assert.Empty(t, e2.Messages)

	got, ok := log.Lookup(1)
	require.True(t, ok)
	assert.Equal(t, uint64(1), got.Alt)
	assert.Equal(t, "anthropic/tag/v1", got.Fingerprint)

	got2, ok := log.Lookup(2)
	require.True(t, ok)
	assert.Empty(t, got2.Messages)

	_, ok = log.Lookup(99)
	assert.False(t, ok)
}

func TestFileTranslationLog_PersistAndReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "translations", "anthropic.jsonl")

	log1, err := store.OpenFileTranslationLog(path)
	require.NoError(t, err)
	_, err = log1.Append([]uint64{1}, []json.RawMessage{json.RawMessage(`{"x":1}`)}, "fp1")
	require.NoError(t, err)
	_, err = log1.Append([]uint64{2}, []json.RawMessage{json.RawMessage(`{"x":2}`)}, "fp1")
	require.NoError(t, err)
	require.NoError(t, log1.Close())

	log2, err := store.OpenFileTranslationLog(path)
	require.NoError(t, err)
	defer log2.Close()

	all := log2.All()
	require.Len(t, all, 2)
	assert.Equal(t, uint64(1), all[0].Alt)
	assert.Equal(t, uint64(2), all[1].Alt)

	got, ok := log2.Lookup(2)
	require.True(t, ok)
	assert.Equal(t, "fp1", got.Fingerprint)
}

func TestFileTranslationLog_AltMonotonicAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "translations", "anthropic.jsonl")

	log1, err := store.OpenFileTranslationLog(path)
	require.NoError(t, err)
	_, err = log1.Append([]uint64{1}, nil, "fp")
	require.NoError(t, err)
	require.NoError(t, log1.Close())

	log2, err := store.OpenFileTranslationLog(path)
	require.NoError(t, err)
	defer log2.Close()

	e, err := log2.Append([]uint64{2}, nil, "fp")
	require.NoError(t, err)
	assert.Equal(t, uint64(2), e.Alt, "alt counter resumes from on-disk state")
}

func TestFileTranslationLog_Clear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "translations", "anthropic.jsonl")

	log, err := store.OpenFileTranslationLog(path)
	require.NoError(t, err)
	_, err = log.Append([]uint64{1}, nil, "fp1")
	require.NoError(t, err)
	require.NoError(t, log.Clear())
	assert.Empty(t, log.All())

	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "Clear must remove the on-disk file")

	// Subsequent Append starts at alt=1 again.
	e, err := log.Append([]uint64{1}, nil, "fp2")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), e.Alt)
}

func TestFileTranslationLog_OpenMissing_Empty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "translations", "anthropic.jsonl")
	log, err := store.OpenFileTranslationLog(path)
	require.NoError(t, err)
	defer log.Close()
	assert.Empty(t, log.All())
	_, ok := log.Lookup(1)
	assert.False(t, ok)
}
