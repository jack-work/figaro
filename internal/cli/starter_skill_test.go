package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteStarterLoadoutCreatesFolderSkill(t *testing.T) {
	root := t.TempDir()
	loadout := filepath.Join(root, "loadouts", "default.toml")
	require.NoError(t, writeStarterLoadout(loadout, "copilot", "gpt-test"))

	skillPath := filepath.Join(root, "skills", "howto", "SKILL.md")
	body, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	require.Equal(t, starterHowToSkill, string(body))
	_, err = os.Stat(filepath.Join(root, "skills", "howto.md"))
	require.True(t, os.IsNotExist(err))
}

func TestWriteStarterLoadoutPreservesExistingSkill(t *testing.T) {
	for _, name := range []string{"legacy-file", "lowercase-folder"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			switch name {
			case "legacy-file":
				require.NoError(t, os.MkdirAll(filepath.Join(root, "skills"), 0o700))
				require.NoError(t, os.WriteFile(
					filepath.Join(root, "skills", "howto.md"),
					[]byte("custom"),
					0o600,
				))
			case "lowercase-folder":
				require.NoError(t, os.MkdirAll(filepath.Join(root, "skills", "howto"), 0o700))
				require.NoError(t, os.WriteFile(
					filepath.Join(root, "skills", "howto", "skill.md"),
					[]byte("custom"),
					0o600,
				))
			}

			loadout := filepath.Join(root, "loadouts", "default.toml")
			require.NoError(t, writeStarterLoadout(loadout, "copilot", "gpt-test"))
			switch name {
			case "legacy-file":
				_, err := os.Stat(filepath.Join(root, "skills", "howto"))
				require.True(t, os.IsNotExist(err))
			case "lowercase-folder":
				body, err := os.ReadFile(filepath.Join(root, "skills", "howto", "skill.md"))
				require.NoError(t, err)
				require.Equal(t, "custom", string(body))
			}
		})
	}
}

func TestWriteStarterLoadoutRepairsEmptyFolder(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "skills", "howto"), 0o700))
	loadout := filepath.Join(root, "loadouts", "default.toml")
	require.NoError(t, writeStarterLoadout(loadout, "copilot", "gpt-test"))
	_, err := os.Stat(filepath.Join(root, "skills", "howto", "SKILL.md"))
	require.NoError(t, err)
}

func TestWriteStarterLoadoutRejectsEmptySymlinkFolder(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	require.NoError(t, os.MkdirAll(target, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "skills"), 0o700))
	if err := os.Symlink(target, filepath.Join(root, "skills", "howto")); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	loadout := filepath.Join(root, "loadouts", "default.toml")
	err := writeStarterLoadout(loadout, "copilot", "gpt-test")
	require.ErrorContains(t, err, "symlink has no SKILL.md")
}
