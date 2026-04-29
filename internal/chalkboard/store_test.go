package chalkboard_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
)

// writeFileTrunc is shared with the lint/template tests — kept here
// because tests in this package use it.
func writeFileTrunc(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

func TestStore_AppendThenSnapshot(t *testing.T) {
	dir := t.TempDir()
	store, err := chalkboard.NewFileStore(dir)
	require.NoError(t, err)
	defer store.Close()

	patch1 := chalkboard.Patch{Set: map[string]json.RawMessage{
		"cwd":   json.RawMessage(`"/foo"`),
		"model": json.RawMessage(`"claude-opus"`),
	}}
	require.NoError(t, store.Append("aria-1", 1, patch1))

	snap, err := store.Snapshot("aria-1")
	require.NoError(t, err)
	assert.Equal(t, json.RawMessage(`"/foo"`), snap["cwd"])
	assert.Equal(t, json.RawMessage(`"claude-opus"`), snap["model"])
}

func TestStore_AppendMultiple_ThenReplay(t *testing.T) {
	dir := t.TempDir()
	store, err := chalkboard.NewFileStore(dir)
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.Append("a", 1, chalkboard.Patch{Set: map[string]json.RawMessage{
		"k": json.RawMessage(`"v1"`),
	}}))
	require.NoError(t, store.Append("a", 2, chalkboard.Patch{Set: map[string]json.RawMessage{
		"k": json.RawMessage(`"v2"`),
	}}))
	require.NoError(t, store.Append("a", 3, chalkboard.Patch{Remove: []string{"k"}}))

	snap, err := store.Snapshot("a")
	require.NoError(t, err)
	_, has := snap["k"]
	assert.False(t, has, "remove patch must clear the key during replay")
}

func TestStore_SaveSnapshot_FastPath(t *testing.T) {
	dir := t.TempDir()
	store, err := chalkboard.NewFileStore(dir)
	require.NoError(t, err)
	defer store.Close()

	// Append, then save snapshot. Subsequent Snapshot reads the cache,
	// not the log.
	require.NoError(t, store.Append("a", 1, chalkboard.Patch{Set: map[string]json.RawMessage{
		"k": json.RawMessage(`"v"`),
	}}))
	require.NoError(t, store.SaveSnapshot("a", chalkboard.Snapshot{
		"k": json.RawMessage(`"v"`),
		"x": json.RawMessage(`"y"`), // not in log -> only readable from snapshot
	}))

	snap, err := store.Snapshot("a")
	require.NoError(t, err)
	assert.Equal(t, json.RawMessage(`"y"`), snap["x"], "snapshot file should be the source of truth when present")
}

func TestStore_Snapshot_MissingAria(t *testing.T) {
	dir := t.TempDir()
	store, err := chalkboard.NewFileStore(dir)
	require.NoError(t, err)
	defer store.Close()

	snap, err := store.Snapshot("never-existed")
	require.NoError(t, err)
	assert.Empty(t, snap, "missing aria → empty snapshot, no error")
}

func TestStore_EmptyPatch_NotPersisted(t *testing.T) {
	dir := t.TempDir()
	store, err := chalkboard.NewFileStore(dir)
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.Append("a", 1, chalkboard.Patch{}))

	// Log file should not exist.
	_, err = os.Stat(filepath.Join(dir, "a", "log.json"))
	assert.True(t, os.IsNotExist(err), "empty patch must not create the log file")
}

func TestStore_AtomicSnapshot_NoTmpLeft(t *testing.T) {
	dir := t.TempDir()
	store, err := chalkboard.NewFileStore(dir)
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.SaveSnapshot("a", chalkboard.Snapshot{
		"k": json.RawMessage(`"v"`),
	}))

	// No .tmp file should remain after the rename.
	tmp := filepath.Join(dir, "a", "snapshot.json.tmp")
	_, err = os.Stat(tmp)
	assert.True(t, os.IsNotExist(err), "snapshot.json.tmp must be cleaned up by rename")
}

func TestStore_LogReplayOrdering_OutOfOrderEntries(t *testing.T) {
	dir := t.TempDir()
	// Hand-write a log with entries out of logical-time order.
	ariaDir := filepath.Join(dir, "a")
	require.NoError(t, os.MkdirAll(ariaDir, 0o700))
	logPath := filepath.Join(ariaDir, "log.json")
	body := []byte(`{"lt":2,"patch":{"set":{"k":"v2"}}}` + "\n" +
		`{"lt":1,"patch":{"set":{"k":"v1"}}}` + "\n")
	require.NoError(t, os.WriteFile(logPath, body, 0o600))

	store, err := chalkboard.NewFileStore(dir)
	require.NoError(t, err)
	defer store.Close()

	snap, err := store.Snapshot("a")
	require.NoError(t, err)
	// After sorting by lt, the lt=2 entry wins, so k=v2.
	assert.Equal(t, json.RawMessage(`"v2"`), snap["k"], "replay must apply patches in lt order regardless of file order")
}
