package authz

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/jkauthz"
)

func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func reloadText(t *testing.T, text string) (*Policies, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}, nil)
	return p, p.Reload(path)
}

const oldNamedPolicy = `
[figaros]
grants = ["read:*"]

[roles.owner]
grants = ["*:*"]

[roles.reader]
permissions = ["read:*"]

[[bind]]
role = "owner"
principals = ["user:1000"]
`

func TestAPolicyUsingTheOldNameLoadsAndWarnsOncePerUse(t *testing.T) {
	buf := captureWarnings(t)
	p, err := reloadText(t, oldNamedPolicy)
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if n := strings.Count(out, "level=WARN"); n != 2 {
		t.Fatalf("%d warnings:\n%s", n, out)
	}
	for _, want := range []string{"write [roles.owner] permissions", "write [figaros] permissions"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	if !p.Allow(jkauthz.Subject{Principal: jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}}, "kill", Daemon).Allowed {
		t.Error("the old name lost the role's permissions")
	}
	if ceiling, set := p.ceiling(); !set || ceiling.String() != "read:*" {
		t.Errorf("ceiling %v set %v", ceiling, set)
	}
	buf.Reset()
	if err := p.Reload(p.path); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(buf.String(), "level=WARN"); n != 2 {
		t.Errorf("a second load warns again, once per use: %d", n)
	}
}

func TestAPolicyUsingOnlyTheNewNameWarnsOfNothing(t *testing.T) {
	buf := captureWarnings(t)
	if _, err := reloadText(t, strings.ReplaceAll(oldNamedPolicy, "grants =", "permissions =")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "level=WARN") {
		t.Fatal(buf.String())
	}
}

func TestBothNamesInOneTableAreRefusedNamingTheConflict(t *testing.T) {
	for name, text := range map[string]string{
		"role":      "[roles.owner]\npermissions = [\"*:*\"]\ngrants = [\"*:*\"]\n\n[[bind]]\nrole = \"owner\"\nprincipals = [\"user:1000\"]\n",
		"[figaros]": "[figaros]\npermissions = [\"*:*\"]\ngrants = [\"*:*\"]\n\n[roles.owner]\npermissions = [\"*:*\"]\n",
	} {
		t.Run(name, func(t *testing.T) {
			p, err := reloadText(t, text)
			if err == nil || !strings.Contains(err.Error(), "both permissions and grants") {
				t.Fatalf("want the conflict named, got %v", err)
			}
			if p.Allow(jkauthz.Subject{Principal: jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}}, "read", Daemon).Allowed {
				t.Fatal("a refused policy allowed a call")
			}
		})
	}
}

func TestATroupePolicyUsingTheOldNameLoadsAndWarns(t *testing.T) {
	buf := captureWarnings(t)
	p := NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}, nil)
	body := "[roles.member]\ngrants = [\"read:*\"]\n\n[[bind]]\nrole = \"member\"\nprincipals = [\"user:bob@auth.kelliher.info\"]\n"
	if err := p.SetTroupe("kelliher", []byte(body)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "troupe policy says [roles.member] grants") {
		t.Fatal(buf.String())
	}
	both := strings.Replace(body, "grants = [\"read:*\"]", "grants = [\"read:*\"]\npermissions = [\"read:*\"]", 1)
	if err := p.SetTroupe("kelliher", []byte(both)); err == nil || !strings.Contains(err.Error(), "both permissions and grants") {
		t.Fatalf("want the conflict named, got %v", err)
	}
}

func TestTheTroupeOnlyRulesHoldUnderTheOldName(t *testing.T) {
	for _, name := range []string{"permissions", "grants"} {
		body := "[roles.s]\n" + name + " = [\"use:sandbox:local\"]\n"
		if err := CheckTroupeDocument([]byte(body), "kelliher", nil); err == nil || !strings.Contains(err.Error(), "sandbox:local") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
