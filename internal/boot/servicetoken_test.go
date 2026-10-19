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

// token-exchange is a public client: its subject is a credential id like a
// secret, and a secret beside it is refused rather than sent, because an IdP
// holding the client as public answers invalid_request to one. The
// exchange's own keys are refused on a client_credentials token, where they
// would be silently ignored.
func TestServiceTokenExchangeConfig(t *testing.T) {
	serverEnv(t)
	base := config.ServiceToken{Grant: "token-exchange", TokenURL: "https://x/token", ClientID: "rou2", SubjectCredential: "rou2-api-token"}
	if _, err := ServiceToken(serviceLoaded(base), "herald"); err != nil {
		t.Fatalf("a well-formed token-exchange token: %v", err)
	}
	for name, mut := range map[string]func(*config.ServiceToken){
		"a secret":                  func(s *config.ServiceToken) { s.SecretCredential = "x" },
		"an auth method":            func(s *config.ServiceToken) { s.AuthMethod = "client_secret_post" },
		"no client id":              func(s *config.ServiceToken) { s.ClientID = "" },
		"a path subject":            func(s *config.ServiceToken) { s.SubjectCredential = "/run/secrets/rou2-api-token" },
		"no subject":                func(s *config.ServiceToken) { s.SubjectCredential = "" },
		"an unknown grant":          func(s *config.ServiceToken) { s.Grant = "password" },
		"a subject on client_creds": func(s *config.ServiceToken) { s.Grant = ""; s.SecretCredential = "x" },
		"a token type on client_creds": func(s *config.ServiceToken) {
			s.Grant = "client_credentials"
			s.SubjectCredential = ""
			s.SecretCredential = "x"
			s.SubjectTokenType = "y"
		},
	} {
		st := base
		mut(&st)
		if _, err := ServiceToken(serviceLoaded(st), "herald"); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// The exchange posts the subject read from $CREDENTIALS_DIRECTORY and mints
// without touching the environment or hush, like client_credentials.
func TestServiceTokenExchangeMints(t *testing.T) {
	_, credDir := serverEnv(t)
	writeFile(t, filepath.Join(credDir, "rou2-api-token"), "api-token\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if _, _, basic := r.BasicAuth(); basic || r.PostForm.Get("client_secret") != "" ||
			r.PostForm.Get("client_id") != "rou2" || r.PostForm.Get("subject_token") != "api-token" ||
			r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:token-exchange" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "exchanged", "token_type": "bearer", "expires_in": 900})
	}))
	defer srv.Close()

	before := strings.Join(os.Environ(), "\n")
	r, err := ServiceToken(serviceLoaded(config.ServiceToken{
		Grant: "token-exchange", TokenURL: srv.URL, ClientID: "rou2", SubjectCredential: "rou2-api-token",
		Scopes: []string{"openid", "groups", "profile"},
	}), "herald")
	if err != nil {
		t.Fatal(err)
	}
	if tok, err := r.Resolve(); err != nil || tok != "exchanged" {
		t.Fatalf("Resolve = %q, %v", tok, err)
	}
	if after := strings.Join(os.Environ(), "\n"); after != before || strings.Contains(after, "api-token") {
		t.Fatal("exchanging a service token changed the process environment")
	}
	assertHushUntouched(t)
}

// provider ls names the grant and the file it reads, so a missing subject
// is not reported as a missing secret.
func TestCredentialSourcesNameTheExchangeSubject(t *testing.T) {
	cfgDir, credDir := serverEnv(t)
	writeFile(t, filepath.Join(cfgDir, "providers", "rou2.toml"), "preset = \"gateway\"\nservice_token = \"rou2\"\n")
	loaded := &config.Loaded{ConfigDir: cfgDir, Config: config.Config{ServiceTokens: map[string]config.ServiceToken{
		"rou2": {Grant: "token-exchange", TokenURL: "https://idm.example/oauth2/token", ClientID: "rou2", SubjectCredential: "rou2-api-token"},
	}}}
	srcs, err := CredentialSources(loaded, &providerPkg.Registration{Name: "rou2"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "service token rou2: client rou2 at https://idm.example/oauth2/token by token exchange (subject " + filepath.Join(credDir, "rou2-api-token") + " missing)"
	if srcs[0].Where != want {
		t.Fatalf("Where = %q\nwant    %q", srcs[0].Where, want)
	}
}
