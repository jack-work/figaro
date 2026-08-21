package form_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
)

func TestState_OpenMissing_EmptySnapshot(t *testing.T) {
	dir := t.TempDir()
	s, err := form.Open(filepath.Join(dir, "the form channel"))
	require.NoError(t, err)
	defer s.Close()
	assert.Equal(t, 0, s.Snapshot().Len())
}

func TestState_ApplyAndSave_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "the form channel") // ensure mkdir works

	s, err := form.Open(path)
	require.NoError(t, err)

	patch := form.Patch{Set: map[string]json.RawMessage{
		"system.credo": json.RawMessage(`"you are figaro"`),
		"cwd":          json.RawMessage(`"/home/figaro"`),
	}}
	post := s.Apply(patch)
	assert.Equal(t, json.RawMessage(`"/home/figaro"`), val(post, "cwd"))

	require.NoError(t, s.Save())

	// Reopen and verify persistence.
	s2, err := form.Open(path)
	require.NoError(t, err)
	defer s2.Close()
	snap := s2.Snapshot()
	assert.Equal(t, json.RawMessage(`"you are figaro"`), val(snap, "system.credo"))
	assert.Equal(t, json.RawMessage(`"/home/figaro"`), val(snap, "cwd"))
}

func TestState_Snapshot_ReturnsClone(t *testing.T) {
	s, err := form.Open(filepath.Join(t.TempDir(), "x.json"))
	require.NoError(t, err)
	defer s.Close()

	s.Apply(form.Patch{Set: map[string]json.RawMessage{"k": json.RawMessage(`"v"`)}})
	snap1 := s.Snapshot()
	// Derive a mutated snapshot from the clone.
	snap1 = snap1.Apply(form.Patch{Set: map[string]json.RawMessage{"k": json.RawMessage(`"mutated"`)}})
	_ = snap1

	snap2 := s.Snapshot()
	assert.Equal(t, json.RawMessage(`"v"`), val(snap2, "k"), "State's internal snapshot must not be affected by clone mutations")
}

func TestState_Save_NotDirty_NoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "the form channel")
	s, err := form.Open(path)
	require.NoError(t, err)
	defer s.Close()

	require.NoError(t, s.Save()) // not dirty; no file should exist
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "Save when clean should not create the file")
}

func TestState_Apply_EmptyPatch_NoMark(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "the form channel")
	s, err := form.Open(path)
	require.NoError(t, err)
	defer s.Close()

	s.Apply(form.Patch{}) // empty
	require.NoError(t, s.Save())
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "empty patch must not mark dirty")
}

func TestState_RemovePatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "the form channel")
	s, err := form.Open(path)
	require.NoError(t, err)

	s.Apply(form.Patch{Set: map[string]json.RawMessage{"k": json.RawMessage(`"v"`)}})
	require.NoError(t, s.Save())
	require.NoError(t, s.Close())

	s2, err := form.Open(path)
	require.NoError(t, err)
	defer s2.Close()
	s2.Apply(form.Patch{Remove: []string{"k"}})
	snap := s2.Snapshot()
	assert.False(t, snap.Has("k"))
	require.NoError(t, s2.Save())
}

func TestState_Close_FlushesPending(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "the form channel")
	s, err := form.Open(path)
	require.NoError(t, err)

	s.Apply(form.Patch{Set: map[string]json.RawMessage{"k": json.RawMessage(`"v"`)}})
	require.NoError(t, s.Close()) // should flush

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, data)
}
