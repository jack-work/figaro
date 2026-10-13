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

func TestLayersApplyInOrderNearestWins(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "review", "[system]\nmodel = \"review-model\"\ncredo = \"review\"\n")
	writeOutfit(t, dir, "opus", "[system]\nmodel = \"opus-model\"\nmax_tokens = 4096\n")
	writeOutfit(t, dir, "top", "layers = [\"review\", \"opus\"]\n[system]\nthinking_effort = \"high\"\n")

	patch, err := outfit.New(dir).Load("top")
	require.NoError(t, err)
	assert.Equal(t, `"opus-model"`, string(mustEntry(patch, "system.model")))
	assert.Equal(t, `"review"`, string(mustEntry(patch, "system.credo")))
	assert.Equal(t, `4096`, string(mustEntry(patch, "system.max_tokens")))
	assert.Equal(t, `"high"`, string(mustEntry(patch, "system.thinking_effort")))
}

func TestLayersComposeRecursively(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "house", "[system]\nmodel = \"house\"\ncredo = \"house-credo\"\n")
	writeOutfit(t, dir, "review", "layers = [\"house\"]\n[system]\nmodel = \"review\"\n")
	writeOutfit(t, dir, "top", "layers = [\"review\"]\n")

	patch, err := outfit.New(dir).Load("top")
	require.NoError(t, err)
	assert.Equal(t, `"review"`, string(mustEntry(patch, "system.model")))
	assert.Equal(t, `"house-credo"`, string(mustEntry(patch, "system.credo")))
}

func TestSharedLayerAppliesAtEachPosition(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "house", "[system]\nmodel = \"house\"\n")
	writeOutfit(t, dir, "left", "layers = [\"house\"]\n[system]\ncredo = \"left\"\n")
	writeOutfit(t, dir, "right", "layers = [\"house\"]\n")
	writeOutfit(t, dir, "top", "layers = [\"left\", \"right\"]\n")

	patch, err := outfit.New(dir).Load("top")
	require.NoError(t, err)
	assert.Equal(t, `"house"`, string(mustEntry(patch, "system.model")))
	assert.Equal(t, `"left"`, string(mustEntry(patch, "system.credo")))
}

func TestMissingLayerIsAnErrorCarryingTheClosure(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "top", "layers = [\"present\", \"absent\"]\n[system]\nprovider = \"anthropic\"\n")
	writeOutfit(t, dir, "present", "[system]\nmodel = \"m\"\n")

	_, err := outfit.New(dir).Load("top")
	var missing *outfit.MissingError
	require.True(t, errors.As(err, &missing), "want MissingError, got %v", err)
	assert.Equal(t, []string{"absent"}, missing.Missing)
	assert.False(t, missing.RootOnly)

	top := missing.Closure
	assert.Equal(t, "top", top.Name)
	assert.True(t, top.Found)
	require.Len(t, top.Layers, 2)
	assert.True(t, top.Layers[0].Found)
	assert.False(t, top.Layers[1].Found)
	assert.Equal(t, "absent", top.Layers[1].Name)
}

func TestMissingOutfitIsOptionalAbsenceButLoadReportsIt(t *testing.T) {
	dir := t.TempDir()
	o := outfit.New(dir)

	patch, err := o.LoadOptional("nope")
	require.NoError(t, err)
	assert.True(t, patch.IsIdentity())

	_, err = o.Load("nope")
	var missing *outfit.MissingError
	require.True(t, errors.As(err, &missing), "want MissingError, got %v", err)
	assert.True(t, missing.RootOnly)
	assert.Equal(t, []string{"nope"}, missing.Missing)
}

func TestNamesOrderLikeLayers(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "a", "[system]\nmodel = \"a\"\ncredo = \"a\"\n")
	writeOutfit(t, dir, "b", "[system]\nmodel = \"b\"\n")

	patch, err := outfit.New(dir).Names("a", "b")
	require.NoError(t, err)
	assert.Equal(t, `"b"`, string(mustEntry(patch, "system.model")))
	assert.Equal(t, `"a"`, string(mustEntry(patch, "system.credo")))

	writeOutfit(t, dir, "equivalent", "layers = [\"a\", \"b\"]\n")
	viaLayers, err := outfit.New(dir).Load("equivalent")
	require.NoError(t, err)
	assert.Equal(t, patch.Entries(), viaLayers.Entries(), "a,b must fold exactly as layers = [a, b]")
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

// `source` was the single-parent spelling; flattening it into a key would be silent.
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
	assert.Equal(t, `"inline-model"`, string(mustEntry(patch, "system.model")))
	assert.Equal(t, `"base"`, string(mustEntry(patch, "system.credo")))
	assert.Equal(t, `"1h"`, string(mustEntry(patch, "ttl")))
	_, hasLayers := patch.Entry("layers")
	assert.False(t, hasLayers)

	// `layers` in a patch is data; only an outfit file's `layers` resolves.
	literal, err := outfit.ParseSet(`{"layers":["base"],"ttl":"2h"}`)
	require.NoError(t, err)
	patch, err = o.Dress(nil, literal, "")
	require.NoError(t, err)
	_, hz := patch.Entry("system.model")
	assert.False(t, hz)
	assert.Equal(t, `["base"]`, string(mustEntry(patch, "layers")))

	_, err = o.Dress([]string{"nope"}, form.Patch{}, "")
	var missing *outfit.MissingError
	require.True(t, errors.As(err, &missing), "want MissingError, got %v", err)

	patch, err = o.Dress([]string{"default"}, form.Patch{}, "")
	require.NoError(t, err)
	assert.True(t, patch.IsIdentity())

	patch, err = o.Dress([]string{"default"}, form.Patch{}, "base")
	require.NoError(t, err)
	assert.Equal(t, `"base-model"`, string(mustEntry(patch, "system.model")))
}

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

func TestLayerNamesObeyTheNameGrammar(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "sneaky", "layers = [\"../../secrets\"]\n")
	_, err := outfit.New(dir).Load("sneaky")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot contain")
}

// A fileName/dirName reference in a client patch must never become a read inside the daemon.
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
			for _, ent := range patch.Entries() {
				if ent.IsRemoval() {
					continue
				}
				k, v := ent.Key, ent.New
				assert.NotContains(t, string(v), "PRIVATE KEY MATERIAL", k)
			}
			if err == nil {
				assert.Empty(t, patch.Entries())
			}
		})
	}
}

// A symlink inside the config dir pointing out survives a naive prefix test.
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
	assert.Empty(t, patch.Entries())
}
