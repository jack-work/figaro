package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// A CLI that autostarts the daemon must not leave it in its own cgroup: under
// a transient unit (an update, a figla reminder) systemd kills the daemon when
// the unit ends. These pin when the daemon gets a scope of its own, and that
// a dev shell's daemon never shares the live one's scope name.

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func managerAt(socket string) func(string) bool {
	return func(p string) bool { return p == socket }
}

func TestTheDaemonStaysWhereItIsWithoutAUserManager(t *testing.T) {
	present := managerAt("/run/user/1000/systemd/private")
	cases := map[string]struct {
		env    map[string]string
		exists func(string) bool
	}{
		"no runtime dir, as in a container or CI": {map[string]string{}, present},
		"a runtime dir with no manager socket":    {map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, managerAt("")},
		"asked not to, as every test run is": {
			map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000", "FIGARO_NO_ANGELUS_SCOPE": "1"}, present,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if p, ok := placeAngelus(envOf(c.env), c.exists, "/run/user/1000/figaro"); ok {
				t.Fatalf("placed the daemon in %+v; with no user manager it must keep today's setsid alone", p)
			}
		})
	}
}

func TestTheDaemonGetsAScopeOfItsOwnWhenAUserManagerAnswers(t *testing.T) {
	socket := "/run/user/1000/systemd/private"
	p, ok := placeAngelus(envOf(map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}), managerAt(socket), "/run/user/1000/figaro")
	if !ok {
		t.Fatal("a reachable user manager was not used")
	}
	if p.managerSocket != socket {
		t.Fatalf("manager socket %q, want %q", p.managerSocket, socket)
	}
	name := p.scopeFor(4242)
	if !strings.HasPrefix(name, "figaro-angelus-") || !strings.HasSuffix(name, "-4242.scope") || strings.Contains(name, "-dev-") {
		t.Fatalf("scope %q, want figaro-angelus-<instance>-4242.scope", name)
	}
}

func TestADevShellsDaemonNeverSharesTheLiveOnesScopeName(t *testing.T) {
	exists := managerAt(filepath.Join("/run/user/1000", "systemd", "private"))
	live, _ := placeAngelus(envOf(map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}), exists, "/run/user/1000/figaro")
	dev, _ := placeAngelus(envOf(map[string]string{
		"XDG_RUNTIME_DIR": "/run/user/1000", "FIGARO_DEV_ROOT": "/var/tmp/figaro-dev-x",
	}), exists, "/var/tmp/figaro-dev-x/run")
	other, _ := placeAngelus(envOf(map[string]string{
		"XDG_RUNTIME_DIR": "/run/user/1000", "FIGARO_DEV_ROOT": "/var/tmp/figaro-dev-y",
	}), exists, "/var/tmp/figaro-dev-y/run")
	if !strings.HasPrefix(dev.scopePrefix, "figaro-angelus-dev-") {
		t.Fatalf("a dev shell's scope %q does not say it is one", dev.scopePrefix)
	}
	if live.scopePrefix == dev.scopePrefix || dev.scopePrefix == other.scopePrefix {
		t.Fatalf("scopes collide: live %q, dev %q, other dev %q", live.scopePrefix, dev.scopePrefix, other.scopePrefix)
	}
}
