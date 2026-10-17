package boot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/config"
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
