package outfit_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/outfit"
)

// A skill reaches the board as keys, not as one value holding an object.
func TestSkillsArriveStructural(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, "skills")
	require.NoError(t, os.MkdirAll(skills, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skills, "alpha.md"),
		[]byte("---\nname: alpha\ndescription: A\n---\nbody"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "credo.md"), []byte("the credo"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "outfits"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "outfits", "top.toml"),
		[]byte("skills = { dirName = \"skills\" }\n[system]\ncredo = { fileName = \"credo.md\" }\n"), 0o644))

	p, err := outfit.New(dir).Load("top")
	require.NoError(t, err)

	var keys []string
	for _, e := range p.Entries() {
		keys = append(keys, e.Key)
	}
	sort.Strings(keys)
	t.Logf("keys: %v", keys)

	for _, want := range []string{"skills.alpha.frontmatter", "skills.alpha.filePath", "system.credo.content"} {
		e, ok := p.Entry(want)
		require.True(t, ok, "%s must be its own key", want)
		var s string
		require.NoError(t, json.Unmarshal(e.New, &s), "%s must be a string, not an object", want)
	}
}
