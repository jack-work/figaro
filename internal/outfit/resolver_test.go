package outfit_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/outfit"
)

// The snapshot's whole reason for existing: a resolution must not straddle an
// edit. The file is rewritten AFTER the epoch has pinned it, and the fold —
// even one rebuilt from scratch after eviction — still answers with the bytes
// that were there when the epoch began.
func TestSnapshotPinsBytesForTheEpoch(t *testing.T) {
	dir := t.TempDir()
	snap := t.TempDir()
	writeOutfit(t, dir, "base", "[system]\nmodel = \"pinned\"\n")
	o := outfit.NewAt(dir, snap)
	o.SetStaleWindow(time.Hour) // one epoch for the whole test

	patch, err := o.Load("base")
	require.NoError(t, err)
	require.Equal(t, `"pinned"`, string(patch.Set["system.model"]))

	// The user saves the file mid-resolution. Nothing in this epoch may see it.
	writeOutfit(t, dir, "base", "[system]\nmodel = \"changed-underneath\"\n")

	// Evict the materialized fold, forcing a rebuild from the snapshot rather
	// than from the live file.
	o.Forget("base")
	patch, err = o.Load("base")
	require.NoError(t, err)
	assert.Equal(t, `"pinned"`, string(patch.Set["system.model"]),
		"a rebuild inside an epoch must come from the snapshot, not the live file")

	// A new epoch is where the edit belongs.
	o.Reload()
	patch, err = o.Load("base")
	require.NoError(t, err)
	assert.Equal(t, `"changed-underneath"`, string(patch.Set["system.model"]))

	// And the bytes are still on disk, under their own hash: the receipt.
	entries, err := os.ReadDir(snap)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(entries), 2, "both versions should be snapshotted")
}

// A cycle is detected lazily, named, and then TAINTED: every name on the loop
// answers from the verdict instead of walking the graph again.
func TestCycleIsTaintedAndNotRewalked(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "a", "layers = [\"b\"]\n[system]\na = 1\n")
	writeOutfit(t, dir, "b", "layers = [\"c\"]\n[system]\nb = 1\n")
	writeOutfit(t, dir, "c", "layers = [\"a\"]\n[system]\nc = 1\n")
	o := outfit.NewAt(dir, t.TempDir())
	o.SetStaleWindow(time.Hour)

	_, err := o.Load("a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cycle")

	// Every name on the loop is tainted, not just the one that was asked for.
	for _, name := range []string{"a", "b", "c"} {
		_, err := o.Load(name)
		require.Error(t, err, "%s sits on the cycle", name)
		assert.Contains(t, err.Error(), "cycle")
	}

	// A fresh epoch clears the verdict — the loop may have been cut.
	writeOutfit(t, dir, "c", "[system]\nc = 1\n")
	o.Reload()
	patch, err := o.Load("a")
	require.NoError(t, err)
	assert.Equal(t, "1", string(patch.Set["system.c"]))
}

// A fold that never had to be walked is the point of the epoch: the second ask
// costs no syscall at all. Proven by deleting the file out from under a pinned
// epoch — a stat-per-dependency cache would notice and refold; this one must
// answer from the epoch it already proved.
func TestEpochAnswersWithoutTouchingDisk(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "base", "[system]\nmodel = \"m\"\n")
	o := outfit.NewAt(dir, t.TempDir())
	o.SetStaleWindow(time.Hour)

	_, err := o.Load("base")
	require.NoError(t, err)

	require.NoError(t, os.Remove(filepath.Join(dir, "outfits", "base.toml")))
	patch, err := o.Load("base")
	require.NoError(t, err, "the epoch already answered this")
	assert.Equal(t, `"m"`, string(patch.Set["system.model"]))

	o.Reload()
	_, err = o.Load("base")
	assert.Error(t, err, "and the next epoch tells the truth")
}

// Warming is background work with no error surface and no blocking: the only
// thing a caller can observe is that the fold is there afterwards.
func TestWarmFoldsInTheBackground(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "housed", "[system]\nmodel = \"m\"\n")
	o := outfit.NewAt(dir, t.TempDir())
	o.Warm("housed")
	require.Eventually(t, func() bool { return o.CachedFolds() > 0 }, 2*time.Second, 5*time.Millisecond)

	o.Warm("no-such-outfit-anywhere") // must not panic, must not block
	o.Warm("")
}

// One resolver serves every aria in the daemon. Fold the same enormous-ish
// closure from many goroutines under -race and prove every answer agrees.
func TestConcurrentDressAgrees(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, "skills")
	require.NoError(t, os.MkdirAll(skills, 0o755))
	for i := 0; i < 20; i++ {
		body := strings.Repeat("x", 1024)
		require.NoError(t, os.WriteFile(filepath.Join(skills, "s"+string(rune('a'+i))+".md"),
			[]byte("---\nname: s\n---\n"+body), 0o600))
	}
	writeOutfit(t, dir, "house", "skills = { dirName = \"skills\" }\n[system]\ncache_control = \"5m\"\n")
	writeOutfit(t, dir, "terse", "layers = [\"house\"]\n[system]\nverbosity = \"low\"\n")
	writeOutfit(t, dir, "thorough", "layers = [\"house\"]\n[system]\neffort = \"high\"\n")
	writeOutfit(t, dir, "full", "layers = [\"terse\", \"thorough\"]\n[system]\nmodel = \"m\"\n")
	o := outfit.NewAt(dir, t.TempDir())

	want, err := o.Dress([]string{"full"}, form.Patch{}, "")
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := o.Dress([]string{"full"}, form.Patch{}, "")
			assert.NoError(t, err)
			assert.Equal(t, len(want.Set), len(got.Set))
			for k, v := range want.Set {
				assert.Equal(t, string(v), string(got.Set[k]), k)
			}
		}()
	}
	wg.Wait()
}
