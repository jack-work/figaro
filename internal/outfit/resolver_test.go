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

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/outfit"
)

// A file rewritten after the epoch pinned it is invisible to that epoch, even after eviction.
func TestSnapshotPinsBytesForTheEpoch(t *testing.T) {
	dir := t.TempDir()
	snap := t.TempDir()
	writeOutfit(t, dir, "base", "[system]\nmodel = \"pinned\"\n")
	o := outfit.NewAt(dir, snap)
	o.SetStaleWindow(time.Hour) // one epoch for the whole test

	patch, err := o.Load("base")
	require.NoError(t, err)
	require.Equal(t, `"pinned"`, string(mustEntry(patch, "system.model")))

	writeOutfit(t, dir, "base", "[system]\nmodel = \"changed-underneath\"\n")

	o.Forget("base")
	patch, err = o.Load("base")
	require.NoError(t, err)
	assert.Equal(t, `"pinned"`, string(mustEntry(patch, "system.model")),
		"a rebuild inside an epoch must come from the snapshot, not the live file")

	o.Reload()
	patch, err = o.Load("base")
	require.NoError(t, err)
	assert.Equal(t, `"changed-underneath"`, string(mustEntry(patch, "system.model")))

	entries, err := os.ReadDir(snap)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(entries), 2, "both versions should be snapshotted")
}

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

	for _, name := range []string{"a", "b", "c"} {
		_, err := o.Load(name)
		require.Error(t, err, "%s sits on the cycle", name)
		assert.Contains(t, err.Error(), "cycle")
	}

	writeOutfit(t, dir, "c", "[system]\nc = 1\n")
	o.Reload()
	patch, err := o.Load("a")
	require.NoError(t, err)
	assert.Equal(t, "1", string(mustEntry(patch, "system.c")))
}

// Deleting the file under a pinned epoch must go unnoticed: the second ask costs no syscall.
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
	assert.Equal(t, `"m"`, string(mustEntry(patch, "system.model")))

	o.Reload()
	_, err = o.Load("base")
	assert.Error(t, err, "and the next epoch tells the truth")
}

func TestWarmFoldsInTheBackground(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "housed", "[system]\nmodel = \"m\"\n")
	o := outfit.NewAt(dir, t.TempDir())
	o.Warm("housed")
	require.Eventually(t, func() bool { return o.CachedFolds() > 0 }, 2*time.Second, 5*time.Millisecond)

	o.Warm("no-such-outfit-anywhere") // must not panic, must not block
	o.Warm("")
}

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
			assert.Equal(t, len(want.Entries()), len(got.Entries()))
			for _, ent := range want.Entries() {
				if ent.IsRemoval() {
					continue
				}
				k, v := ent.Key, ent.New
				assert.Equal(t, string(v), string(mustEntry(got, k)), k)
			}
		}()
	}
	wg.Wait()
}
