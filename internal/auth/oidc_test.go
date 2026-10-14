package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type issuerFixture struct {
	srv    *httptest.Server
	oidc   map[string]string
	agents []string
}

func newIssuer(t *testing.T) *issuerFixture {
	f := &issuerFixture{}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.agents = append(f.agents, r.UserAgent())
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(f.oidc)
		case "/.well-known/gateway":
			json.NewEncoder(w).Encode(map[string]any{
				"service":   "anything",
				"issuer":    f.srv.URL,
				"client_id": "from-discovery",
				"scopes":    []string{"openid", "groups"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	f.oidc = map[string]string{
		"issuer":                        f.srv.URL,
		"token_endpoint":                f.srv.URL + "/api/oidc/token",
		"device_authorization_endpoint": f.srv.URL + "/api/oidc/device-authorization",
	}
	prev := oidcHTTP
	oidcHTTP = f.srv.Client()
	t.Cleanup(func() { oidcHTTP = prev })
	return f
}

func (f *issuerFixture) settings() OAuthSettings {
	return OAuthSettings{Issuer: f.srv.URL, ClientID: "gw", Scopes: []string{"openid", "profile", "offline_access", "groups"}}
}

func TestDeviceConfigComesFromTheIssuersDiscovery(t *testing.T) {
	f := newIssuer(t)
	cfg, err := f.settings().DeviceConfig("house")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderName != "house" || cfg.ClientID != "gw" || cfg.TokenURL != f.oidc["token_endpoint"] || cfg.DeviceURL != f.oidc["device_authorization_endpoint"] {
		t.Fatalf("cfg = %+v", cfg)
	}
	// offline_access exactly once and last, or no refresh token is minted.
	if cfg.Scope != "openid profile groups offline_access" {
		t.Fatalf("scope = %q", cfg.Scope)
	}
}

func TestDeviceConfigSendsAFigaroUserAgent(t *testing.T) {
	// Cloudflare in front of auth.kelliher.info bans some default agents
	// (error 1010); every issuer call must name figaro.
	f := newIssuer(t)
	prev := UserAgent
	UserAgent = "figaro/9.9.9"
	t.Cleanup(func() { UserAgent = prev })
	if _, err := f.settings().DeviceConfig("house"); err != nil {
		t.Fatal(err)
	}
	if len(f.agents) == 0 || f.agents[0] != "figaro/9.9.9" {
		t.Fatalf("agents = %q", f.agents)
	}
}

func TestDeviceConfigRefusesEndpointsOffTheIssuer(t *testing.T) {
	f := newIssuer(t)
	f.oidc["token_endpoint"] = "https://evil.example/token"
	if _, err := f.settings().DeviceConfig("house"); err == nil || !strings.Contains(err.Error(), "not on the issuer") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeviceConfigRefusesAnIssuerMismatch(t *testing.T) {
	f := newIssuer(t)
	f.oidc["issuer"] = "https://other.example"
	if _, err := f.settings().DeviceConfig("house"); err == nil || !strings.Contains(err.Error(), "names issuer") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeviceConfigRefusesWhatItCannotDo(t *testing.T) {
	f := newIssuer(t)
	for name, mutate := range map[string]func(*OAuthSettings){
		"plain http issuer": func(s *OAuthSettings) { s.Issuer = strings.Replace(s.Issuer, "https://", "http://", 1) },
		"no client id":      func(s *OAuthSettings) { s.ClientID = "" },
		"another flow":      func(s *OAuthSettings) { s.Flow = "authorization_code" },
	} {
		s := f.settings()
		mutate(&s)
		if _, err := s.DeviceConfig("house"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	delete(f.oidc, "device_authorization_endpoint")
	if _, err := f.settings().DeviceConfig("house"); err == nil || !strings.Contains(err.Error(), "device flow") {
		t.Fatalf("issuer without a device endpoint: err = %v", err)
	}
}

func TestDiscoveredFillsOnlyWhatTheFileLeavesOut(t *testing.T) {
	f := newIssuer(t)
	s, err := OAuthSettings{ClientID: "from-file", Discovery: f.srv.URL + "/.well-known/gateway"}.Discovered()
	if err != nil {
		t.Fatal(err)
	}
	if s.Issuer != f.srv.URL || s.ClientID != "from-file" || strings.Join(s.Scopes, " ") != "openid groups" {
		t.Fatalf("settings = %+v", s)
	}
}
