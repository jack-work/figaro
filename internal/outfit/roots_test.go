package outfit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestALockedNameRefusesTheOverlayAndKeepsTheManagedOne(t *testing.T) {
	o, managed, user := layeredOutfitter(t)
	writeRootFile(t, managed, "seat.toml", "[system]\nmodel = \"managed-model\"\n")
	writeRootFile(t, managed, "free.toml", "[system]\nmodel = \"managed-free\"\n")
	writeRootFile(t, managed, ".locked", "# the charter's seats\nseat\n")
	writeRootFile(t, user, "seat.toml", "[system]\nmodel = \"user-model\"\n")
	writeRootFile(t, user, "free.toml", "[system]\nmodel = \"user-free\"\n")

	patch, err := o.Load("seat")
	require.NoError(t, err)
	assert.Equal(t, `"managed-model"`, string(mustEntry(patch, "system.model")), "an overlay of a locked name must not apply")
	patch, err = o.Load("free")
	require.NoError(t, err)
	assert.Equal(t, `"user-free"`, string(mustEntry(patch, "system.model")), "the lock covers only the names it lists")
}

func TestALockedNameTheManagedRootLacksIsStillRefused(t *testing.T) {
	o, managed, user := layeredOutfitter(t)
	writeRootFile(t, managed, ".locked", "reserved\n")
	writeRootFile(t, user, "reserved.toml", "[system]\nmodel = \"user\"\n")

	_, err := o.Load("reserved")
	var missing *outfit.MissingError
	require.ErrorAs(t, err, &missing, "a reserved name must not be filled from the overlay")
}

func TestOutfitFilesNamesEveryRefusal(t *testing.T) {
	managed, user := t.TempDir(), t.TempDir()
	kept := writeRootFile(t, managed, "seat.toml", "")
	lock := writeRootFile(t, managed, ".locked", "seat other\n")
	refused := writeRootFile(t, user, "nested/seat.toml", "")
	writeRootFile(t, user, ".locked", "mine\n")
	writeRootFile(t, user, "mine.toml", "")

	idx, err := config.OutfitFiles(layeredRoots(managed, user), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{kept}, idx["seat"].Paths)
	assert.Equal(t, []config.OutfitRefusal{{Name: "seat", OutfitSource: config.OutfitSource{Path: refused, Root: user}, LockedBy: lock}}, idx.Refusals())
	assert.Equal(t, []string{"mine", "seat"}, idx.Names(), "a .locked in the overlay locks nothing, and a lock alone is not an outfit")
}

func TestALockIsReadOnRefresh(t *testing.T) {
	o, managed, user := layeredOutfitter(t)
	writeRootFile(t, managed, "seat.toml", "[system]\nmodel = \"managed\"\n")
	writeRootFile(t, user, "seat.toml", "[system]\nmodel = \"user\"\n")
	writeRootFile(t, managed, ".locked", "other\n")
	patch, err := o.Load("seat")
	require.NoError(t, err)
	require.Equal(t, `"user"`, string(mustEntry(patch, "system.model")))

	writeRootFile(t, managed, ".locked", "seat\n")
	patch, err = o.Load("seat")
	require.NoError(t, err)
	assert.Equal(t, `"managed"`, string(mustEntry(patch, "system.model")), "an edit of .locked in place must turn the epoch over")
}

func storeGeneration(t *testing.T, model, locked string) string {
	t.Helper()
	gen := t.TempDir()
	writeRootFile(t, gen, "seat.toml", "[system]\nmodel = \""+model+"\"\n")
	writeRootFile(t, gen, ".locked", locked)
	epoch := time.Unix(1, 0)
	for _, f := range []string{"seat.toml", ".locked", "."} {
		require.NoError(t, os.Chtimes(filepath.Join(gen, f), epoch, epoch))
	}
	return gen
}

func swapLink(t *testing.T, link, target string) {
	t.Helper()
	tmp := link + ".new"
	require.NoError(t, os.Symlink(target, tmp))
	require.NoError(t, os.Rename(tmp, link))
}

func TestARebuildSwappingTheManagedLinkIsSeen(t *testing.T) {
	base := t.TempDir()
	link := filepath.Join(base, "managed")
	user := filepath.Join(base, "user")
	require.NoError(t, os.MkdirAll(user, 0o755))
	writeRootFile(t, user, "seat.toml", "[system]\nmodel = \"user\"\n")
	swapLink(t, link, storeGeneration(t, "gen-a", "seat\n"))

	o := outfit.New(t.TempDir())
	o.SetStaleWindow(0)
	o.SetRoots(layeredRoots(link, user))
	patch, err := o.Load("seat")
	require.NoError(t, err)
	require.Equal(t, `"gen-a"`, string(mustEntry(patch, "system.model")), "a symlinked managed root must be walked")
	assert.Equal(t, filepath.Join(link, "seat.toml"), o.Resolve("seat").Path, "paths read through the root as configured")

	swapLink(t, link, storeGeneration(t, "gen-b", "seat\n"))
	patch, err = o.Load("seat")
	require.NoError(t, err)
	assert.Equal(t, `"gen-b"`, string(mustEntry(patch, "system.model")), "a swapped link with identical stat must turn the epoch over")

	swapLink(t, link, storeGeneration(t, "gen-c", "\n"))
	patch, err = o.Load("seat")
	require.NoError(t, err)
	assert.Equal(t, `"user"`, string(mustEntry(patch, "system.model")), "a generation dropping the lock must free the name")
}

func TestANameARebuildAddsBehindTheLinkIsSeen(t *testing.T) {
	base := t.TempDir()
	link := filepath.Join(base, "managed")
	genA := storeGeneration(t, "gen-a", "")
	require.NoError(t, os.Remove(filepath.Join(genA, ".locked")))
	swapLink(t, link, genA)
	o := outfit.New(t.TempDir())
	o.SetStaleWindow(0)
	o.SetRoots(layeredRoots(link, filepath.Join(base, "user")))
	_, err := o.Load("extra")
	var missing *outfit.MissingError
	require.ErrorAs(t, err, &missing)

	gen := storeGeneration(t, "gen-b", "")
	require.NoError(t, os.Remove(filepath.Join(gen, ".locked")))
	writeRootFile(t, gen, "extra.toml", "[system]\nmodel = \"extra\"\n")
	epoch := time.Unix(1, 0)
	for _, f := range []string{"extra.toml", "."} {
		require.NoError(t, os.Chtimes(filepath.Join(gen, f), epoch, epoch))
	}
	swapLink(t, link, gen)
	patch, err := o.Load("extra")
	require.NoError(t, err, "the link itself is a dependency, not only the directory it pointed at")
	assert.Equal(t, `"extra"`, string(mustEntry(patch, "system.model")))
}

func TestALockedOutfitFoldsItsLayersFromManagedRootsOnly(t *testing.T) {
	o, managed, user := layeredOutfitter(t)
	writeRootFile(t, managed, "seat.toml", "layers = [\"helper\"]\n[system]\nprovider = \"mock\"\n")
	writeRootFile(t, managed, "helper.toml", "[system]\nmodel = \"managed-helper\"\n")
	writeRootFile(t, managed, ".locked", "seat\n")
	writeRootFile(t, managed, "free.toml", "layers = [\"helper\"]\n")
	overlay := writeRootFile(t, user, "helper.toml", "[system]\nmodel = \"user-helper\"\n[system.grants]\noutfits = [\"*\"]\n")

	patch, err := o.Load("seat")
	require.NoError(t, err)
	assert.Equal(t, `"managed-helper"`, string(mustEntry(patch, "system.model")), "a locked outfit's layer must come from the managed roots")
	_, granted := patch.Entry("system.grants.outfits")
	assert.False(t, granted, "an overlay of a locked outfit's layer must not grant outfits")

	patch, err = o.Load("free")
	require.NoError(t, err)
	assert.Equal(t, `"user-helper"`, string(mustEntry(patch, "system.model")), "an unlocked outfit still takes the overlay")

	seat := o.Resolve("seat")
	require.Len(t, seat.Layers, 1)
	assert.Equal(t, filepath.Join(managed, "helper.toml"), seat.Layers[0].Path)
	assert.Equal(t, []config.OutfitSource{{Path: overlay, Root: user}}, seat.Layers[0].Overlaid)
	assert.Equal(t, []outfit.LockedOverlay{{Locked: "seat", Layer: "helper", OutfitSource: config.OutfitSource{Path: overlay, Root: user}}},
		o.LockedLayerOverlays([]string{"seat"}))
}

func TestALockedOutfitsTransitiveLayersStayManaged(t *testing.T) {
	o, managed, user := layeredOutfitter(t)
	writeRootFile(t, managed, "seat.toml", "layers = [\"mid\"]\n")
	writeRootFile(t, managed, "mid.toml", "layers = [\"leaf\"]\n")
	writeRootFile(t, managed, "leaf.toml", "[system]\nmodel = \"managed-leaf\"\n")
	writeRootFile(t, managed, ".locked", "seat\n")
	writeRootFile(t, user, "leaf.toml", "[system]\nmodel = \"user-leaf\"\n")
	writeRootFile(t, user, "mine.toml", "layers = [\"leaf\"]\n")

	patch, err := o.Load("mine")
	require.NoError(t, err)
	assert.Equal(t, `"user-leaf"`, string(mustEntry(patch, "system.model")))
	patch, err = o.Load("seat")
	require.NoError(t, err)
	assert.Equal(t, `"managed-leaf"`, string(mustEntry(patch, "system.model")), "the closure is managed at every depth, and a fold cached for the overlay must not leak in")
}

func TestAnUnknownNameInsideTheStaleWindowDoesNotHideALinkSwap(t *testing.T) {
	base := t.TempDir()
	link := filepath.Join(base, "managed")
	user := filepath.Join(base, "user")
	require.NoError(t, os.MkdirAll(user, 0o755))
	swapLink(t, link, storeGeneration(t, "gen-a", "seat\n"))

	o := outfit.New(t.TempDir())
	o.SetRoots(layeredRoots(link, user))
	_, err := o.Load("extra")
	var missing *outfit.MissingError
	require.ErrorAs(t, err, &missing)

	gen := storeGeneration(t, "gen-b", "seat\n")
	writeRootFile(t, gen, "extra.toml", "[system]\nmodel = \"extra\"\n")
	epoch := time.Unix(1, 0)
	for _, f := range []string{"extra.toml", "."} {
		require.NoError(t, os.Chtimes(filepath.Join(gen, f), epoch, epoch))
	}
	swapLink(t, link, gen)
	_, err = o.Load("unknown")
	require.ErrorAs(t, err, &missing)

	time.Sleep(150 * time.Millisecond)
	patch, err := o.Load("extra")
	require.NoError(t, err, "a walk inside the stale window must not overwrite the baseline that sees the swap")
	assert.Equal(t, `"extra"`, string(mustEntry(patch, "system.model")))
}

func TestASecondReadInsideTheStaleWindowKeepsTheEpochsBaseline(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "one", "skills = { dirName = \"skills\" }\n")
	writeOutfit(t, dir, "two", "skills = { dirName = \"skills\" }\n")
	writeRootFile(t, dir, "skills/a.md", "a")
	o := outfit.New(dir)

	_, err := o.Load("one")
	require.NoError(t, err)
	writeRootFile(t, dir, "skills/b.md", "b")
	_, err = o.Load("two")
	require.NoError(t, err)

	time.Sleep(150 * time.Millisecond)
	patch, err := o.Load("one")
	require.NoError(t, err)
	hasB := false
	for _, e := range patch.Entries() {
		if strings.HasPrefix(e.Key, "skills.b") {
			hasB = true
		}
	}
	assert.True(t, hasB, "a re-read of a path must not replace the baseline that sees the change")
}

func TestAMissWhenNothingMovedDoesNotRollTheEpoch(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "seat", "[system]\nmodel = \"seat\"\n")
	epoch := time.Unix(1, 0)
	for _, p := range []string{"outfits/seat.toml", "outfits", "."} {
		require.NoError(t, os.Chtimes(filepath.Join(dir, p), epoch, epoch))
	}
	o := outfit.New(dir)
	o.SetStaleWindow(time.Hour)
	_, err := o.Load("seat")
	require.NoError(t, err)
	gen := o.Generation()

	_, err = o.Load("absent")
	var missing *outfit.MissingError
	require.ErrorAs(t, err, &missing)
	assert.Equal(t, gen, o.Generation(), "a miss with nothing moved keeps the epoch and its folds")

	writeOutfit(t, dir, "late", "[system]\nmodel = \"late\"\n")
	_, err = o.Load("late")
	require.NoError(t, err, "a miss after the disk moved rolls the epoch and walks once more")
	assert.Greater(t, o.Generation(), gen)
}

func TestTheTreeNamesARefusalOnALockedNameWithNoManagedFile(t *testing.T) {
	o, managed, user := layeredOutfitter(t)
	writeRootFile(t, managed, ".locked", "reserved\n")
	refused := writeRootFile(t, user, "reserved.toml", "[system]\nmodel = \"user\"\n")

	c := o.Resolve("reserved")
	assert.False(t, c.Found)
	assert.Equal(t, []config.OutfitSource{{Path: refused, Root: user}}, c.Refused, "a not-found closure must still say what the lock refused")
}
