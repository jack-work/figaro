package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/config"
)

// troupe carries what exists today: signing a human in to a troupe's door,
// and listing the troupes this CLI knows. Verbs for enrollment arrive with
// enrollment, not before.
func TestTroupeCarriesLoginAndLsOnly(t *testing.T) {
	cmd, ok := buildRouter("figaro", nil).Command("troupe")
	require.True(t, ok)
	require.Contains(t, cmd.Usage, "login")
	require.Contains(t, cmd.Usage, "ls")
	for _, absent := range []string{"join", "create", "approve", "invite", "leave"} {
		require.NotContains(t, cmd.Usage, absent)
	}
	require.True(t, terminalSubverbs["troupe login"], ":troupe login reads the keyboard")
}

func troupeRig(t *testing.T, files map[string]string) *config.Loaded {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FIGARO_TROUPE", "")
	loaded, err := config.Load(dir)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(loaded.TroupesDir(), 0o700))
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(loaded.TroupesDir(), name+".toml"), []byte(body), 0o600))
	}
	return loaded
}

const oidcTroupe = "url = \"wss://fig.example\"\n[auth]\nscheme = \"oidc-device\"\nissuer = \"https://auth.example\"\nclient_id = \"figaro-cli\"\n"

func TestTroupeRowsSaySignedInFromHush(t *testing.T) {
	loaded := troupeRig(t, map[string]string{
		"spain":  oidcTroupe,
		"home":   "url = \"unix:///run/figaro/angelus.sock\"\n",
		"broken": "url = \"wss://x\"\n[auth]\nscheme = \"saml\"\n",
		"mesh":   oidcTroupe,
	})
	t.Setenv("FIGARO_TROUPE", "spain")
	rows := troupeRows(loaded, &fakeOAuth{names: []string{"troupe:spain", "anthropic"}})

	byName := map[string]troupeRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	require.Equal(t, []string{"broken", "home", "mesh", "spain"}, []string{rows[0].Name, rows[1].Name, rows[2].Name, rows[3].Name})
	require.Equal(t, troupeSignedIn, byName["spain"].SignIn)
	require.True(t, byName["spain"].Selected)
	require.Equal(t, troupeSignedOut, byName["mesh"].SignIn)
	require.Equal(t, troupeNoAuth, byName["home"].SignIn)
	require.Equal(t, "wss://fig.example", byName["spain"].URL)
	require.Contains(t, byName["broken"].Error, "auth.scheme")
}

func TestTroupeLoginRefusesATroupeWithoutAuth(t *testing.T) {
	loaded := troupeRig(t, map[string]string{"home": "url = \"unix:///run/figaro/angelus.sock\"\n"})
	_, err := troupeForLogin(loaded, "home")
	require.ErrorContains(t, err, "no [auth]")
	_, err = troupeForLogin(loaded, "nowhere")
	require.ErrorContains(t, err, "no troupe")
}
