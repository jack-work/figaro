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

func TestState_OpenMissing_EmptySnapshot(t *testing.T) {
	dir := t.TempDir()
	s, err := chalkboard.Open(filepath.Join(dir, "chalkboard.json"))
	require.NoError(t, err)
	defer s.Close()
	assert.Empty(t, s.Snapshot())
}

func TestState_ApplyAndSave_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "chalkboard.json") // ensure mkdir works

	s, err := chalkboard.Open(path)
	require.NoError(t, err)

	patch := chalkboard.Patch{Set: map[string]json.RawMessage{
		"system.prompt": json.RawMessage(`"you are figaro"`),
		"cwd":           json.RawMessage(`"/home/figaro"`),
	}}
	post := s.Apply(patch)
	assert.Equal(t, json.RawMessage(`"/home/figaro"`), post["cwd"])

	require.NoError(t, s.Save())

	// Reopen and verify persistence.
	s2, err := chalkboard.Open(path)
	require.NoError(t, err)
	defer s2.Close()
	snap := s2.Snapshot()
	assert.Equal(t, json.RawMessage(`"you are figaro"`), snap["system.prompt"])
	assert.Equal(t, json.RawMessage(`"/home/figaro"`), snap["cwd"])
}

func TestState_Snapshot_ReturnsClone(t *testing.T) {
	s, err := chalkboard.Open(filepath.Join(t.TempDir(), "x.json"))
	require.NoError(t, err)
	defer s.Close()

	s.Apply(chalkboard.Patch{Set: map[string]json.RawMessage{"k": json.RawMessage(`"v"`)}})
	snap1 := s.Snapshot()
	snap1["k"] = json.RawMessage(`"mutated"`) // mutate the clone

	snap2 := s.Snapshot()
	assert.Equal(t, json.RawMessage(`"v"`), snap2["k"], "State's internal snapshot must not be affected by clone mutations")
}

func TestState_Save_NotDirty_NoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chalkboard.json")
	s, err := chalkboard.Open(path)
	require.NoError(t, err)
	defer s.Close()

	require.NoError(t, s.Save()) // not dirty; no file should exist
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "Save when clean should not create the file")
}

func TestState_Apply_EmptyPatch_NoMark(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chalkboard.json")
	s, err := chalkboard.Open(path)
	require.NoError(t, err)
	defer s.Close()

	s.Apply(chalkboard.Patch{}) // empty
	require.NoError(t, s.Save())
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "empty patch must not mark dirty")
}

func TestState_RemovePatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chalkboard.json")
	s, err := chalkboard.Open(path)
	require.NoError(t, err)

	s.Apply(chalkboard.Patch{Set: map[string]json.RawMessage{"k": json.RawMessage(`"v"`)}})
	require.NoError(t, s.Save())
	require.NoError(t, s.Close())

	s2, err := chalkboard.Open(path)
	require.NoError(t, err)
	defer s2.Close()
	s2.Apply(chalkboard.Patch{Remove: []string{"k"}})
	snap := s2.Snapshot()
	_, has := snap["k"]
	assert.False(t, has)
	require.NoError(t, s2.Save())
}

func TestState_Close_FlushesPending(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chalkboard.json")
	s, err := chalkboard.Open(path)
	require.NoError(t, err)

	s.Apply(chalkboard.Patch{Set: map[string]json.RawMessage{"k": json.RawMessage(`"v"`)}})
	require.NoError(t, s.Close()) // should flush

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, data)
}
