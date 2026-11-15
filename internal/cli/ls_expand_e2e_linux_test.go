//go:build linux

package cli

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func (r *restartRig) forkOf(id string) string {
	r.t.Helper()
	var out struct {
		Alternative string `json:"alternative"`
	}
	raw := r.run("fork", id, "-j")
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &out); err != nil || out.Alternative == "" {
		r.t.Fatalf("fork -j printed %q", raw)
	}
	return out.Alternative
}

func TestLsHierarchyAndMoreInTheTerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns isolated daemons")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	r := newRestartRig(t)
	top := r.newEmptyAria()
	r.run("set", "--id", top, "mantra", "the top")
	mid := r.forkOf(top)
	r.run("set", "--id", mid, "mantra", "the middle")
	leaf := r.forkOf(mid)
	r.run("set", "--id", leaf, "mantra", "the leaf")
	r.run("set", "--id", leaf, "plan.next", "write the test")
	kid := r.forkOf(leaf)
	name := func(id string) string { return r.ariaField(id, "handle") }
	names := map[string]string{}
	for _, id := range []string{top, mid, leaf, kid} {
		names[id] = name(id)
	}
	p := newPane(t, r.env, r.bin, 150, 48)
	p.tmux("resize-window", "-x", "150", "-y", "48")
	if h := strings.TrimSpace(p.tmuxOut("display", "-p", "#{pane_height}")); h != "48" {
		t.Fatalf("pane height %s, want 48", h)
	}
	p.send(r.bin + " attend " + leaf + " && " + r.bin + " listen")
	p.key("Enter")
	waitVisible(t, p, "pager never painted", "live")
	fullscreen := false
	for _, tc := range []struct {
		key, command string
		more         bool
	}{
		{key: "l"}, {key: "L", more: true},
		{command: "ls -H"}, {command: "ls -H --more", more: true},
		{command: "ls -a"}, {command: "ls -a --more", more: true},
		{command: "ls ^ -n 3"}, {command: "ls ^ -n 3 --more", more: true},
		{command: "ls -g"}, {command: "ls -g --more", more: true},
	} {
		label := tc.key + tc.command
		t.Run(label, func(t *testing.T) {
			if tc.key != "" {
				p.typeSlowly(tc.key)
			} else {
				p.typeSlowly(":" + tc.command)
				p.key("Enter")
			}
			waitVisible(t, p, label+" did not open", names[leaf])
			if !fullscreen {
				p.key("F")
				fullscreen = true
			}
			screen := waitVisible(t, p, label+" lost expanded fork", names[kid])
			if tc.more {
				screen = waitVisible(t, p, label+" did not show form details", "system (", "write the test")
			}
			if !strings.Contains(screen, "? help") && !strings.Contains(screen, "Enter attend") {
				t.Fatalf("no pager chrome:\n%s", screen)
			}
			at := lineWith(screen, names[leaf])
			row := strings.Split(screen, "\n")[at]
			if !strings.Contains(row, `"the leaf"`) || !strings.Contains(row, "●") {
				t.Errorf("attended row lost mantra/mark: %s", row)
			}
			if !tc.more && (strings.Contains(screen, "forks (") || strings.Contains(screen, "system (") || strings.Contains(screen, "mantra:")) {
				t.Errorf("hierarchy contains form keys:\n%s", screen)
			}
			if tc.more && !strings.Contains(screen, "forks (") {
				t.Errorf("full topology has no forks key:\n%s", screen)
			}
			t.Logf("%s, 150x48:\n%s", label, screen)
			p.key("Escape")
		})
	}
}
