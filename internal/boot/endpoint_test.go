package boot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/config"
)

func endpointConfig(t *testing.T, name, body string) *config.Loaded {
	t.Helper()
	cfgDir, _ := serverEnv(t)
	t.Setenv("FIGARO_ENDPOINT", "")
	if err := os.MkdirAll(filepath.Join(cfgDir, "endpoints"), 0o700); err != nil {
		t.Fatal(err)
	}
	if name != "" {
		writeFile(t, filepath.Join(cfgDir, "endpoints", name+".toml"), body)
	}
	return &config.Loaded{ConfigDir: cfgDir}
}

const spainEndpoint = `url = "wss://fig.kelliher.info"
[oauth]
issuer = "https://auth.kelliher.info"
client_id = "figaro-cli"
flow = "device"
`

func TestNoEndpointConfiguredMeansLocal(t *testing.T) {
	l := endpointConfig(t, "", "")
	if _, ok, err := SelectedEndpoint(l); ok || err != nil {
		t.Fatalf("ok=%v err=%v with nothing configured", ok, err)
	}
}

func TestEndpointSelectedByConfigAndEnvironment(t *testing.T) {
	l := endpointConfig(t, "spain", spainEndpoint)
	l.Config.CLI.Endpoint = "spain"
	e, ok, err := SelectedEndpoint(l)
	if err != nil || !ok || e.URL != "wss://fig.kelliher.info" || e.OAuth == nil || e.OAuth.ClientID != "figaro-cli" {
		t.Fatalf("config selection = %+v ok=%v err=%v", e, ok, err)
	}
	if e.HushCredential() != "endpoint:spain" {
		t.Fatalf("hush credential = %q", e.HushCredential())
	}

	t.Setenv("FIGARO_ENDPOINT", "unix:///run/figaro-door/angelus.sock")
	e, ok, err = SelectedEndpoint(l)
	if err != nil || !ok || e.URL != "unix:///run/figaro-door/angelus.sock" {
		t.Fatalf("env selection = %+v ok=%v err=%v: the environment outranks the config", e, ok, err)
	}
	if EndpointResolver(e) != nil {
		t.Fatal("a unix endpoint authenticates by peer credentials, not a bearer")
	}
}

func TestEndpointRefusals(t *testing.T) {
	l := endpointConfig(t, "plain", `url = "http://fig.kelliher.info"`+"\n")
	for _, sel := range []string{"plain", "missing", "../x", "https://fig.kelliher.info"} {
		t.Setenv("FIGARO_ENDPOINT", sel)
		if _, _, err := SelectedEndpoint(l); err == nil {
			t.Errorf("FIGARO_ENDPOINT=%s was accepted", sel)
		}
	}
}

type fakeWorkload struct{ aud string }

func (f *fakeWorkload) Token(aud string) (string, error) { f.aud = aud; return "wl-token", nil }

// Inside a box the workload source answers first and hush is never reached.
func TestEndpointBearerPrefersTheWorkloadSource(t *testing.T) {
	l := endpointConfig(t, "spain", spainEndpoint)
	fw := &fakeWorkload{}
	WorkloadSource = fw
	t.Cleanup(func() { WorkloadSource = nil })

	e, err := LoadEndpoint(l, "spain")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := EndpointResolver(e).Resolve()
	if err != nil || tok != "wl-token" {
		t.Fatalf("Resolve = %q, %v", tok, err)
	}
	if fw.aud != "wss://fig.kelliher.info" {
		t.Fatalf("audience = %q, want the endpoint URL by default", fw.aud)
	}
	assertHushUntouched(t)
}

func TestEndpointWithoutAnySourceSaysSo(t *testing.T) {
	l := endpointConfig(t, "bare", `url = "wss://fig.kelliher.info"`+"\n")
	e, _ := LoadEndpoint(l, "bare")
	_, err := EndpointResolver(e).Resolve()
	if err == nil || !strings.Contains(err.Error(), "no credential") {
		t.Fatalf("err = %v", err)
	}
	assertHushUntouched(t)
}
