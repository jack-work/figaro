package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
)

func setModelsKey(t *testing.T, f *Form, key string, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	snap, _ := f.Snapshot()
	_, _, err = f.ApplyEffect(form.Build(snap, map[string]json.RawMessage{key: raw}, nil), 0)
	require.NoError(t, err)
}

func modelsChannelBytes(t *testing.T, root string) (int64, int) {
	t.Helper()
	dir := filepath.Join(root, chanForm, modelsStump)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var size int64
	segs := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		info, err := e.Info()
		require.NoError(t, err)
		size += info.Size()
		segs++
	}
	return size, segs
}

// The catalog is a store form now: a restart must give back the same entries
// at the same version, or a client mirror holding that version would be told
// it is current when it is not.
func TestModelsFormKeepsEntriesAndVersionAcrossARestart(t *testing.T) {
	root := t.TempDir()
	be, err := NewXwalBackend(root, 0)
	require.NoError(t, err)
	f, err := be.ModelsForm()
	require.NoError(t, err)
	setModelsKey(t, f, "openai.models.gpt-4.1.context.default", 1_047_576)
	setModelsKey(t, f, "openai.refreshed_at", 1)
	want, version := f.Snapshot()
	require.NotZero(t, version)
	require.NoError(t, be.Close())

	be, err = NewXwalBackend(root, 0)
	require.NoError(t, err)
	defer be.Close()
	f, err = be.ModelsForm()
	require.NoError(t, err)
	got, gotVersion := f.Snapshot()
	require.Equal(t, version, gotVersion)
	require.True(t, got.Diff(want).IsIdentity(), "entries changed across the restart")
}

// A poll restamps the catalog forever; the form must not grow with it. Old
// segments go once the active one's watermark carries their state.
func TestModelsFormStaysBoundedUnderRepeatedPolls(t *testing.T) {
	root := t.TempDir()
	be, err := NewXwalBackend(root, 2048)
	require.NoError(t, err)
	f, err := be.ModelsForm()
	require.NoError(t, err)
	setModelsKey(t, f, "openai.models.gpt-4.1.context.default", 1_047_576)
	_, start := f.Snapshot()

	const polls = 2000
	var peak int64
	for i := 1; i <= polls; i++ {
		setModelsKey(t, f, "openai.refreshed_at", i)
		if i%100 == 0 {
			size, _ := modelsChannelBytes(t, root)
			peak = max(peak, size)
		}
	}
	require.Less(t, peak, int64(3*2048), "the channel grew past a sealed and an active segment")
	_, segs := modelsChannelBytes(t, root)
	require.LessOrEqual(t, segs, 2)

	want, version := f.Snapshot()
	require.Equal(t, start+polls, version)
	require.NoError(t, be.Close())

	be, err = NewXwalBackend(root, 2048)
	require.NoError(t, err)
	defer be.Close()
	f, err = be.ModelsForm()
	require.NoError(t, err)
	got, gotVersion := f.Snapshot()
	require.Equal(t, version, gotVersion)
	require.True(t, got.Diff(want).IsIdentity())
	raw, ok := got.Get("openai.refreshed_at")
	require.True(t, ok)
	require.Equal(t, fmt.Sprint(polls), string(raw))

	setModelsKey(t, f, "openai.refreshed_at", polls+1)
	_, after := f.Snapshot()
	require.Equal(t, version+1, after, "a write after the reopen continues the numbering")
}

// A reader behind the dropped segments is told to reseed, not handed a short
// history that reads as complete.
func TestModelsFormRefusesARangeBelowTheDroppedSegments(t *testing.T) {
	root := t.TempDir()
	be, err := NewXwalBackend(root, 1024)
	require.NoError(t, err)
	defer be.Close()
	f, err := be.ModelsForm()
	require.NoError(t, err)
	for i := 1; i <= 200; i++ {
		setModelsKey(t, f, "p.refreshed_at", i)
	}
	floor := f.PatchFloor()
	require.NotZero(t, floor)
	base, baseVersion := f.Base()
	require.Equal(t, floor, baseVersion)
	require.True(t, base.Has("p.refreshed_at"))
	err = f.log.RangePatches(1, 0, func(uint64, []byte) error { return nil })
	require.ErrorIs(t, err, ErrBelowFloor)
}

func TestModelsStumpIsNeverListed(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	defer be.Close()
	f, err := be.ModelsForm()
	require.NoError(t, err)
	setModelsKey(t, f, "p.refreshed_at", 1)
	for _, n := range be.Nodes() {
		require.NotEqual(t, modelsStump, n.ID)
	}
	for _, n := range be.Forms() {
		require.NotEqual(t, modelsStump, n.ID)
	}
	for _, st := range be.store.listStumps() {
		require.NotEqual(t, modelsStump, st.Name)
	}
}
