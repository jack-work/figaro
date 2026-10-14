package routed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/auth"
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
		"house":     houseTOML,
		"keyed":     "preset = \"gateway\"\nbase_url = \"http://127.0.0.1:1/v1\"\n",
		"decisions": "base_url = \"http://127.0.0.1:8787/v1\"\n",
	})
	got := RegisterDeclared(loaded)
	if strings.Join(got, ",") != "house,keyed" {
		t.Fatalf("registered %v; a file without a preset (decisions.toml) must not become a provider", got)
	}
	house := provider.Lookup("house")
	if house == nil || !house.Declared || !house.HasOAuth || house.Login == nil {
		t.Fatalf("house = %+v: [oauth] must make hush's token the credential and offer a login", house)
	}
	if keyed := provider.Lookup("keyed"); keyed.HasOAuth || keyed.Login != nil {
		t.Fatalf("keyed = %+v: no [oauth], no login", keyed)
	}
	p, err := house.Build(provider.BuildContext{Loaded: loaded, Resolver: staticToken("t")})
	if err != nil || p == nil {
		t.Fatalf("build: %v", err)
	}
}

func TestDeclaredLoginReceivesTheFilesOAuth(t *testing.T) {
	loaded := declaredDir(t, map[string]string{"house": houseTOML})
	RegisterDeclared(loaded)
	var gotName string
	var gotOAuth auth.OAuthSettings
	prev := DeclaredLogin
	DeclaredLogin = func(_ *config.Loaded, name string, s auth.OAuthSettings) error {
		gotName, gotOAuth = name, s
		return nil
	}
	t.Cleanup(func() { DeclaredLogin = prev })
	if err := provider.Lookup("house").Login(loaded); err != nil {
		t.Fatal(err)
	}
	if gotName != "house" || gotOAuth.Issuer != "https://auth.example" || gotOAuth.ClientID != "house" || gotOAuth.Flow != "device" || len(gotOAuth.Scopes) != 3 {
		t.Fatalf("login got %q %+v", gotName, gotOAuth)
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
