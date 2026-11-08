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

// :ls opens the attended figaro wherever it sits, a fork of a top-level
// figaro or a fork of that fork: its forks, then its own form, with system
// folded, while the figaros above it open only far enough to reach it.
func TestLsOpensTheAttendedForkWithItsForm(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns daemons; skipped under -short")
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

	for _, tc := range []struct {
		depth            string
		attended, fork   string
		mantra, notShown string
	}{
		{"a fork of a top-level figaro", mid, leaf, "the middle", `mantra:  "the top"`},
		{"a fork of that fork", leaf, kid, "the leaf", `mantra:  "the middle"`},
	} {
		t.Run(tc.depth, func(t *testing.T) {
			p := newPane(t, r.env, r.bin, 110, 40)
			p.send(r.bin + " attend " + tc.attended + " && " + r.bin + " listen")
			p.key("Enter")
			waitVisible(t, p, "the pager never painted", "live")
			p.send(":ls")
			p.key("Enter")
			waitVisible(t, p, ":ls never opened", name(tc.attended))
			p.key("F")
			screen := waitVisible(t, p, ":ls did not open the attended figaro's form",
				`mantra:  "`+tc.mantra+`"`, "system (")
			t.Logf("%s:\n%s", tc.depth, screen)
			at, fork := lineWith(screen, name(tc.attended)), lineWith(screen, name(tc.fork))
			form := lineWith(screen, `mantra:  "`+tc.mantra+`"`)
			if !strings.Contains(strings.Split(screen, "\n")[at], "●") {
				t.Errorf("the row for %s is not the marked one", name(tc.attended))
			}
			if !(at < fork && fork < form) {
				t.Errorf("want %s, then its fork %s, then its form: rows %d, %d, %d", name(tc.attended), name(tc.fork), at, fork, form)
			}
			if tc.attended == leaf && !strings.Contains(screen, "plan") {
				t.Errorf("the leaf's own keys are missing")
			}
			if strings.Contains(screen, tc.notShown) {
				t.Errorf("an ancestor's form is drawn (%s)", tc.notShown)
			}
		})
	}
}
