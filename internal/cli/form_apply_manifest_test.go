package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "roles.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRoleManifestDefaultsToCheckAndOrdersByName(t *testing.T) {
	path := writeManifest(t, `
[roles.zeta]
id = "@22222222222222222222222222222222"
outfits = ["role", "extra"]
managed = ["charter.purpose"]
mode = "apply"

[roles.alpha]
id = "@11111111"
outfits = ["role"]
managed = ["charter"]
`)
	roles, err := loadRoleManifest(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 2 || roles[0].name != "alpha" || roles[1].name != "zeta" {
		t.Fatalf("roles not in name order: %+v", roles)
	}
	if !roles[0].check || roles[1].check {
		t.Fatalf("an undeclared mode must check and only apply may write: %+v", roles)
	}
	if got := strings.Join(roles[1].request.Outfits, ","); got != "role,extra" {
		t.Fatalf("outfit order lost: %s", got)
	}
	forced, err := loadRoleManifest(path, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range forced {
		if !role.check {
			t.Fatalf("--check must override a declared apply mode: %+v", role)
		}
	}
}

func TestRoleManifestRefusesAnythingThatCouldTargetTheWrongRole(t *testing.T) {
	for name, body := range map[string]string{
		"empty":        ``,
		"prefix":       "[roles.a]\nid = \"@1111111111\"\noutfits = [\"r\"]\nmanaged = [\"charter\"]\n",
		"short-id":     "[roles.a]\nid = \"@1111111111111111111111111111111\"\noutfits = [\"r\"]\nmanaged = [\"charter\"]\n",
		"name":         "[roles.a]\nid = \"engineer\"\noutfits = [\"r\"]\nmanaged = [\"charter\"]\n",
		"no-at":        "[roles.a]\nid = \"11111111\"\noutfits = [\"r\"]\nmanaged = [\"charter\"]\n",
		"no-outfits":   "[roles.a]\nid = \"@11111111111111111111111111111111\"\noutfits = []\nmanaged = [\"charter\"]\n",
		"no-managed":   "[roles.a]\nid = \"@11111111111111111111111111111111\"\noutfits = [\"r\"]\n",
		"bad-mode":     "[roles.a]\nid = \"@11111111111111111111111111111111\"\noutfits = [\"r\"]\nmanaged = [\"charter\"]\nmode = \"create\"\n",
		"unknown-key":  "[roles.a]\nid = \"@11111111111111111111111111111111\"\noutfits = [\"r\"]\nmanaged = [\"charter\"]\nholder = \"x\"\n",
		"duplicate-id": "[roles.a]\nid = \"@11111111111111111111111111111111\"\noutfits = [\"r\"]\nmanaged = [\"charter\"]\n[roles.b]\nid = \"@11111111111111111111111111111111\"\noutfits = [\"r\"]\nmanaged = [\"charter\"]\n",
	} {
		t.Run(name, func(t *testing.T) {
			if roles, err := loadRoleManifest(writeManifest(t, body), false); err == nil {
				t.Fatalf("accepted: %+v", roles)
			}
		})
	}
}
