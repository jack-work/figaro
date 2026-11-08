//go:build linux

package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// `:new` typed into the pager mints an aria from the default outfit, or the
// outfits and keys it names, attends it, and shows it in the same pager.
func TestPagerNewMintsAttendsAndShowsAnAria(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns daemons; skipped under -short")
	}
	r := newRestartRig(t)
	if err := os.WriteFile(filepath.Join(r.root, "config/outfits/other.toml"),
		[]byte("[system]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-4-5\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := r.mint()

	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	p := newPane(t, r.env, r.bin, 100, 30)
	shell := smokeAtoi(strings.TrimSpace(p.tmuxOut("display", "-p", "-t", "smoke:0", "#{pane_pid}")))
	p.send(r.bin + " listen " + first)
	p.key("Enter")
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(p.visible(), "live") {
		if time.Now().After(deadline) {
			t.Fatalf("the pager never painted:\n%s", p.visible())
		}
		time.Sleep(100 * time.Millisecond)
	}

	attended := func(not ...string) string {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			id := r.boundTo(shell)
			fresh := id != ""
			for _, n := range not {
				fresh = fresh && id != n
			}
			if fresh {
				return id
			}
			if time.Now().After(deadline) {
				t.Fatalf("the pager never attended a new aria (bound to %q):\n%s", id, p.visible())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	shows := func(id string) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !strings.Contains(p.visible(), ariaHandle(id)) {
			if time.Now().After(deadline) {
				t.Fatalf("the pager never showed %s:\n%s", ariaHandle(id), p.visible())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	p.typeSlowly(":new")
	p.key("Enter")
	second := attended(first)
	if model := r.ariaModel(second); model != "claude-sonnet-4-5" {
		t.Fatalf(":new dressed %s in %q, want the default outfit's model", second, model)
	}
	shows(second)

	p.typeSlowly(":new -O other -S mantra=elsewhere")
	p.key("Enter")
	third := attended(first, second)
	if model := r.ariaModel(third); model != "claude-haiku-4-5" {
		t.Fatalf(":new -O other dressed %s in %q", third, model)
	}
	if mantra := r.ariaMantra(third); mantra != "elsewhere" {
		t.Fatalf(":new -S mantra=elsewhere gave mantra %q", mantra)
	}
	shows(third)
}

func (r *restartRig) ariaField(id, field string) string {
	r.t.Helper()
	out, err := r.cmd("ls", "-j").Output()
	if err != nil {
		r.t.Fatalf("ls -j: %v", err)
	}
	var listing []map[string]any
	if err := json.Unmarshal(out, &listing); err != nil {
		r.t.Fatalf("ls -j: %v\n%s", err, out)
	}
	for _, f := range listing {
		if f["id"] == id {
			s, _ := f[field].(string)
			return s
		}
	}
	r.t.Fatalf("%s is not listed", id)
	return ""
}

func (r *restartRig) ariaModel(id string) string  { return r.ariaField(id, "model") }
func (r *restartRig) ariaMantra(id string) string { return r.ariaField(id, "mantra") }
