//go:build linux

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Gluck, 11-20: a default outfit that does not resolve never starts the
// setup wizard; figaro new fails with one line naming the outfit and the fix.
func TestNewWithAnUnresolvableDefaultOutfitFailsInOneLine(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	r := newRestartRig(t)
	config := filepath.Join(r.root, "config", "config.toml")
	cases := map[string]struct {
		config, outfit string
		want           []string
	}{
		"missing":     {"default_outfit = \"default_kic\"\n", "", []string{`"default_kic"`, "does not exist", config}},
		"no provider": {"default_outfit = \"bare\"\n", "[system]\nmodel = \"m\"\n", []string{`"bare"`, "names no system.provider", config}},
		"none named":  {"", "", []string{"no default_outfit", config}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(config, []byte(c.config), 0o600); err != nil {
				t.Fatal(err)
			}
			_ = os.Remove(filepath.Join(r.root, "config", "outfits", "default.toml"))
			if c.outfit != "" {
				r.outfit("bare", c.outfit)
			}
			r.run("state", "outfit", "--refresh")
			out, err := r.cmd("new", "-j").CombinedOutput()
			if err == nil {
				t.Fatalf("new succeeded: %s", out)
			}
			text := strings.TrimSpace(string(out))
			if strings.Contains(text, "\n") {
				t.Fatalf("more than one line:\n%s", text)
			}
			for _, w := range c.want {
				if !strings.Contains(text, w) {
					t.Fatalf("%q does not say %q", text, w)
				}
			}
			if strings.Contains(strings.ToLower(text), "setup") {
				t.Fatalf("the failure points at the wizard: %s", text)
			}
		})
	}
}
