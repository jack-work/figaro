//go:build linux

package cli

import (
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func (r *restartRig) forksOf(id string) []string {
	r.t.Helper()
	out, err := r.cmd("ls", "-j").Output()
	if err != nil {
		r.t.Fatalf("ls -j: %v", err)
	}
	var listing []struct {
		ID     string `json:"id"`
		Parent string `json:"parent"`
	}
	if err := json.Unmarshal(out, &listing); err != nil {
		r.t.Fatalf("ls -j: %v\n%s", err, out)
	}
	var forks []string
	for _, f := range listing {
		if f.Parent == id {
			forks = append(forks, f.ID)
		}
	}
	return forks
}

// Typed into the pager's command line: :fork -f forks and stays, with or
// without a prompt; :send -f sends; :fork without -f attends the branch.
func TestPagerFMeansStay(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns daemons; skipped under -short")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	prov := newScriptedProvider(t, func(string) (bool, string) { return false, sseText("noted") })
	r := newRestartRig(t)
	r.env = append(r.env, "ANTHROPIC_BASE_URL="+prov.srv.URL)
	top := r.newEmptyAria()

	p := newPane(t, r.env, r.bin, 110, 40)
	shell := smokeAtoi(strings.TrimSpace(p.tmuxOut("display", "-p", "-t", "smoke:0", "#{pane_pid}")))
	p.send(r.bin + " attend " + top + " && " + r.bin + " listen")
	p.key("Enter")
	waitVisible(t, p, "the pager never painted", "live")
	command := func(line string) {
		t.Helper()
		p.send(":" + line)
		p.key("Enter")
	}
	newFork := func(known []string, why string) string {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			for _, id := range r.forksOf(top) {
				if !slices.Contains(known, id) {
					return id
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: no new fork of %s:\n%s", why, top, p.visible())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	stillOn := func(why string) {
		t.Helper()
		time.Sleep(300 * time.Millisecond)
		if got := r.boundTo(shell); got != top {
			t.Fatalf("%s: the shell is attended to %q, want it to stay on %s:\n%s", why, got, top, p.visible())
		}
	}

	command("fork -f")
	bare := newFork(nil, ":fork -f")
	stillOn(":fork -f")

	asks := prov.count()
	command("fork -f -- branch and stay")
	prompted := newFork([]string{bare}, ":fork -f -- <prompt>")
	prov.awaitAsk(t, asks, 30*time.Second, "branch and stay")
	stillOn(":fork -f -- <prompt>")

	asks = prov.count()
	command("send -f -- sent with f")
	prov.awaitAsk(t, asks, 30*time.Second, "sent with f")
	stillOn(":send -f")

	asks = prov.count()
	command("fork -- branch and follow")
	moved := newFork([]string{bare, prompted}, ":fork -- <prompt>")
	prov.awaitAsk(t, asks, 30*time.Second, "branch and follow")
	deadline := time.Now().Add(15 * time.Second)
	for r.boundTo(shell) != moved {
		if time.Now().After(deadline) {
			t.Fatalf(":fork did not attend the branch %s (bound to %q):\n%s", moved, r.boundTo(shell), p.visible())
		}
		time.Sleep(100 * time.Millisecond)
	}
}
