package outfit_test

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/outfit"
)

func writeOutfit(t *testing.T, dir, name, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "outfits"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "outfits", name+".toml"), []byte(body), 0o600))
}

// Later layers win, and so does the outfit's own patch over all of them.
func TestLayersApplyInOrderNearestWins(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "review", "[system]\nmodel = \"review-model\"\ncredo = \"review\"\n")
	writeOutfit(t, dir, "opus", "[system]\nmodel = \"opus-model\"\nmax_tokens = 4096\n")
	writeOutfit(t, dir, "top", "layers = [\"review\", \"opus\"]\n[system]\nthinking_effort = \"high\"\n")

	patch, err := outfit.New(dir).Load("top")
	require.NoError(t, err)
	// opus follows review, so its model wins; review's credo has no rival.
	assert.Equal(t, `"opus-model"`, string(patch.Set["system.model"]))
	assert.Equal(t, `"review"`, string(patch.Set["system.credo"]))
	assert.Equal(t, `4096`, string(patch.Set["system.max_tokens"]))
	assert.Equal(t, `"high"`, string(patch.Set["system.thinking_effort"]))
}

// A layer's own layers are folded before it contributes, so a nested outfit
// arrives as one already-composed patch at the position it is named in.
func TestLayersComposeRecursively(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "house", "[system]\nmodel = \"house\"\ncredo = \"house-credo\"\n")
	writeOutfit(t, dir, "review", "layers = [\"house\"]\n[system]\nmodel = \"review\"\n")
	writeOutfit(t, dir, "top", "layers = [\"review\"]\n")

	patch, err := outfit.New(dir).Load("top")
	require.NoError(t, err)
	assert.Equal(t, `"review"`, string(patch.Set["system.model"]))
	assert.Equal(t, `"house-credo"`, string(patch.Set["system.credo"]))
}

// A shared ancestor must be applied at EVERY position it appears in, not
// skipped after the first: the old single-parent walker marked a name visited
// forever, which would have quietly broken "later wins" on a diamond.
func TestSharedLayerAppliesAtEachPosition(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "house", "[system]\nmodel = \"house\"\n")
	writeOutfit(t, dir, "left", "layers = [\"house\"]\n[system]\ncredo = \"left\"\n")
	writeOutfit(t, dir, "right", "layers = [\"house\"]\n")
	// left sets model via house then credo; right re-applies house, so house's
	// model must be the winner even though left was folded first.
	writeOutfit(t, dir, "top", "layers = [\"left\", \"right\"]\n")

	patch, err := outfit.New(dir).Load("top")
	require.NoError(t, err)
	assert.Equal(t, `"house"`, string(patch.Set["system.model"]))
	assert.Equal(t, `"left"`, string(patch.Set["system.credo"]))
}

// The bug this replaces: a missing layer silently discarded the whole patch,
// so a typo looked like an empty outfit and, downstream, like a missing
// provider: which sent the user to the first-run wizard.
func TestMissingLayerIsAnErrorCarryingTheClosure(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "top", "layers = [\"present\", \"absent\"]\n[system]\nprovider = \"anthropic\"\n")
	writeOutfit(t, dir, "present", "[system]\nmodel = \"m\"\n")

	_, err := outfit.New(dir).Load("top")
	var missing *outfit.MissingError
	require.True(t, errors.As(err, &missing), "want MissingError, got %v", err)
	assert.Equal(t, []string{"absent"}, missing.Missing)
	assert.False(t, missing.RootOnly)

	// The closure must describe the shape the gap was found in.
	top := missing.Closure
	assert.Equal(t, "top", top.Name)
	assert.True(t, top.Found)
	require.Len(t, top.Layers, 2)
	assert.True(t, top.Layers[0].Found)
	assert.False(t, top.Layers[1].Found)
	assert.Equal(t, "absent", top.Layers[1].Name)
}

// An outfit that does not exist is an absence for LoadOptional (the first-run
// flow rides on it) and a fault for Load.
func TestMissingOutfitIsOptionalAbsenceButLoadReportsIt(t *testing.T) {
	dir := t.TempDir()
	o := outfit.New(dir)

	patch, err := o.LoadOptional("nope")
	require.NoError(t, err)
	assert.True(t, patch.IsEmpty())

	_, err = o.Load("nope")
	var missing *outfit.MissingError
	require.True(t, errors.As(err, &missing), "want MissingError, got %v", err)
	assert.True(t, missing.RootOnly)
	assert.Equal(t, []string{"nope"}, missing.Missing)
}

// A multi-name dressing must order exactly as layers do.
func TestNamesOrderLikeLayers(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "a", "[system]\nmodel = \"a\"\ncredo = \"a\"\n")
	writeOutfit(t, dir, "b", "[system]\nmodel = \"b\"\n")

	patch, err := outfit.New(dir).Names("a", "b")
	require.NoError(t, err)
	assert.Equal(t, `"b"`, string(patch.Set["system.model"]))
	assert.Equal(t, `"a"`, string(patch.Set["system.credo"]))

	writeOutfit(t, dir, "equivalent", "layers = [\"a\", \"b\"]\n")
	viaLayers, err := outfit.New(dir).Load("equivalent")
	require.NoError(t, err)
	assert.Equal(t, patch.Set, viaLayers.Set, "a,b must fold exactly as layers = [a, b]")
}

func TestCycleInLayersIsReported(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "a", "layers = [\"b\"]\n")
	writeOutfit(t, dir, "b", "layers = [\"a\"]\n")

	_, err := outfit.New(dir).Load("a")
	var cycle *outfit.CycleError
	require.True(t, errors.As(err, &cycle), "want CycleError, got %v", err)
	assert.Equal(t, "a", cycle.At)
}

// `source` was the single-parent spelling. Ignoring it would flatten it into a
// form key named "source": the silent kind of wrong, and an array-valued
// `source` used to be dropped without a word.
func TestSourceIsRejectedRatherThanIgnored(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "base", "[system]\nmodel = \"m\"\n")
	writeOutfit(t, dir, "top", "source = \"base\"\n[system]\nown = 1\n")

	_, err := outfit.New(dir).Load("top")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "layers")
}

func TestMalformedLayersIsReported(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"a bare string", "layers = \"base\"\n", "must be an array"},
		{"a non-string element", "layers = [1]\n", "must be a string"},
		{"an empty element", "layers = [\"\"]\n", "empty name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeOutfit(t, dir, "top", tc.body)
			_, err := outfit.New(dir).Load("top")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// The patch's own keys beat the outfits named beside them, and no directive
// ever reaches a board: the two axes are parsed apart and folded in one call.
func TestDressPutsOutfitsUnderThePatch(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "base", "[system]\nmodel = \"base-model\"\ncredo = \"base\"\n")
	o := outfit.New(dir)

	names, err := outfit.ParseNames("base")
	require.NoError(t, err)
	asked, err := outfit.ParseSet(`{"system.model":"inline-model","ttl":"1h"}`)
	require.NoError(t, err)
	patch, err := o.Dress(names, asked, "")
	require.NoError(t, err)
	assert.Equal(t, `"inline-model"`, string(patch.Set["system.model"]))
	assert.Equal(t, `"base"`, string(patch.Set["system.credo"]))
	assert.Equal(t, `"1h"`, string(patch.Set["ttl"]))
	assert.NotContains(t, patch.Set, "layers")

	// `layers` written into a PATCH is data now, not a directive: it is
	// stored as typed and resolves nothing. The only place the key is
	// respected is the unmarshal of an outfit FILE.
	literal, err := outfit.ParseSet(`{"layers":["base"],"ttl":"2h"}`)
	require.NoError(t, err)
	patch, err = o.Dress(nil, literal, "")
	require.NoError(t, err)
	assert.NotContains(t, patch.Set, "system.model")
	assert.Equal(t, `["base"]`, string(patch.Set["layers"]))

	// A missing outfit is a broken reference, always.
	_, err = o.Dress([]string{"nope"}, form.Patch{}, "")
	var missing *outfit.MissingError
	require.True(t, errors.As(err, &missing), "want MissingError, got %v", err)

	// `default` is the one lenient name: unset folds nothing, so the first-run
	// flow can notice a missing provider instead of a missing file.
	patch, err = o.Dress([]string{"default"}, form.Patch{}, "")
	require.NoError(t, err)
	assert.True(t, patch.IsEmpty())

	patch, err = o.Dress([]string{"default"}, form.Patch{}, "base")
	require.NoError(t, err)
	assert.Equal(t, `"base-model"`, string(patch.Set["system.model"]))
}

// The grammar refuses the other axis's terms, naming the flag that takes them.
func TestGrammarKeepsTheAxesApart(t *testing.T) {
	_, err := outfit.ParseNames("ttl=1h")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--set")

	_, err = outfit.ParseNames(`{"a":1}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--set")

	_, err = outfit.ParseSet("sonn5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-O sonn5")

	paths, err := outfit.ParseDelete("a.b, mantra")
	require.NoError(t, err)
	assert.Equal(t, []string{"a.b", "mantra"}, paths)
}

// One Outfitter serves every aria in the daemon, and a fold reads files and
// caches. Materialize the same patch from many goroutines under -race, and
// prove every answer is identical.
func TestConcurrentFoldsAgree(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "base", "[system]\nmodel = \"base-model\"\n")
	writeOutfit(t, dir, "top", "layers = [\"base\"]\n[system]\ncredo = \"top\"\n")
	o := outfit.New(dir)
	names, err := outfit.ParseNames("top,base")
	require.NoError(t, err)
	asked, err := outfit.ParseSet(`{"ttl":"1h"}`)
	require.NoError(t, err)

	want, err := o.Dress(names, asked, "")
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := o.Dress(names, asked, "")
			assert.NoError(t, err)
			assert.Equal(t, len(want.Set), len(got.Set))
			for k, v := range want.Set {
				assert.Equal(t, string(v), string(got.Set[k]), k)
			}
		}()
	}
	wg.Wait()
}

// A layer name is resolved into the outfits directory exactly as a spec name
// is, so it must pass the same gate: `layers = ["../../secrets"]` is refused
// where it is declared, not silently opened.
func TestLayerNamesObeyTheNameGrammar(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "sneaky", "layers = [\"../../secrets\"]\n")
	_, err := outfit.New(dir).Load("sneaky")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot contain")
}

// A fileName/dirName reference is DATA, and the loader is what turns it into a
// read inside the daemon. Before the spec collapse a client could send
// `-O '{"x":{"fileName":"../../.ssh/id_ed25519"}}'` and have the contents folded
// onto its own form and rendered to a provider. That door is closed; this keeps
// it closed.
func TestLoaderRefusesPathsOutsideTheConfigDir(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "config")
	require.NoError(t, os.MkdirAll(filepath.Join(cfg, "outfits"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "secrets"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "secrets", "id_ed25519"),
		[]byte("PRIVATE KEY MATERIAL"), 0o600))

	for _, tc := range []struct{ name, body string }{
		{"fileName climbs out", "leak = { fileName = \"../secrets/id_ed25519\" }\n"},
		{"dirName climbs out", "leak = { dirName = \"../secrets\" }\n"},
		{"fileName is absolute", "leak = { fileName = \"/etc/passwd\" }\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeOutfit(t, cfg, "leaky", tc.body)
			patch, err := outfit.New(cfg).Load("leaky")
			for k, v := range patch.Set {
				assert.NotContains(t, string(v), "PRIVATE KEY MATERIAL", k)
			}
			if err == nil {
				// An absolute path is re-rooted rather than refused, so it can
				// only ever name something inside the config dir: which here
				// does not exist, and the open error is the report.
				assert.Empty(t, patch.Set)
			}
		})
	}
}

// A symlink INSIDE the config dir pointing out of it is the version of this bug
// that survives a naive prefix test.
func TestLoaderRefusesASymlinkOutOfTheConfigDir(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "config")
	require.NoError(t, os.MkdirAll(filepath.Join(cfg, "outfits"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "secrets"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "secrets", "key"), []byte("PRIVATE KEY MATERIAL"), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(root, "secrets"), filepath.Join(cfg, "escape")))

	writeOutfit(t, cfg, "leaky", "leak = { dirName = \"escape\" }\n")
	patch, err := outfit.New(cfg).Load("leaky")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolves outside")
	assert.Empty(t, patch.Set)
}
