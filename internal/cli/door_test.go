package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/internal/boot"
)

func freshDoor(t *testing.T, endpoint string) door {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("FIGARO_CONFIG_DIR", cfg)
	t.Setenv("FIGARO_RUNTIME_DIR", t.TempDir())
	t.Setenv("FIGARO_ENDPOINT", endpoint)
	if err := os.MkdirAll(filepath.Join(cfg, "endpoints"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "endpoints", "spain.toml"), []byte(`url = "wss://fig.kelliher.info"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return resolveDoor()
}

// With no endpoint the CLI keeps its own daemon, which it may start.
func TestDoorDefaultsToTheLocalDaemon(t *testing.T) {
	d := freshDoor(t, "")
	if d.configured || d.ep.Scheme != "unix" || d.ep.Address != boot.AngelusSocketPath() {
		t.Fatalf("door = %+v", d)
	}
}

// A configured endpoint is reached and never started.
func TestDoorTakesTheConfiguredEndpoint(t *testing.T) {
	d := freshDoor(t, "unix:///run/figaro-door/angelus.sock")
	if !d.configured || d.ep.Scheme != "unix" || d.ep.Address != "/run/figaro-door/angelus.sock" {
		t.Fatalf("unix endpoint: door = %+v", d)
	}

	d = freshDoor(t, "spain")
	if !d.configured || d.ep.Scheme != "wss" || d.ep.Address != "wss://fig.kelliher.info" || d.ep.Bearer == nil {
		t.Fatalf("wss endpoint: door = %+v", d)
	}
	if d.label != "spain (wss://fig.kelliher.info)" {
		t.Fatalf("label = %q", d.label)
	}
}
