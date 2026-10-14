package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRou2Root(t *testing.T) {
	for in, want := range map[string]string{
		"http://100.103.40.20:9110":     "http://100.103.40.20:9110",
		"http://100.103.40.20:9110/":    "http://100.103.40.20:9110",
		" http://100.103.40.20:9110/v1": "http://100.103.40.20:9110",
		"https://rou2.example/v1/":      "https://rou2.example",
	} {
		got, err := Rou2Root(in)
		if err != nil || got != want {
			t.Errorf("Rou2Root(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "100.103.40.20:9110", "ftp://x", "http://x/u/explabs/v1"} {
		if _, err := Rou2Root(bad); err == nil {
			t.Errorf("Rou2Root(%q) accepted", bad)
		}
	}
}

type rou2Fixture struct {
	issuer *httptest.Server
	rou2   *httptest.Server
	oidc   map[string]string
	doc    map[string]any
}

func newRou2Fixture(t *testing.T) *rou2Fixture {
	f := &rou2Fixture{}
	f.issuer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(f.oidc)
	}))
	t.Cleanup(f.issuer.Close)
	f.rou2 = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != Rou2WellKnown {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(f.doc)
	}))
	t.Cleanup(f.rou2.Close)
	f.oidc = map[string]string{
		"issuer":                        f.issuer.URL,
		"token_endpoint":                f.issuer.URL + "/api/oidc/token",
		"device_authorization_endpoint": f.issuer.URL + "/api/oidc/device-authorization",
	}
	f.doc = map[string]any{
		"service":        "rou2",
		"gate":           "oidc",
		"issuer":         f.issuer.URL,
		"client_id":      "rou2",
		"scopes":         []string{"openid", "profile", "offline_access", "groups"},
		"oidc_discovery": f.issuer.URL + "/.well-known/openid-configuration",
		"upstreams":      []map[string]any{{"name": "explabs", "base_url": f.rou2.URL + "/u/explabs/v1", "default": true}},
	}
	prev := rou2HTTP
	rou2HTTP = f.issuer.Client()
	t.Cleanup(func() { rou2HTTP = prev })
	return f
}

func TestDiscoverRou2BuildsTheDeviceFlowFromBothDocuments(t *testing.T) {
	f := newRou2Fixture(t)
	d, cfg, err := DiscoverRou2(f.rou2.URL)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientID != "rou2" || cfg.TokenURL != f.oidc["token_endpoint"] || cfg.DeviceURL != f.oidc["device_authorization_endpoint"] {
		t.Fatalf("cfg = %+v", cfg)
	}
	// offline_access must appear exactly once and last, or Authelia mints no refresh token.
	if cfg.Scope != "openid profile groups offline_access" {
		t.Fatalf("scope = %q", cfg.Scope)
	}
	if len(d.Upstreams) != 1 || d.Upstreams[0].Name != "explabs" {
		t.Fatalf("upstreams = %+v", d.Upstreams)
	}
}

func TestDiscoverRou2RefusesEndpointsOffTheIssuer(t *testing.T) {
	// A tampered discovery document must not send the device code or the
	// refresh token to a host that is not the issuer.
	f := newRou2Fixture(t)
	f.oidc["token_endpoint"] = "https://evil.example/token"
	if _, _, err := DiscoverRou2(f.rou2.URL); err == nil || !strings.Contains(err.Error(), "not on the issuer") {
		t.Fatalf("err = %v", err)
	}
}

func TestDiscoverRou2RefusesAnIssuerMismatch(t *testing.T) {
	f := newRou2Fixture(t)
	f.oidc["issuer"] = "https://other.example"
	if _, _, err := DiscoverRou2(f.rou2.URL); err == nil || !strings.Contains(err.Error(), "names issuer") {
		t.Fatalf("err = %v", err)
	}
}

func TestDiscoverRou2RefusesAPlainHTTPIssuer(t *testing.T) {
	f := newRou2Fixture(t)
	f.doc["issuer"] = strings.Replace(f.issuer.URL, "https://", "http://", 1)
	if _, _, err := DiscoverRou2(f.rou2.URL); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("err = %v", err)
	}
}

func TestDiscoverRou2RefusesAnOpenGate(t *testing.T) {
	f := newRou2Fixture(t)
	f.doc["gate"] = "open"
	if _, _, err := DiscoverRou2(f.rou2.URL); err == nil {
		t.Fatal("an open gate was treated as a login target")
	}
}
