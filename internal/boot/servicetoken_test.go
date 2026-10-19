package boot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/config"
	providerPkg "github.com/jack-work/figaro/internal/provider"
)

func serviceLoaded(st config.ServiceToken) *config.Loaded {
	return &config.Loaded{Config: config.Config{ServiceTokens: map[string]config.ServiceToken{"herald": st}}}
}

// The secret must sit in $CREDENTIALS_DIRECTORY, which the bwrap provider
// hides from tools; a path anywhere else could be read by one.
func TestServiceTokenSecretMustBeACredentialID(t *testing.T) {
	serverEnv(t)
	for _, id := range []string{"", "/etc/figaro/secret", "../secret", "sub/secret", ".."} {
		_, err := ServiceToken(serviceLoaded(config.ServiceToken{TokenURL: "https://x/token", ClientID: "figaro", SecretCredential: id}), "herald")
		if err == nil || !strings.Contains(err.Error(), "credential id") {
			t.Errorf("secret_credential %q: err = %v", id, err)
		}
	}
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	_, err := ServiceToken(serviceLoaded(config.ServiceToken{TokenURL: "https://x/token", ClientID: "figaro", SecretCredential: "authelia-client-secret"}), "herald")
	if err == nil || !strings.Contains(err.Error(), "CREDENTIALS_DIRECTORY") {
		t.Errorf("unset directory: err = %v", err)
	}
}

func TestServiceTokenUnconfigured(t *testing.T) {
	serverEnv(t)
	if _, err := ServiceToken(&config.Loaded{}, "herald"); err == nil {
		t.Fatal("an unconfigured service token resolved")
	}
}

// The token is the daemon's alone: minting it puts nothing in the process
// environment a tool would inherit.
func TestServiceTokenMintsAndStaysOutOfTheEnvironment(t *testing.T) {
	_, credDir := serverEnv(t)
	writeFile(t, filepath.Join(credDir, "authelia-client-secret"), "the-secret\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, sec, _ := r.BasicAuth(); id != "figaro" || sec != "the-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "svc-token", "token_type": "bearer", "expires_in": 300})
	}))
	defer srv.Close()

	before := strings.Join(os.Environ(), "\n")
	r, err := ServiceToken(serviceLoaded(config.ServiceToken{
		TokenURL: srv.URL, ClientID: "figaro", SecretCredential: "authelia-client-secret",
		Audience: []string{"https://herald.kelliher.info"},
	}), "herald")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := r.Resolve()
	if err != nil || tok != "svc-token" {
		t.Fatalf("Resolve = %q, %v", tok, err)
	}
	after := strings.Join(os.Environ(), "\n")
	if after != before || strings.Contains(after, "svc-token") || strings.Contains(after, "the-secret") {
		t.Fatal("minting a service token changed the process environment")
	}
	assertHushUntouched(t)
}

func tokenServer(t *testing.T, minted *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, sec, _ := r.BasicAuth(); id != "figaro" || sec != "the-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		*minted++
		json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("svc-%d", *minted), "token_type": "bearer", "expires_in": 300})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func rou2Loaded(cfgDir, tokenURL string) *config.Loaded {
	return &config.Loaded{ConfigDir: cfgDir, Config: config.Config{ServiceTokens: map[string]config.ServiceToken{
		"rou2": {TokenURL: tokenURL, ClientID: "figaro", SecretCredential: "authelia-client-secret", Audience: []string{"https://rou2.kelliher.info"}},
	}}}
}

// A provider naming a service token authenticates as that client and as
// nothing else: a key lying around in the file or the environment must not
// be sent in its place.
func TestAProviderWithAServiceTokenMintsThroughIt(t *testing.T) {
	cfgDir, credDir := serverEnv(t)
	writeFile(t, filepath.Join(credDir, "authelia-client-secret"), "the-secret\n")
	writeFile(t, filepath.Join(credDir, "rou2"), "sk-stray\n")
	writeFile(t, filepath.Join(cfgDir, "providers", "rou2.toml"), "preset = \"gateway\"\nservice_token = \"rou2\"\napi_key = \"sk-file\"\n")
	var minted int
	loaded := rou2Loaded(cfgDir, tokenServer(t, &minted).URL)

	r, err := BuildResolver(loaded, "rou2")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := r.Resolve()
	if err != nil || tok != "svc-1" {
		t.Fatalf("Resolve = %q, %v", tok, err)
	}
	if err := r.Invalidate(tok); err != nil {
		t.Fatal(err)
	}
	if tok, _ := r.Resolve(); tok != "svc-2" {
		t.Fatalf("Resolve after Invalidate = %q, want a fresh mint", tok)
	}
	assertHushUntouched(t)
}

// Every aria builds its own resolver; one daemon is one client, so they
// share one token rather than each minting its own.
func TestAriasShareOneServiceToken(t *testing.T) {
	cfgDir, credDir := serverEnv(t)
	writeFile(t, filepath.Join(credDir, "authelia-client-secret"), "the-secret\n")
	writeFile(t, filepath.Join(cfgDir, "providers", "rou2.toml"), "preset = \"gateway\"\nservice_token = \"rou2\"\n")
	var minted int
	loaded := rou2Loaded(cfgDir, tokenServer(t, &minted).URL)

	for i := 0; i < 3; i++ {
		r, err := BuildResolver(loaded, "rou2")
		if err != nil {
			t.Fatal(err)
		}
		if tok, err := r.Resolve(); err != nil || tok != "svc-1" {
			t.Fatalf("aria %d: Resolve = %q, %v", i, tok, err)
		}
	}
	if minted != 1 {
		t.Fatalf("minted %d tokens for one client", minted)
	}
}

func TestAProviderNamingAMissingServiceTokenDoesNotBuild(t *testing.T) {
	cfgDir, _ := serverEnv(t)
	writeFile(t, filepath.Join(cfgDir, "providers", "rou2.toml"), "preset = \"gateway\"\nservice_token = \"nope\"\napi_key = \"sk-file\"\n")
	if _, err := BuildResolver(rou2Loaded(cfgDir, "https://x/token"), "rou2"); err == nil || !strings.Contains(err.Error(), "service_tokens.nope") {
		t.Fatalf("err = %v; a misnamed service token must not fall back to the file's key", err)
	}
}

func TestCredentialSourcesNameTheServiceTokenFirst(t *testing.T) {
	cfgDir, credDir := serverEnv(t)
	writeFile(t, filepath.Join(cfgDir, "providers", "rou2.toml"), "preset = \"gateway\"\nservice_token = \"rou2\"\napi_key = \"sk-file\"\n")
	loaded := rou2Loaded(cfgDir, "https://auth.example/token")
	reg := &providerPkg.Registration{Name: "rou2"}

	srcs, err := CredentialSources(loaded, reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 2 || srcs[0].Kind != SourceService || srcs[1].Kind != SourcePlainKey || srcs[0].Owned {
		t.Fatalf("sources = %+v", srcs)
	}
	want := "service token rou2: client figaro at https://auth.example/token (secret " + filepath.Join(credDir, "authelia-client-secret") + " missing)"
	if srcs[0].Where != want {
		t.Fatalf("Where = %q\nwant    %q", srcs[0].Where, want)
	}

	writeFile(t, filepath.Join(credDir, "authelia-client-secret"), "the-secret\n")
	srcs, _ = CredentialSources(loaded, reg, nil)
	if srcs[0].Where != "service token rou2: client figaro at https://auth.example/token" {
		t.Fatalf("Where = %q", srcs[0].Where)
	}

	srcs, _ = CredentialSources(&config.Loaded{ConfigDir: cfgDir}, reg, nil)
	if !strings.Contains(srcs[0].Where, "not configured") {
		t.Fatalf("Where = %q; an unconfigured token must say so", srcs[0].Where)
	}
}
