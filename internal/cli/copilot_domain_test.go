package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/internal/config"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	_ "github.com/jack-work/figaro/internal/provider/copilot"
)

// Switching GHE -> github.com must clear the old host, or hush exchanges a
// github.com token at api.<ghe> and GitHub answers 401 Bad credentials.
func TestWriteCopilotDomainSwitchesHosts(t *testing.T) {
	dir := t.TempDir()
	loaded := &config.Loaded{ConfigDir: dir}
	path := loaded.ProviderAuthPath("copilot")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	seed := "enterprise_domain = \"microsoft.ghe.com\"\nbase_url = \"https://example.test\"\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	exchange := providerPkg.Lookup("copilot").ExchangeURL

	if got, want := exchange(loaded), "https://api.microsoft.ghe.com/copilot_internal/v2/token"; got != want {
		t.Fatalf("seeded exchange = %q, want %q", got, want)
	}

	if err := writeCopilotDomain(loaded, ""); err != nil {
		t.Fatal(err)
	}
	if got, want := exchange(loaded), "https://api.github.com/copilot_internal/v2/token"; got != want {
		t.Fatalf("after blank answer exchange = %q, want %q", got, want)
	}
	assertKey(t, loaded, "enterprise_domain", "")
	assertKey(t, loaded, "base_url", "https://example.test")

	if err := writeCopilotDomain(loaded, "acme.ghe.com"); err != nil {
		t.Fatal(err)
	}
	if got, want := exchange(loaded), "https://api.acme.ghe.com/copilot_internal/v2/token"; got != want {
		t.Fatalf("after enterprise answer exchange = %q, want %q", got, want)
	}
	assertKey(t, loaded, "base_url", "https://example.test")
}

func assertKey(t *testing.T, loaded *config.Loaded, key, want string) {
	t.Helper()
	cfg, err := readCopilotFile(loaded)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := cfg[key].(string)
	if got != want {
		t.Fatalf("%s = %q, want %q (file: %v)", key, got, want, cfg)
	}
}
