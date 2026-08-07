package outfit_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/outfit"
)

// A cache that never goes stale is the whole point; a cache that never
// invalidates is a bug that looks like a feature until someone edits a skill.
func TestCacheSeesAnEditedOutfit(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "base", "[system]\nmodel = \"first\"\n")
	writeOutfit(t, dir, "top", "layers = [\"base\"]\n")
	o := outfit.New(dir)

	patch, err := o.Load("top")
	require.NoError(t, err)
	require.Equal(t, `"first"`, string(patch.Set["system.model"]))

	// Rewritten with a different size, so mtime granularity cannot mask it.
	writeOutfit(t, dir, "base", "[system]\nmodel = \"second-and-longer\"\n")
	patch, err = o.Load("top")
	require.NoError(t, err)
	assert.Equal(t, `"second-and-longer"`, string(patch.Set["system.model"]),
		"an edit to a LAYER must invalidate the outfit above it")
}

func TestCacheSeesAnEditedContentFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "credo.md"), []byte("first"), 0o600))
	writeOutfit(t, dir, "top", "system = { credo = { fileName = \"credo.md\" } }\n")
	o := outfit.New(dir)

	patch, err := o.Load("top")
	require.NoError(t, err)
	require.Contains(t, string(patch.Set["system.credo"]), "first")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "credo.md"), []byte("second-and-longer"), 0o600))
	patch, err = o.Load("top")
	require.NoError(t, err)
	assert.Contains(t, string(patch.Set["system.credo"]), "second-and-longer",
		"a fileName dependency must invalidate the outfit that names it")
}

// Adding or removing a skill leaves every surviving file untouched, so the
// directory's own stat is what catches it.
func TestCacheSeesSkillsAddedAndRemoved(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, "skills")
	require.NoError(t, os.MkdirAll(skills, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skills, "alpha.md"), []byte("---\nname: alpha\n---\nbody\n"), 0o600))
	writeOutfit(t, dir, "top", "skills = { dirName = \"skills\" }\n")
	o := outfit.New(dir)

	patch, err := o.Load("top")
	require.NoError(t, err)
	require.Contains(t, patch.Set, "skills.alpha")
	require.NotContains(t, patch.Set, "skills.beta")

	require.NoError(t, os.WriteFile(filepath.Join(skills, "beta.md"), []byte("---\nname: beta\n---\nbody\n"), 0o600))
	patch, err = o.Load("top")
	require.NoError(t, err)
	assert.Contains(t, patch.Set, "skills.beta", "an ADDED skill must invalidate the outfit")

	require.NoError(t, os.Remove(filepath.Join(skills, "alpha.md")))
	patch, err = o.Load("top")
	require.NoError(t, err)
	assert.NotContains(t, patch.Set, "skills.alpha", "a REMOVED skill must invalidate the outfit")
}

// A layer that did not exist and then does must be picked up: the failure is
// cached as an absence, not as a fact about the parent.
func TestCacheSeesALayerAppear(t *testing.T) {
	dir := t.TempDir()
	writeOutfit(t, dir, "top", "layers = [\"later\"]\n[system]\nmodel = \"own\"\n")
	o := outfit.New(dir)

	_, err := o.Load("top")
	require.Error(t, err)

	writeOutfit(t, dir, "later", "[system]\ncredo = \"arrived\"\n")
	patch, err := o.Load("top")
	require.NoError(t, err)
	assert.Equal(t, `"arrived"`, string(patch.Set["system.credo"]))
}

// bigConfig writes a realistic composition: a shared base, a diamond over it,
// and a skills directory of the size a real config carries.
func bigConfig(t testing.TB, skillCount, skillBytes int) string {
	t.Helper()
	dir := t.TempDir()
	skills := filepath.Join(dir, "skills")
	require.NoError(t, os.MkdirAll(skills, 0o755))
	body := strings.Repeat("lorem ipsum dolor sit amet, consectetur adipiscing elit. ", skillBytes/56+1)
	for i := 0; i < skillCount; i++ {
		text := fmt.Sprintf("---\nname: skill-%02d\ndescription: one of many\n---\n%s", i, body)
		require.NoError(t, os.WriteFile(filepath.Join(skills, fmt.Sprintf("skill-%02d.md", i)), []byte(text), 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "credo.md"), []byte("---\nname: credo\n---\n"+body), 0o600))

	write := func(name, body string) {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "outfits"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "outfits", name+".toml"), []byte(body), 0o600))
	}
	write("house", "skills = { dirName = \"skills\" }\n[system]\ncache_control = \"5m\"\ncredo = { fileName = \"credo.md\" }\n")
	write("anthropic", "[system]\nprovider = \"anthropic\"\nmodel = \"claude-sonnet-4-5\"\nmax_tokens = 32000\n")
	write("terse", "layers = [\"house\"]\n[system]\nverbosity = \"low\"\n")
	write("thorough", "layers = [\"house\"]\n[system]\nthinking_effort = \"high\"\nthinking_budget = 8192\n")
	write("review", "layers = [\"terse\", \"thorough\"]\n[system]\ntemperature = 0.2\n")
	write("full", "layers = [\"anthropic\", \"review\"]\n[system]\nparallel_tool_calls = true\n")
	return dir
}

// BenchmarkFoldCold is a fresh Outfitter every iteration: every file parsed and
// every skill read, which is what the old code did on EVERY fold.
func BenchmarkFoldCold(b *testing.B) {
	dir := bigConfig(b, 40, 3000)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := outfit.New(dir).Load("full"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFoldWarm reuses the Outfitter: the cost is a stat per dependency.
func BenchmarkFoldWarm(b *testing.B) {
	dir := bigConfig(b, 40, 3000)
	o := outfit.New(dir)
	if _, err := o.Load("full"); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := o.Load("full"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFoldWarmEightArias is the shape that made `figaro ls` slow: one fold
// per listed aria, all naming the same outfit.
func BenchmarkFoldWarmEightArias(b *testing.B) {
	dir := bigConfig(b, 40, 3000)
	o := outfit.New(dir)
	if _, err := o.Load("full"); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 8; j++ {
			if _, err := o.Load("full"); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkResolveClosure is what `figaro outfit --tree` costs: the graph only,
// no content.
func BenchmarkResolveClosure(b *testing.B) {
	dir := bigConfig(b, 40, 3000)
	o := outfit.New(dir)
	o.Resolve("full")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if c := o.Resolve("full"); c == nil {
			b.Fatal("nil closure")
		}
	}
}
