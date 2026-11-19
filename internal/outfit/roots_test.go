package outfit_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/outfit"
)

func writeRootFile(t *testing.T, root, rel, body string) string {
	t.Helper()
	path := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

func layeredRoots(managed, user string) config.OutfitRoots {
	return config.OutfitRoots{Dirs: []config.OutfitRoot{{Path: managed, Managed: true}, {Path: user}}}
}

func layeredOutfitter(t *testing.T) (o *outfit.Outfitter, managed, user string) {
	t.Helper()
	managed, user = t.TempDir(), t.TempDir()
	o = outfit.New(t.TempDir())
	o.SetStaleWindow(0)
	o.SetRoots(layeredRoots(managed, user))
	return o, managed, user
}

func TestTheUserRootShadowsOneLayerOfAManagedOutfit(t *testing.T) {
	o, managed, user := layeredOutfitter(t)
	writeRootFile(t, managed, "role.toml", "layers = [\"model\", \"voice\"]\n[system]\nrole = \"managed\"\n")
	writeRootFile(t, managed, "model/model.toml", "[system]\nmodel = \"managed-model\"\n")
	writeRootFile(t, managed, "voice.toml", "[system]\nvoice = \"managed-voice\"\n")
	writeRootFile(t, user, "model.toml", "[system]\nmodel = \"user-model\"\n")

	patch, err := o.Load("role")
	require.NoError(t, err)
	assert.Equal(t, `"user-model"`, string(mustEntry(patch, "system.model")), "the last root wins a name, also as a layer")
	assert.Equal(t, `"managed-voice"`, string(mustEntry(patch, "system.voice")))
	assert.Equal(t, `"managed"`, string(mustEntry(patch, "system.role")))
}

func TestOutfitFilesKeepsTheWinnerAndWhatItShadowed(t *testing.T) {
	managed, user := t.TempDir(), t.TempDir()
	shadowed := writeRootFile(t, managed, "model.toml", "")
	only := writeRootFile(t, managed, "voice.toml", "")
	winner := writeRootFile(t, user, "sub/model.toml", "")

	idx, err := config.OutfitFiles(layeredRoots(managed, user), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"model", "voice"}, idx.Names())
	require.NotNil(t, idx["model"])
	assert.Equal(t, []string{winner}, idx["model"].Paths)
	assert.Equal(t, user, idx["model"].Root.Path)
	assert.False(t, idx["model"].Root.Managed)
	assert.Equal(t, []config.OutfitSource{{Path: shadowed, Root: managed, Managed: true}}, idx["model"].Shadowed)
	assert.Equal(t, []string{only}, idx["voice"].Paths)
	assert.True(t, idx["voice"].Root.Managed)
	assert.Empty(t, idx["voice"].Shadowed)
}

func TestADuplicateInsideOneRootIsStillAmbiguous(t *testing.T) {
	o, managed, user := layeredOutfitter(t)
	writeRootFile(t, managed, "model.toml", "")
	writeRootFile(t, user, "a/jev.toml", "")
	writeRootFile(t, user, "b/jev.toml", "")
	_, err := o.Load("jev")
	require.ErrorContains(t, err, "ambiguous")
	_, err = o.Load("model")
	require.NoError(t, err)
}

func TestARootThatDoesNotExistIsNoOutfits(t *testing.T) {
	o, managed, _ := layeredOutfitter(t)
	require.NoError(t, os.RemoveAll(managed))
	_, err := o.Load("anything")
	var missing *outfit.MissingError
	require.ErrorAs(t, err, &missing)
}
