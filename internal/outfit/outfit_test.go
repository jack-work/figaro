package outfit_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/outfit"
)

func TestLoad_FlattensInlineTables(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "outfits"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "outfits", "config.toml"), []byte(`
system = { model = "claude-x", max_tokens = 1024 }
friendly_name = "Figaro"
user_id = 7
`), 0600))

	patch, err := outfit.New(dir).Load("config")
	require.NoError(t, err)
	assert.Equal(t, `"claude-x"`, string(mustEntry(patch, "system.model")))
	assert.Equal(t, `1024`, string(mustEntry(patch, "system.max_tokens")))
	assert.Equal(t, `"Figaro"`, string(mustEntry(patch, "friendly_name")))
	assert.Equal(t, `7`, string(mustEntry(patch, "user_id")))
}

func TestLoad_FileName_NoFrontmatter_StoresContent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "credo.md"), []byte("# Credo\nbody"), 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "outfits"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "outfits", "config.toml"), []byte(`
system = { credo = { fileName = "credo.md" } }
`), 0600))

	patch, err := outfit.New(dir).Load("config")
	require.NoError(t, err)
	var env outfit.ContentEnvelope
	require.NoError(t, json.Unmarshal(mustEntry(patch, "system.credo"), &env))
	assert.Equal(t, "# Credo\nbody", env.Content)
	assert.Empty(t, env.Frontmatter)
	assert.Equal(t, filepath.Join(dir, "credo.md"), env.FilePath)
}

func TestLoad_FileName_WithFrontmatter_StripsBody(t *testing.T) {
	dir := t.TempDir()
	contents := "---\nname: foo\ndescription: a foo\n---\nthe body goes here\nand keeps going"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "foo.md"), []byte(contents), 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "outfits"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "outfits", "config.toml"), []byte(`
foo = { fileName = "foo.md" }
`), 0600))

	patch, err := outfit.New(dir).Load("config")
	require.NoError(t, err)
	var env outfit.ContentEnvelope
	require.NoError(t, json.Unmarshal(mustEntry(patch, "foo"), &env))
	assert.Equal(t, "name: foo\ndescription: a foo", env.Frontmatter)
	assert.Empty(t, env.Content)
	assert.Equal(t, filepath.Join(dir, "foo.md"), env.FilePath)
}

func TestLoad_DirName(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "go.md"), []byte("go body"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "bravo.md"),
		[]byte("---\nname: bravo\ndescription: B\n---\nbravo body"), 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "outfits"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "outfits", "config.toml"), []byte(`
skills = { dirName = "skills" }
`), 0600))

	patch, err := outfit.New(dir).Load("config")
	require.NoError(t, err)

	for _, want := range []string{"skills.go", "skills.bravo"} {
		if _, ok := patch.Entry(want); ok {
			return
		}
	}
	t.Fatal("dirName must fan entries out as skills.<base>")

	var goEnv outfit.ContentEnvelope
	require.NoError(t, json.Unmarshal(mustEntry(patch, "skills.go"), &goEnv))
	assert.Equal(t, "go body", goEnv.Content)
	assert.Empty(t, goEnv.Frontmatter)
	assert.Equal(t, filepath.Join(dir, "skills", "go.md"), goEnv.FilePath)

	var bravoEnv outfit.ContentEnvelope
	require.NoError(t, json.Unmarshal(mustEntry(patch, "skills.bravo"), &bravoEnv))
	assert.Equal(t, "name: bravo\ndescription: B", bravoEnv.Frontmatter)
	assert.Empty(t, bravoEnv.Content)
	assert.Equal(t, filepath.Join(dir, "skills", "bravo.md"), bravoEnv.FilePath)
}

func TestLoad_EmptyNameReturnsEmptyPatch(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "outfits"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "outfits", "config.toml"), []byte(`x = 1`), 0600))

	patch, err := outfit.New(dir).Load("")
	require.NoError(t, err)
	assert.True(t, patch.IsIdentity(), "empty outfit name must yield empty patch")
}

// Bundled skills win by name over the user's copy.
func TestLoad_DirSkill_AndBundledMerge(t *testing.T) {
	bundled := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(bundled, "skills", "figaro"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(bundled, "skills", "figaro", "SKILL.md"),
		[]byte("---\nname: figaro\ndescription: bundled\n---\nbody"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(bundled, "skills", "figaro", "architecture.md"),
		[]byte("arch"), 0600)) // a section, NOT surfaced as its own skill
	require.NoError(t, os.WriteFile(filepath.Join(bundled, "skills", "shared.md"),
		[]byte("bundled shared"), 0600))
	t.Setenv("FIGARO_BUNDLED_SKILLS", bundled)

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "mine.md"), []byte("user mine"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "shared.md"), []byte("user shared"), 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "outfits"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "outfits", "config.toml"),
		[]byte("skills = { dirName = \"skills\" }\n"), 0600))

	patch, err := outfit.New(dir).Load("config")
	require.NoError(t, err)

	var fig outfit.ContentEnvelope
	require.NoError(t, json.Unmarshal(mustEntry(patch, "skills.figaro"), &fig))
	assert.Equal(t, "name: figaro\ndescription: bundled", fig.Frontmatter)
	assert.Equal(t, filepath.Join(bundled, "skills", "figaro", "SKILL.md"), fig.FilePath)
	_, hasArch := patch.Entry("skills.architecture")
	assert.False(t, hasArch, "section files must not surface as their own skills")

	var mine, shared outfit.ContentEnvelope
	require.NoError(t, json.Unmarshal(mustEntry(patch, "skills.mine"), &mine))
	assert.Equal(t, "user mine", mine.Content)
	require.NoError(t, json.Unmarshal(mustEntry(patch, "skills.shared"), &shared))
	assert.Equal(t, "bundled shared", shared.Content, "a bundled skill overrides a config copy by name")
}

func TestLoad_DirSkill_UserSymlinkAndLowercaseManifest(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target-skill")
	require.NoError(t, os.MkdirAll(target, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(target, "skill.md"),
		[]byte("---\nname: linked\ndescription: linked folder skill\n---\nbody"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(target, "reference.md"),
		[]byte("supplemental"), 0600))

	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	require.NoError(t, os.MkdirAll(skillsDir, 0700))
	link := filepath.Join(skillsDir, "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "outfits"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "outfits", "config.toml"),
		[]byte("skills = { dirName = \"skills\" }\n"), 0600))

	patch, err := outfit.New(dir).Load("config")
	require.NoError(t, err)
	var linked outfit.ContentEnvelope
	require.NoError(t, json.Unmarshal(mustEntry(patch, "skills.linked"), &linked))
	assert.Equal(t, "name: linked\ndescription: linked folder skill", linked.Frontmatter)
	assert.Equal(t, strings.ToLower(filepath.Join(link, "skill.md")), strings.ToLower(linked.FilePath))
	_, supplementalSurfaced := patch.Entry("skills.reference")
	assert.False(t, supplementalSurfaced)
}

// An outfit that layered one with a credo file and set credo = "..." of its
// own lost its string: the layer's credo.content and credo.filePath and the
// string at credo itself went into one build, which kept the branch, so
// `figaro state outfit` reported no changes. A key an outfit sets replaces
// whatever an earlier layer, or an earlier outfit in the list, put at that
// key or under it; the caller's own keys replace the outfits' the same way.
func TestALaterCredoStringReplacesALayersCredoFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "credo.md"), []byte("---\nname: house\n---\nhouse credo"), 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "outfits"), 0700))
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "outfits", name+".toml"), []byte(body), 0600))
	}
	write("house", "[system]\ncredo = { fileName = \"credo.md\" }\nmodel = \"m\"\n")
	write("plain", "[system]\ncredo = \"a plain credo\"\n")
	write("layered", "layers = [\"house\"]\n[system]\ncredo = \"a plain credo\"\n")
	write("onfile", "layers = [\"plain\"]\n[system]\ncredo = { fileName = \"credo.md\" }\n")
	o := outfit.New(dir)
	credo := func(names []string, patch form.Patch) string {
		t.Helper()
		p, err := o.Dress(names, patch, "")
		require.NoError(t, err)
		raw, ok := form.Snapshot{}.Apply(p).Get("system.credo")
		require.True(t, ok, "%v: no system.credo", names)
		return string(raw)
	}
	assert.Equal(t, `"a plain credo"`, credo([]string{"layered"}, form.Patch{}))
	assert.Equal(t, `"a plain credo"`, credo([]string{"house", "plain"}, form.Patch{}))
	caller := form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.credo": json.RawMessage(`"from -S"`)}, nil)
	assert.Equal(t, `"from -S"`, credo([]string{"house"}, caller))

	var env outfit.ContentEnvelope
	require.NoError(t, json.Unmarshal([]byte(credo([]string{"onfile"}, form.Patch{})), &env))
	assert.Equal(t, "name: house", env.Frontmatter)

	p, err := o.Dress([]string{"layered"}, form.Patch{}, "")
	require.NoError(t, err)
	model, _ := form.Snapshot{}.Apply(p).Get("system.model")
	assert.Equal(t, `"m"`, string(model), "a sibling the layer set survives")
}
