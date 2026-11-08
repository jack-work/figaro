//go:build linux

package cli

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/recent"
)

// Alt+l, sent as the two bytes a terminal writes for it (ESC l), opens
// @recent over the pager: one row per figaro, the most recently active first
// whatever order they were made in, each led by its handle's first word. The
// row's Enter attends it.
func TestAltLOpensRecentNewestFirstAndEnterAttends(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns daemons; skipped under -short")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	prov := newScriptedProvider(t, func(string) (bool, string) { return false, sseText("noted") })
	r := newRestartRig(t)
	r.env = append(r.env, "ANTHROPIC_BASE_URL="+prov.srv.URL)
	made := r.newEmptyAria()
	later := r.newEmptyAria()
	r.run("set", "--id", later, "mantra", "made later, touched first")
	r.run("set", "--id", made, "mantra", "made first, touched last")
	r.run("send", "--id", later, "--", "first")
	r.run("send", "--id", made, "--", "last")
	word := func(id string) string { return recent.FirstWord(r.ariaField(id, "handle")) }

	p := newPane(t, r.env, r.bin, 100, 30)
	shell := smokeAtoi(strings.TrimSpace(p.tmuxOut("display", "-p", "-t", "smoke:0", "#{pane_pid}")))
	p.send(r.bin + " listen " + later)
	p.key("Enter")
	waitVisible(t, p, "the pager never painted", "live")

	p.tmux("send-keys", "-H", "1b", "6c")
	screen := waitVisible(t, p, "M-l never opened @recent", "touched last", "touched first")
	top, below := lineWith(screen, "touched last"), lineWith(screen, "touched first")
	if top < 0 || below < 0 || top > below {
		t.Fatalf("want the figaro touched last above the one made later:\n%s", screen)
	}
	t.Logf("@recent:\n%s", screen)
	lines := strings.Split(screen, "\n")
	for _, at := range []struct {
		line int
		id   string
	}{{top, made}, {below, later}} {
		if got := strings.TrimLeft(lines[at.line], " ▸"); !strings.HasPrefix(got, word(at.id)) {
			t.Errorf("row %q does not lead with %s's first word %q", got, at.id, word(at.id))
		}
	}

	p.key("k")
	p.key("Enter")
	deadline := time.Now().Add(15 * time.Second)
	for r.boundTo(shell) != made {
		if time.Now().After(deadline) {
			t.Fatalf("Enter on the top row did not attend %s (bound to %q):\n%s", made, r.boundTo(shell), p.visible())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func waitVisible(t *testing.T, p *pane, why string, texts ...string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		screen := p.visible()
		all := true
		for _, s := range texts {
			all = all && strings.Contains(screen, s)
		}
		if all {
			return screen
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s:\n%s", why, screen)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func lineWith(screen, text string) int {
	for i, line := range strings.Split(screen, "\n") {
		if strings.Contains(line, text) {
			return i
		}
	}
	return -1
}

// figaro recent prints what the pit shows: newest activity first whatever the
// order of birth, led by each handle's first word; -n keeps the newest and -j
// gives the same rows as JSON.
func TestFigaroRecentListsNewestFirst(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns daemons; skipped under -short")
	}
	prov := newScriptedProvider(t, func(string) (bool, string) { return false, sseText("noted") })
	r := newRestartRig(t)
	r.env = append(r.env, "ANTHROPIC_BASE_URL="+prov.srv.URL)
	made := r.newEmptyAria()
	later := r.newEmptyAria()
	never := r.newEmptyAria()
	r.run("send", "--id", later, "--", "first")
	r.run("send", "--id", made, "--", "last")

	var rows []recent.Row
	if err := json.Unmarshal([]byte(r.run("recent", "-j")), &rows); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, row := range rows {
		ids = append(ids, row.ID)
		if want := recent.FirstWord(r.ariaField(row.ID, "handle")); !strings.HasPrefix(row.Name, want) {
			t.Errorf("%s is named %q, want its first word %q", row.ID, row.Name, want)
		}
	}
	if got, want := strings.Join(ids, ","), strings.Join([]string{made, later, never}, ","); got != want {
		t.Fatalf("recent -j order %s, want %s: last touched first, never prompted last", got, want)
	}
	if rows[0].LastActive <= rows[1].LastActive || rows[1].LastActive <= rows[2].LastActive {
		t.Errorf("last_active = %d, %d, %d", rows[0].LastActive, rows[1].LastActive, rows[2].LastActive)
	}

	text := strings.Split(strings.TrimSpace(r.run("recent", "-n", "2")), "\n")
	if len(text) != 2 || !strings.HasPrefix(text[0], rows[0].Name+" ") || !strings.HasPrefix(text[1], rows[1].Name+" ") {
		t.Fatalf("recent -n 2 printed:\n%s", strings.Join(text, "\n"))
	}
}
