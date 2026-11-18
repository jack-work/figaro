package outfit_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/outfit"
)

// Defends a child policy carried by an outfit: its keys are key paths, so a
// dotted or quoted TOML key inside it stays one path and is not split into
// the policy's own subtree, and a later outfit's policy replaces an earlier
// one whole.
func TestAnOutfitsChildPolicyKeepsItsKeyPaths(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "outfits"), 0700))
	for name, body := range map[string]string{
		"dotted": "[system.child_policy]\nsystem.scopes = [\"send:parent\"]\nsystem.fanout.max_children = 2\n",
		"quoted": "[system.child_policy]\n\"system.scopes\" = [\"send:parent\"]\n\"system.fanout.max_children\" = 2\n",
		"inline": "[system]\nchild_policy = { \"system.scopes\" = [\"send:parent\"], system = { fanout = { max_children = 2 } } }\n",
		"later":  "[system.child_policy]\nmantra = \"a lane\"\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "outfits", name+".toml"), []byte(body), 0600))
	}
	want := `{"system.fanout.max_children":2,"system.scopes":["send:parent"]}`
	o := outfit.New(dir)
	for _, name := range []string{"dotted", "quoted", "inline"} {
		patch, err := o.Load(name)
		require.NoError(t, err)
		got, _ := form.Snapshot{}.Apply(patch).Get(form.ChildPolicyKey)
		require.JSONEq(t, want, string(got), "outfit %s", name)
	}
	patch, err := o.Dress([]string{"dotted", "later"}, form.Patch{}, "")
	require.NoError(t, err)
	got, _ := form.Snapshot{}.Apply(patch).Get(form.ChildPolicyKey)
	require.JSONEq(t, `{"mantra":"a lane"}`, string(got))
}
