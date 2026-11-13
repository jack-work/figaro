package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/internal/boot"
)

func freshEndpoint(t *testing.T, troupe string) endpoint {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("FIGARO_CONFIG_DIR", cfg)
	t.Setenv("FIGARO_RUNTIME_DIR", t.TempDir())
	t.Setenv("FIGARO_TROUPE", troupe)
	if err := os.MkdirAll(filepath.Join(cfg, "troupes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "troupes", "spain.toml"), []byte(`url = "wss://fig.kelliher.info"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return resolveEndpoint()
}

// With no troupe the CLI keeps its own daemon, which it may start.
func TestEndpointDefaultsToTheLocalDaemon(t *testing.T) {
	d := freshEndpoint(t, "")
	if d.configured || d.ep.Scheme != "unix" || d.ep.Address != boot.AngelusSocketPath() {
		t.Fatalf("door = %+v", d)
	}
}

// A configured troupe is reached and never started.
func TestEndpointTakesTheConfiguredTroupe(t *testing.T) {
	d := freshEndpoint(t, "unix:///run/figaro-door/angelus.sock")
	if !d.configured || d.ep.Scheme != "unix" || d.ep.Address != "/run/figaro-door/angelus.sock" {
		t.Fatalf("unix troupe: door = %+v", d)
	}

	d = freshEndpoint(t, "spain")
	if !d.configured || d.ep.Scheme != "wss" || d.ep.Address != "wss://fig.kelliher.info" || d.ep.Bearer == nil {
		t.Fatalf("wss troupe: door = %+v", d)
	}
	if d.label != "spain (wss://fig.kelliher.info)" {
		t.Fatalf("label = %q", d.label)
	}
}
