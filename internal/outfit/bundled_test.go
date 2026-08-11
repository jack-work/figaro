package outfit

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	figaro "github.com/jack-work/figaro"
)

// The bundle is a promise about WHICH skills ship. `figaro` is the one an
// agent cannot look up if it does not already have it; everything else in
// skills/ is a skill a user chooses. If a second directory ever shows up
// here, an install just got heavier and nobody decided to make it so.
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

	// The chapter the credo points an agent at when it forks a worker.
	_, err = figaro.Skills.ReadFile("skills/figaro/subagents.md")
	require.NoError(t, err)
}

func TestUnpackBundledSkills(t *testing.T) {
	dir := t.TempDir()

	root, err := unpackBundledSkills(dir)
	require.NoError(t, err)
	require.Equal(t, dir, filepath.Dir(root), "the root is a hash-named child of the parent")

	// dirName = "skills" must resolve under this root, exactly as it does
	// for a user's config dir.
	skill := filepath.Join(root, "skills", "figaro", "SKILL.md")
	onDisk, err := os.ReadFile(skill)
	require.NoError(t, err)
	embedded, err := figaro.Skills.ReadFile("skills/figaro/SKILL.md")
	require.NoError(t, err)
	require.Equal(t, string(embedded), string(onDisk))

	// Idempotent: same hash, same path, no rewrite.
	again, err := unpackBundledSkills(dir)
	require.NoError(t, err)
	require.Equal(t, root, again)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "a second unpack must not leave a staging directory behind")
}

// A process killed mid-unpack leaves a root with no stamp. The next run must
// treat that as incomplete and rebuild it, not read it as whole: a truncated
// skill tree is the failure this whole staging dance exists to prevent.
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

	// config.toml's bundled_skills = false.
	SetBundledSkills(false)
	require.Empty(t, bundledSkillsRoot())

	// The env var outranks the config either way.
	SetBundledSkills(true)
	t.Setenv("FIGARO_BUNDLED_SKILLS", "/somewhere/else")
	require.Equal(t, "/somewhere/else", bundledSkillsRoot())
	t.Setenv("FIGARO_BUNDLED_SKILLS", "off")
	require.Empty(t, bundledSkillsRoot())
}

// The whole chain, with the real embedded bundle and no FIGARO_BUNDLED_SKILLS
// pointing at a fixture: a `dirName = "skills"` outfit must come back holding
// the figaro skill, read from the unpacked root, outranking a user copy of the
// same name. This is the test that fails if the embed, the unpack and the
// loader stop agreeing about where the skills are.
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

	raw, ok := patch.Set["skills.figaro"]
	require.True(t, ok, "the bundled figaro skill must reach the form")
	var env ContentEnvelope
	require.NoError(t, json.Unmarshal(raw, &env))
	require.Contains(t, env.Frontmatter, "name: figaro")
	require.Equal(t, filepath.Join(BundledSkillsRoot(), "skills", "figaro", "SKILL.md"), env.FilePath)
	require.Empty(t, env.Content, "frontmatter only: the body is read from filePath when the model wants it")
}
