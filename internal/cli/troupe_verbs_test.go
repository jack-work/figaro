package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
)

// troupe carries what exists today: signing a human in to a troupe's door,
// listing troupes, this node's keys and joining. No approvals or invites:
// permission decides.
func TestTroupeCarriesWhatExistsOnly(t *testing.T) {
	cmd, ok := buildRouter("figaro", nil).Command("troupe")
	require.True(t, ok)
	for _, verb := range []string{"login", "ls", "join", "leave", "revoke", "create", "intermediate"} {
		require.Contains(t, cmd.Usage, verb)
	}
	for _, absent := range []string{"approve", "invite"} {
		require.NotContains(t, cmd.Usage, absent)
	}
	require.True(t, terminalSubverbs["troupe login"], ":troupe login reads the keyboard")
	require.True(t, terminalSubverbs["troupe join"], ":troupe join asks before trusting a root")
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

// troupe ls shows this node's membership version and, per peer, the version
// it last showed and whether that drifts from ours.
func TestTroupeLsShowsPeerVersionsAndDrift(t *testing.T) {
	var buf strings.Builder
	writeTroupeMembers(&buf, &rpc.TroupeMembersResponse{
		Node:    "11111111-0000-4000-8000-000000000001",
		Version: 7,
		Peers: []rpc.TroupeMember{
			{Node: "agrees", Endpoint: "tls://10.0.0.2:7443", Version: 7, Met: true},
			{Node: "behind", Endpoint: "tls://10.0.0.3:7443", Version: 5, Met: true, Drift: true},
			{Node: "unmet", Endpoint: "tls://10.0.0.4:7443"},
		},
	})
	out := buf.String()
	require.Contains(t, out, "version 7")
	lines := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		for _, n := range []string{"agrees", "behind", "unmet"} {
			if strings.Contains(l, n) {
				lines[n] = l
			}
		}
	}
	require.Regexp(t, `agrees\s+tls://10\.0\.0\.2:7443\s+7\s*$`, lines["agrees"])
	require.Regexp(t, `behind\s+tls://10\.0\.0\.3:7443\s+5\s+drift: ours is 7`, lines["behind"])
	require.Regexp(t, `unmet\s+tls://10\.0\.0\.4:7443\s+-\s+not met`, lines["unmet"])
}

// troupe ls shows when this node's certificate and intermediate end, and
// the keeper's error verbatim when the keys are not usable.
func TestTroupeLsShowsKeyExpiry(t *testing.T) {
	var buf strings.Builder
	writeTroupeMembers(&buf, &rpc.TroupeMembersResponse{Node: "n", Peers: []rpc.TroupeMember{}, Keys: &rpc.TroupeKeys{
		Troupe: "estate", NodeExpires: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC).UnixMilli(),
		IntermediateExpires: time.Date(2027, 10, 18, 0, 0, 0, 0, time.UTC).UnixMilli(),
	}})
	require.Contains(t, buf.String(), "node certificate until 2026-10-25")
	require.Contains(t, buf.String(), "intermediate until 2027-10-18")

	buf.Reset()
	writeTroupeMembers(&buf, &rpc.TroupeMembersResponse{Node: "n", Peers: []rpc.TroupeMember{}, Keys: &rpc.TroupeKeys{
		Troupe: "estate", Error: "troupe estate: the node intermediate has expired; run: op read <root ref> | figaro troupe intermediate estate",
	}})
	require.Contains(t, buf.String(), "figaro troupe intermediate estate")
}
