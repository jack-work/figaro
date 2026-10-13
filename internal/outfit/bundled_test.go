package outfit

import (
	"encoding/json"
	"github.com/jack-work/figaro/api/form"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	figaro "github.com/jack-work/figaro"
)

func TestEmbeddedBundleIsFigaroSkillOnly(t *testing.T) {
	entries, err := fs.ReadDir(figaro.Skills, "skills")
	require.NoError(t, err)
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	require.Equal(t, []string{"figaro"}, names)

	body, err := figaro.Skills.ReadFile("skills/figaro/SKILL.md")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(body), "---\nname: figaro\n"),
		"the bundled skill must carry frontmatter, or the whole file lands in every form")

	_, err = figaro.Skills.ReadFile("skills/figaro/subagents.md")
	require.NoError(t, err)
}

func TestUnpackBundledSkills(t *testing.T) {
	dir := t.TempDir()

	root, err := unpackBundledSkills(dir)
	require.NoError(t, err)
	require.Equal(t, dir, filepath.Dir(root), "the root is a hash-named child of the parent")

	skill := filepath.Join(root, "skills", "figaro", "SKILL.md")
	onDisk, err := os.ReadFile(skill)
	require.NoError(t, err)
	embedded, err := figaro.Skills.ReadFile("skills/figaro/SKILL.md")
	require.NoError(t, err)
	require.Equal(t, string(embedded), string(onDisk))

	again, err := unpackBundledSkills(dir)
	require.NoError(t, err)
	require.Equal(t, root, again)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "a second unpack must not leave a staging directory behind")
}

// A root without a stamp is an interrupted unpack and must be rebuilt.
func TestUnpackRepairsUnstampedRoot(t *testing.T) {
	dir := t.TempDir()
	root, err := unpackBundledSkills(dir)
	require.NoError(t, err)

	require.NoError(t, os.Remove(filepath.Join(root, ".stamp")))
	require.NoError(t, os.RemoveAll(filepath.Join(root, "skills", "figaro", "reference")))

	repaired, err := unpackBundledSkills(dir)
	require.NoError(t, err)
	require.Equal(t, root, repaired)
	_, err = os.Stat(filepath.Join(root, ".stamp"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(root, "skills", "figaro", "reference"))
	require.NoError(t, err, "the missing chapters must come back")
}

func TestBundledSkillsSwitches(t *testing.T) {
	t.Setenv("FIGARO_STATE_DIR", t.TempDir())
	t.Cleanup(func() { SetBundledSkills(true) })

	SetBundledSkills(true)
	root := bundledSkillsRoot()
	require.NotEmpty(t, root)
	_, err := os.Stat(filepath.Join(root, "skills", "figaro", "SKILL.md"))
	require.NoError(t, err)

	SetBundledSkills(false)
	require.Empty(t, bundledSkillsRoot())

	SetBundledSkills(true)
	t.Setenv("FIGARO_BUNDLED_SKILLS", "/somewhere/else")
	require.Equal(t, "/somewhere/else", bundledSkillsRoot())
	t.Setenv("FIGARO_BUNDLED_SKILLS", "off")
	require.Empty(t, bundledSkillsRoot())
}

// Real embedded bundle, no FIGARO_BUNDLED_SKILLS: embed, unpack and loader must agree.
func TestFoldUsesTheEmbeddedBundle(t *testing.T) {
	t.Setenv("FIGARO_STATE_DIR", t.TempDir())
	t.Setenv("FIGARO_BUNDLED_SKILLS", "")
	require.NoError(t, os.Unsetenv("FIGARO_BUNDLED_SKILLS"))
	SetBundledSkills(true)
	t.Cleanup(func() { SetBundledSkills(true) })
	bundledMu.Lock()
	bundledKnown, bundledRoot = false, ""
	bundledMu.Unlock()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "figaro.md"),
		[]byte("a stale copy of the figaro skill"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "outfits"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "outfits", "config.toml"),
		[]byte("skills = { dirName = \"skills\" }\n"), 0o600))

	patch, err := New(dir).Load("config")
	require.NoError(t, err)

	ent, ok := patch.Entry("skills.figaro")
	raw := ent.New
	require.True(t, ok, "the bundled figaro skill must reach the form")
	var env ContentEnvelope
	require.NoError(t, json.Unmarshal(raw, &env))
	require.Contains(t, env.Frontmatter, "name: figaro")
	require.Equal(t, filepath.Join(BundledSkillsRoot(), "skills", "figaro", "SKILL.md"), env.FilePath)
	require.Empty(t, env.Content, "frontmatter only: the body is read from filePath when the model wants it")
}

func mustEntry(p form.Patch, key string) json.RawMessage {
	e, ok := p.Entry(key)
	if !ok {
		return nil
	}
	return e.New
}
