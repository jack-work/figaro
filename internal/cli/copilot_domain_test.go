package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/jack-work/figaro/internal/config"
)

// Fixes #53: switching from a GitHub Enterprise login back to github.com
// must clear the old domain and keep the provider file's other keys.
func TestCopilotDomainSwitchKeepsTheFileAndClearsTheDomain(t *testing.T) {
	dir := t.TempDir()
	loaded, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := loaded.ProviderAuthPath("copilot")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("token_mode = \"exchange\"\nbase_url = \"https://example.invalid\"\nenterprise_domain = \"corp.ghe.com\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	read := func() map[string]any {
		t.Helper()
		got := map[string]any{}
		if _, err := toml.DecodeFile(path, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	if err := writeCopilotDomain(loaded, ""); err != nil {
		t.Fatal(err)
	}
	got := read()
	if _, stale := got["enterprise_domain"]; stale {
		t.Fatalf("a blank domain kept the old one: %v", got)
	}
	if got["token_mode"] != "exchange" || got["base_url"] != "https://example.invalid" {
		t.Fatalf("the provider file lost its other keys: %v", got)
	}

	if err := writeCopilotDomain(loaded, "other.ghe.com"); err != nil {
		t.Fatal(err)
	}
	got = read()
	if got["enterprise_domain"] != "other.ghe.com" || got["token_mode"] != "exchange" {
		t.Fatalf("setting a domain dropped keys or did not set it: %v", got)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v err %v", info.Mode().Perm(), err)
	}
}
