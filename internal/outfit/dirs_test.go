package outfit_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/outfit"
)

func writeNested(t *testing.T, dir, rel, body string) {
	t.Helper()
	path := filepath.Join(dir, "outfits", rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

// Gluck, 2026-10-14: outfit files may be organized into directories; the
// name is still the basename, so -O, layers and default_outfit are unchanged.
func TestANestedOutfitIsFoundByItsName(t *testing.T) {
	dir := t.TempDir()
	writeNested(t, dir, "decide/jev.toml", "[system.decide]\nprovider = \"rou2\"\nmodel = \"explabs/jev-latest\"\n")
	writeNested(t, dir, "models/deep/opus.toml", "[system]\nmodel = \"opus\"\n")
	writeOutfit(t, dir, "top", "layers = [\"opus\", \"jev\"]\n")
	o := outfit.New(dir)

	patch, err := o.Load("jev")
	require.NoError(t, err)
	assert.Equal(t, `"explabs/jev-latest"`, string(mustEntry(patch, "system.decide.model")))

	patch, err = o.Load("top")
	require.NoError(t, err, "a top-level outfit layers nested ones by name")
	assert.Equal(t, `"opus"`, string(mustEntry(patch, "system.model")))
	assert.Equal(t, `"rou2"`, string(mustEntry(patch, "system.decide.provider")))
}

func TestTwoFilesWithOneNameAreRefusedNamingBoth(t *testing.T) {
	dir := t.TempDir()
	writeNested(t, dir, "jev.toml", "[system]\nmodel = \"a\"\n")
	writeNested(t, dir, "old/jev.toml", "[system]\nmodel = \"b\"\n")
	_, err := outfit.New(dir).Load("jev")
	require.Error(t, err, "a silent pick between two files is the bug this refuses")
	assert.Contains(t, err.Error(), filepath.Join("outfits", "jev.toml"))
	assert.Contains(t, err.Error(), filepath.Join("outfits", "old", "jev.toml"))
}

// The resolver records what it walked, so a nested outfit created after a
// miss is found by the next ask, as a top-level one always was.
func TestANestedOutfitCreatedAfterAMissIsFound(t *testing.T) {
	dir := t.TempDir()
	writeNested(t, dir, "decide/luna.toml", "[system]\nmodel = \"luna\"\n")
	o := outfit.New(dir)
	o.SetStaleWindow(0)

	_, err := o.Load("jev")
	require.Error(t, err)

	writeNested(t, dir, "decide/jev.toml", "[system]\nmodel = \"jev\"\n")
	patch, err := o.Load("jev")
	require.NoError(t, err, "a file added to an already-walked directory must turn the epoch over")
	assert.Equal(t, `"jev"`, string(mustEntry(patch, "system.model")))

	writeNested(t, dir, "brand/new/deep.toml", "[system]\nmodel = \"deep\"\n")
	patch, err = o.Load("deep")
	require.NoError(t, err, "so must a file in a directory that did not exist")
	assert.Equal(t, `"deep"`, string(mustEntry(patch, "system.model")))
}

func TestAMovedOutfitIsFoundAtItsNewPath(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "jev", "[system]\nmodel = \"jev\"\n")
	o := outfit.New(dir)
	o.SetStaleWindow(0)
	_, err := o.Load("jev")
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "outfits", "decide"), 0o700))
	require.NoError(t, os.Rename(filepath.Join(dir, "outfits", "jev.toml"), filepath.Join(dir, "outfits", "decide", "jev.toml")))
	patch, err := o.Load("jev")
	require.NoError(t, err, "organizing a file into a directory must not break its name")
	assert.Equal(t, `"jev"`, string(mustEntry(patch, "system.model")))
}

// The index of outfit files is built once per epoch, but a name it does not
// hold is looked for again before it is reported missing. A file written a
// moment ago (first-run writes the default outfit, then creates an aria on
// it) must be found inside the same epoch, as it was when every lookup was a
// stat of outfits/<name>.toml.
func TestAFileWrittenMidEpochIsFoundByItsFirstLookup(t *testing.T) {
	dir := t.TempDir()
	writeNested(t, dir, "base.toml", "[system]\nmodel = \"base\"\n")
	o := outfit.New(dir)
	o.SetStaleWindow(time.Hour)
	_, err := o.Load("base")
	require.NoError(t, err)

	writeNested(t, dir, "decide/fresh.toml", "[system]\nmodel = \"fresh\"\n")
	patch, err := o.Load("fresh")
	require.NoError(t, err, "a miss in the epoch's index must look again before saying missing")
	assert.Equal(t, `"fresh"`, string(mustEntry(patch, "system.model")))
}
