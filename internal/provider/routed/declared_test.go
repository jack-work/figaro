package routed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
)

func declaredDir(t *testing.T, files map[string]string) *config.Loaded {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "providers"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, "providers", name+".toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &config.Loaded{ConfigDir: dir}
}

const houseTOML = `preset = "gateway"
base_url = "http://100.103.40.20:9110/v1"

[oauth]
issuer = "https://auth.example"
client_id = "house"
scopes = ["openid", "groups", "offline_access"]
flow = "device"
`

func TestAProviderFileWithAPresetIsAProvider(t *testing.T) {
	loaded := declaredDir(t, map[string]string{
		"house": houseTOML,
		"keyed": "preset = \"gateway\"\nbase_url = \"http://127.0.0.1:1/v1\"\n",
		"notes": "base_url = \"http://127.0.0.1:8787/v1\"\n",
	})
	got := RegisterDeclared(loaded)
	if strings.Join(got, ",") != "house,keyed" {
		t.Fatalf("registered %v; a file without a preset must not become a provider", got)
	}
	house := provider.Lookup("house")
	if house == nil || !house.Declared || !house.HasOAuth || house.OAuth == nil {
		t.Fatalf("house = %+v: [oauth] must make hush's token the credential and offer a login", house)
	}
	if keyed := provider.Lookup("keyed"); keyed.HasOAuth || keyed.OAuth != nil {
		t.Fatalf("keyed = %+v: no [oauth], no login", keyed)
	}
	p, err := house.Build(provider.BuildContext{Loaded: loaded, Resolver: staticToken("t")})
	if err != nil || p == nil {
		t.Fatalf("build: %v", err)
	}
}

func TestTheRegistrationCarriesTheFilesOAuth(t *testing.T) {
	// figaro provider login <name> reads the [oauth] settings off the registration.
	RegisterDeclared(declaredDir(t, map[string]string{"house": houseTOML}))
	got := provider.Lookup("house").OAuth
	if got == nil || got.Issuer != "https://auth.example" || got.ClientID != "house" || got.Flow != "device" || len(got.Scopes) != 3 {
		t.Fatalf("oauth = %+v", got)
	}
}

func TestADeclaredFileCannotReplaceABuiltIn(t *testing.T) {
	before := provider.Lookup("openrouter")
	RegisterDeclared(declaredDir(t, map[string]string{"openrouter": houseTOML}))
	if provider.Lookup("openrouter") != before {
		t.Fatal("providers/openrouter.toml with a preset replaced the built-in")
	}
}

func TestAnUnknownPresetIsNotAProvider(t *testing.T) {
	got := RegisterDeclared(declaredDir(t, map[string]string{"odd": "preset = \"nonesuch\"\nbase_url = \"http://x/v1\"\n"}))
	if len(got) != 0 {
		t.Fatalf("registered %v", got)
	}
}

func TestADeclaredOAuthGatewayNeedsItsCredential(t *testing.T) {
	// The builtin gateway may run keyless; one that declares [oauth] must not
	// silently send no Authorization when the login is missing.
	loaded := declaredDir(t, map[string]string{"house": houseTOML})
	RegisterDeclared(loaded)
	t.Setenv("HOUSE_BASE_URL", "")
	p, err := provider.Lookup("house").Build(provider.BuildContext{Loaded: loaded, Resolver: staticToken("t")})
	if err != nil {
		t.Fatal(err)
	}
	if p.(*Provider).spec.AuthOptional {
		t.Fatal("an [oauth] gateway was built with AuthOptional")
	}
}

// A host that takes a key from the environment (TypeSafe, Experiential Labs)
// is a declared provider like any other; api_key_env names the variable, and
// the resolver reads it through the registration as it does a built-in's.
func TestAPIKeyEnvIsTheRegistrationsVariable(t *testing.T) {
	loaded := declaredDir(t, map[string]string{
		"typesafe": "preset = \"gateway\"\nbase_url = \"https://api.typesafe.ai/v1\"\napi_key_env = \"TYPESAFE_API_KEY\"\n",
		"plain":    "preset = \"gateway\"\nbase_url = \"http://127.0.0.1:1/v1\"\n",
	})
	RegisterDeclared(loaded)
	if envs := provider.Lookup("typesafe").CredentialEnvs(); strings.Join(envs, ",") != "TYPESAFE_API_KEY" {
		t.Fatalf("typesafe reads %v", envs)
	}
	if envs := provider.Lookup("plain").CredentialEnvs(); len(envs) != 0 {
		t.Fatalf("plain reads %v; no api_key_env, no variable", envs)
	}
}

func TestAServiceTokenAndOAuthAreNotBothTheCredential(t *testing.T) {
	loaded := declaredDir(t, map[string]string{"both": "service_token = \"rou2\"\n" + houseTOML})
	if got := RegisterDeclared(loaded); len(got) != 0 {
		t.Fatalf("registered %v; two credentials for one provider must be refused", got)
	}
}

// A minted token that fails must stop the turn, not send it anonymously.
func TestAServiceTokenMakesTheGatewayRequireAuth(t *testing.T) {
	loaded := declaredDir(t, map[string]string{"svc": "preset = \"gateway\"\nbase_url = \"http://127.0.0.1:1/v1\"\nservice_token = \"rou2\"\n"})
	RegisterDeclared(loaded)
	p, err := provider.Lookup("svc").Build(provider.BuildContext{Loaded: loaded, Resolver: staticToken("t")})
	if err != nil {
		t.Fatal(err)
	}
	if p.(*Provider).spec.AuthOptional {
		t.Fatal("a gateway with a service token treats a missing token as optional")
	}
}
