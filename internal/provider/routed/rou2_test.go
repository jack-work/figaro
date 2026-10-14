package routed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
)

func TestRou2IsAnOAuthProvider(t *testing.T) {
	reg := provider.Lookup("rou2")
	if reg == nil {
		t.Fatal("rou2 is not registered")
	}
	// Without HasOAuth, BuildResolver never asks hush for the Authelia token.
	if !reg.HasOAuth || reg.ExchangeGrant != "" {
		t.Fatalf("rou2 must read hush's token as the bearer: %+v", reg)
	}
}

func TestRou2NeedsABaseURLAndSaysHowToGetOne(t *testing.T) {
	t.Setenv("ROU2_BASE_URL", "")
	_, err := provider.Lookup("rou2").Build(provider.BuildContext{Loaded: &config.Loaded{ConfigDir: t.TempDir()}, Resolver: staticToken("t")})
	if err == nil || !strings.Contains(err.Error(), "figaro login rou2") {
		t.Fatalf("err = %v", err)
	}
}

func TestRou2ReadsBaseURLFromItsFile(t *testing.T) {
	t.Setenv("ROU2_BASE_URL", "")
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "providers"), 0o700)
	os.WriteFile(filepath.Join(dir, "providers", "rou2.toml"), []byte("base_url = \"http://100.103.40.20:9110/v1\"\n"), 0o600)
	p, err := provider.Lookup("rou2").Build(provider.BuildContext{Loaded: &config.Loaded{ConfigDir: dir}, Resolver: staticToken("t")})
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		t.Fatal("nil provider")
	}
}

func TestRou2CachesPerBlockNotAtTopLevel(t *testing.T) {
	// explabs relays block-level cache_control and honours no top-level directive.
	if !rou2Caps.BlockMarkers || rou2Caps.TopLevel || !rou2Caps.MarkTail() {
		t.Fatalf("caps = %+v", rou2Caps)
	}
}

func TestRou2DoorAcceptsTheRootTheLoginTakes(t *testing.T) {
	// ROU2_BASE_URL is shared with figaro login rou2, which takes the bare
	// root; a bare root must not send chat to <root>/chat/completions (404).
	for in, want := range map[string]string{
		"http://100.103.40.20:9110":               "http://100.103.40.20:9110/v1",
		"http://100.103.40.20:9110/":              "http://100.103.40.20:9110/v1",
		"http://100.103.40.20:9110/v1":            "http://100.103.40.20:9110/v1",
		"http://100.103.40.20:9110/u/explabs/v1/": "http://100.103.40.20:9110/u/explabs/v1",
	} {
		if got := rou2Door(in); got != want {
			t.Errorf("rou2Door(%q) = %q, want %q", in, got, want)
		}
	}
}
