//go:build linux

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (r *restartRig) outfit(name, body string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.root, "config", "outfits", name+".toml"), []byte(body), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *restartRig) key(id, key string) string {
	r.t.Helper()
	out, _ := r.cmd("get", "--id", id, key).Output()
	return strings.TrimSpace(string(out))
}

// Gluck, 11-20: reroute is the one verb that moves a route from outfits.
func TestRerouteBringsAFigaroBackToItsOutfit(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	r := newRestartRig(t)
	id := r.newEmptyAria()
	r.run("set", "--id", id, "system.model", "runtime-model")
	r.run("set", "--id", id, "system.thinking_effort", "high")
	r.outfit("default", "[system]\nprovider = \"anthropic\"\nmodel = \"claude-opus-4-5\"\n")
	r.run("state", "outfit", "--refresh")
	if got := r.key(id, "system.model"); got != "runtime-model" {
		t.Fatalf("an outfit refresh moved the model to %q", got)
	}
	out := r.run("reroute", "--id", id)
	if !strings.Contains(out, "anthropic/claude-opus-4-5") {
		t.Fatalf("reroute did not say where it went: %s", out)
	}
	if got := r.key(id, "system.model"); got != "claude-opus-4-5" {
		t.Fatalf("reroute left the model at %q", got)
	}
	if got := r.key(id, "system.thinking_effort"); got != "" {
		t.Fatalf("reroute kept an effort the outfit does not name: %q", got)
	}
	r.outfit("fast", "[system]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-4-5\"\nthinking_effort = \"low\"\n")
	r.run("reroute", "--id", id, "-O", "fast")
	if got := r.key(id, "system.model") + " " + r.key(id, "system.thinking_effort"); got != "claude-haiku-4-5 low" {
		t.Fatalf("reroute -O fast landed on %q", got)
	}
}

func TestRerouteOfARoleMovesItsHolderOntoTheRolesRoute(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	r := newRestartRig(t)
	r.outfit("seat", "[system]\nprovider = \"anthropic\"\nmodel = \"claude-opus-4-5\"\nthinking_effort = \"low\"\n")
	holder := r.newEmptyAria()
	role := r.roleFor(holder, "warden")
	r.run("set", "--id", role, "succession-policy.outfit", "seat")
	r.run("set", "--id", role, "system.thinking_effort", "high")
	r.run("set", "--id", holder, "system.model", "runtime-model")
	out := r.run("reroute", "--id", role)
	if !strings.Contains(out, "role") {
		t.Fatalf("reroute did not name the role: %s", out)
	}
	if got := r.key(holder, "system.model") + " " + r.key(holder, "system.thinking_effort"); got != "claude-opus-4-5 high" {
		t.Fatalf("the holder landed on %q, want the role's outfit under the role's own route keys", got)
	}
}

// review-lane-usability-1 #1: the owner reroutes, and so does a figaro
// holding whole set on the target. Keyed set is not enough, not even a
// default lane's on itself.
func TestRerouteIsForTheOwnerAndAFigaroHoldingWholeSet(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	r := newRestartRig(t)
	r.enforceAll()
	parent := r.newEmptyAria()
	r.run("set", "--id", parent, "system.model", "runtime-model")
	r.run("set", "--id", parent, "system.thinking_effort", "high")
	lane := r.laneOf(parent)
	scopes := func(extra string) {
		r.run("set", "--id", lane, "system.scopes", `["send:parent","read:aria:`+parent+`","read:aria:self","write:aria:self#@self"`+extra+`]`)
	}
	refused := func(what, target string) {
		t.Helper()
		if out, err := r.as(lane, "reroute", "--id", target); err == nil {
			t.Fatalf("%s: rerouted: %s", what, out)
		}
	}
	scopes(`,"write:aria:` + parent + `#mantra"`)
	refused("keyed set on the parent", parent)
	refused("a default lane on itself", lane)
	if got := r.key(parent, "system.model"); got != "runtime-model" {
		t.Fatalf("a refused reroute moved the model to %q", got)
	}
	scopes(`,"write:aria:` + parent + `"`)
	if out, err := r.as(lane, "reroute", "--id", parent); err != nil {
		t.Fatalf("a lane holding whole set on its parent could not reroute it: %v\n%s", err, out)
	}
	if got := r.key(parent, "system.model") + "|" + r.key(parent, "system.thinking_effort"); got != "claude-sonnet-4-5|" {
		t.Fatalf("the delegated reroute landed on %q", got)
	}
}

// review-lane-usability-1 #3: a figaro born on the configured default wears
// its name, default_kic here; a delegate holding whole set reroutes it
// without a grant for that outfit, since only the route is read.
func TestADelegateReroutesAFigaroBornOnANamedDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	r := newRestartRig(t)
	r.enforceAll()
	r.outfit("default_kic", "[system]\nprovider = \"anthropic\"\nmodel = \"claude-opus-4-5\"\n")
	config := filepath.Join(r.root, "config", "config.toml")
	raw, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(strings.Replace(string(raw), `default_outfit = "default"`, `default_outfit = "default_kic"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := r.newEmptyAria()
	if got := r.key(parent, "system.outfit_name"); got != "default_kic" {
		t.Fatalf("canary: born wearing %q", got)
	}
	r.run("set", "--id", parent, "system.model", "runtime-model")
	lane := r.laneOf(parent)
	r.run("set", "--id", lane, "system.scopes", `["send:parent","read:aria:`+parent+`","write:aria:`+parent+`","read:aria:self","write:aria:self#@self"]`)
	if out, err := r.as(lane, "reroute", "--id", parent); err != nil {
		t.Fatalf("a whole-set delegate could not reroute a default_kic figaro: %v\n%s", err, out)
	}
	if got := r.key(parent, "system.model"); got != "claude-opus-4-5" {
		t.Fatalf("landed on %q", got)
	}
}

// review-lane-usability-1 #7: -O on a figaro that keeps its route says so,
// and names the verb that would move it.
func TestDressingARoutedFigaroSaysTheRouteStayed(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	r := newRestartRig(t)
	id := r.newEmptyAria()
	r.outfit("fast", "[system]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-4-5\"\n")
	want := "figaro reroute -O fast"
	if out := r.run("state", "outfit", "--id", id, "fast"); !strings.Contains(out, want) {
		t.Fatalf("state outfit said nothing of the kept route:\n%s", out)
	}
	if out, _ := r.cmd("-O", "fast", "--id", id, "--", "hi").CombinedOutput(); !strings.Contains(string(out), want) {
		t.Fatalf("a prompt with -O said nothing of the kept route:\n%s", out)
	}
	r.outfit("same", "[system]\nprovider = \"anthropic\"\nmodel = \"claude-sonnet-4-5\"\n")
	if out := r.run("state", "outfit", "--id", id, "same"); strings.Contains(out, "reroute") {
		t.Fatalf("an outfit on the route the figaro already has drew the note:\n%s", out)
	}
}
