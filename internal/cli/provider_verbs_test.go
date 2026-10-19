package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/config"
)

// Bare `login` guessed between a provider and a daemon endpoint. Model
// credentials now have their own noun, and the bare verb is gone rather than
// kept as an alias that would go on guessing.
func TestBareLoginIsGoneAndProviderCarriesTheVerbs(t *testing.T) {
	r := buildRouter("figaro", nil)
	require.False(t, r.HasCommand("login"), "bare login must not exist")
	cmd, ok := r.Command("provider")
	require.True(t, ok, "provider must exist")
	require.Contains(t, cmd.Usage, "login")
	require.Contains(t, cmd.Usage, "logout")
	require.Contains(t, cmd.Usage, "ls")
	require.False(t, overlayVerbs["login"], "the pager must not keep a :login")
	require.True(t, terminalSubverbs["provider login"], ":provider login reads the keyboard")
	require.True(t, terminalSubverbs["provider logout"], ":provider logout may ask for the vault")
}

func providerRig(t *testing.T) *config.Loaded {
	t.Helper()
	dir := t.TempDir()
	for _, v := range []string{"ANTHROPIC_API_KEY", "META_API_KEY", "OPENROUTER_API_KEY", "CREDENTIALS_DIRECTORY"} {
		t.Setenv(v, "")
	}
	loaded, err := config.Load(dir)
	require.NoError(t, err)
	return loaded
}

func writeProviderFile(t *testing.T, loaded *config.Loaded, name, body string) string {
	t.Helper()
	path := loaded.ProviderAuthPath(name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func kinds(srcs []boot.CredentialSource) []string {
	out := make([]string, len(srcs))
	for i, s := range srcs {
		out[i] = s.Kind
	}
	return out
}

func TestProviderSourcesFollowResolveOrder(t *testing.T) {
	loaded := providerRig(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")
	creds := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(creds, "anthropic"), []byte("sk-dir"), 0o600))
	t.Setenv("CREDENTIALS_DIRECTORY", creds)
	writeProviderFile(t, loaded, "anthropic", "api_key = \"AGE-ENC[abc]\"\n")
	store := &fakeOAuth{names: []string{"anthropic", "troupe:spain"}}

	srcs, err := providerSources(loaded, "anthropic", store)
	require.NoError(t, err)
	require.Equal(t, []string{boot.SourceEnv, boot.SourceCredDir, boot.SourceSealedKey, boot.SourceOAuth}, kinds(srcs))
	require.Equal(t, "$ANTHROPIC_API_KEY", srcs[0].Where)
	require.False(t, srcs[0].Owned)
	require.False(t, srcs[1].Owned)
	require.True(t, srcs[2].Owned)
	require.True(t, srcs[3].Owned)
}

func TestProviderSourcesSkipOAuthForAKeyOnlyProvider(t *testing.T) {
	loaded := providerRig(t)
	store := &fakeOAuth{names: []string{"meta"}}
	srcs, err := providerSources(loaded, "meta", store)
	require.NoError(t, err)
	require.Empty(t, srcs, "meta has no OAuth strategy, so a hush entry by its name is not its credential")
}

func TestLogoutRemovesTheHushOAuthCredential(t *testing.T) {
	loaded := providerRig(t)
	store := &fakeOAuth{names: []string{"anthropic", "troupe:spain"}}
	rep, err := providerLogout(loaded, "anthropic", store)
	require.NoError(t, err)
	require.Equal(t, []string{"anthropic"}, store.deleted)
	require.Equal(t, []string{boot.SourceOAuth}, kinds(rep.Removed))
	require.Empty(t, rep.Remaining)
}

func TestLogoutRemovesOnlyTheSealedKeyAndKeepsTheRestOfTheFile(t *testing.T) {
	loaded := providerRig(t)
	path := writeProviderFile(t, loaded, "meta", "api_key = \"AGE-ENC[abc]\"\nbase_url = \"https://example.test\"\n")
	rep, err := providerLogout(loaded, "meta", &fakeOAuth{})
	require.NoError(t, err)
	require.Equal(t, []string{boot.SourceSealedKey}, kinds(rep.Removed))
	require.Empty(t, rep.Remaining)

	doc := map[string]any{}
	_, err = toml.DecodeFile(path, &doc)
	require.NoError(t, err)
	require.NotContains(t, doc, "api_key")
	require.Equal(t, "https://example.test", doc["base_url"])
}

func TestLogoutLeavesWhatFigaroDidNotWrite(t *testing.T) {
	loaded := providerRig(t)
	t.Setenv("OPENROUTER_API_KEY", "sk-env")
	keyFile := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(keyFile, []byte("sk-file"), 0o600))
	body := "api_key = \"sk-plain\"\napi_key_file = \"" + keyFile + "\"\n"
	path := writeProviderFile(t, loaded, "openrouter", body)

	rep, err := providerLogout(loaded, "openrouter", &fakeOAuth{})
	require.NoError(t, err)
	require.Empty(t, rep.Removed)
	require.Equal(t, []string{boot.SourceEnv, boot.SourceKeyFile, boot.SourcePlainKey}, kinds(rep.Remaining))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, body, string(after), "a file figaro did not write is left byte for byte")
}

func TestLogoutWithNothingStoredIsANoOp(t *testing.T) {
	loaded := providerRig(t)
	rep, err := providerLogout(loaded, "anthropic", &fakeOAuth{})
	require.NoError(t, err)
	require.Empty(t, rep.Removed)
	require.Empty(t, rep.Remaining)
}

func TestLogoutRefusesAnUnknownProvider(t *testing.T) {
	loaded := providerRig(t)
	_, err := providerLogout(loaded, "spain", &fakeOAuth{names: []string{"troupe:spain"}})
	require.ErrorContains(t, err, "no provider")
}

// copilot reaches the API only through its hush credential. Remove that and a
// token in the environment is handed to hush again on the next request, so
// logout must say the provider is still signed in.
func TestCopilotLogoutLeavesATokenThatWouldRefillHush(t *testing.T) {
	loaded := providerRig(t)
	for _, v := range []string{"COPILOT_GITHUB_TOKEN", "GITHUB_TOKEN"} {
		t.Setenv(v, "")
	}
	t.Setenv("GH_TOKEN", "gho_env")
	store := &fakeOAuth{names: []string{"copilot"}}

	srcs, err := providerSources(loaded, "copilot", store)
	require.NoError(t, err)
	require.Equal(t, []string{boot.SourceOAuth, boot.SourceEnv}, kinds(srcs), "hush is what copilot uses; the env token is what refills it")

	rep, err := providerLogout(loaded, "copilot", store)
	require.NoError(t, err)
	require.Equal(t, []string{"copilot"}, store.deleted)
	require.Equal(t, []string{boot.SourceEnv}, kinds(rep.Remaining))
}

// Completion is the router's: `provider <TAB>` offers the three verbs, and
// the slot after a verb offers provider names and nothing past it.
func TestProviderCompletesVerbsThenNames(t *testing.T) {
	cmd, ok := buildRouter("figaro", nil).Command("provider")
	require.True(t, ok)
	require.ElementsMatch(t, []string{"login", "logout", "ls"}, cmd.CompleteArgs(&cmdkit.CompleteContext{}))
	names := cmd.CompleteArgs(&cmdkit.CompleteContext{Args: []string{"logout"}})
	require.Contains(t, names, "anthropic")
	require.Contains(t, names, "copilot")
	require.Empty(t, cmd.CompleteArgs(&cmdkit.CompleteContext{Args: []string{"logout", "copilot"}}))
}

// The daemon mints a service token from config; logout has nothing of its to
// remove and says where it comes from.
func TestLogoutKeepsAServiceToken(t *testing.T) {
	loaded := providerRig(t)
	writeProviderFile(t, loaded, "gateway", "service_token = \"rou2\"\n")
	rep, err := providerLogout(loaded, "gateway", noOAuth{})
	require.NoError(t, err)
	require.Empty(t, rep.Removed)
	require.Equal(t, []string{boot.SourceService}, kinds(rep.Remaining))
	require.Contains(t, leaveHint(rep.Remaining[0]), "service_token")
}
