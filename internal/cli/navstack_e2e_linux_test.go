//go:build linux

package cli

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ctrl+o, Tab and the ':' verbs walk this shell's history through real keys,
// in a real pager over a real daemon. It defends the bugs the pager-local
// jumplist had: forward past a revisited figaro, fast keys that moved one step,
// a stack that died with the pager, and a killed figaro that was still landed on.
func TestThePagerWalksTheShellsHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns daemons; skipped under -short")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	r := newRestartRig(t)
	a, b, c, d, e := r.mint(), r.mint(), r.mint(), r.mint(), r.mint()

	p := newPane(t, r.env, r.bin, 120, 30)
	shell := smokeAtoi(strings.TrimSpace(p.tmuxOut("display", "-p", "-t", "smoke:0", "#{pane_pid}")))
	p.send(r.bin + " attend " + a + " && " + r.bin)
	p.key("Enter")

	waitFor := func(what string, ok func(screen string) bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for !ok(p.visible()) {
			if time.Now().After(deadline) {
				t.Fatalf("never: %s\n%s", what, p.visible())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	at := func(id, where string) {
		t.Helper()
		waitFor("attending "+ariaHandle(id)+" "+where, func(s string) bool {
			return r.boundTo(shell) == id && strings.Contains(s, "attending "+ariaHandle(id)+" "+where)
		})
	}
	waitFor("the pager paints", func(s string) bool { return strings.Contains(s, "live") })
	cmd := func(line string) {
		t.Helper()
		p.key("Escape")
		p.typeSlowly(":" + line)
		p.key("Enter")
	}

	cmd("attend " + b)
	at(b, "(2/2)")
	cmd("attend " + a)
	at(a, "(3/3)")
	cmd("attend " + c)
	at(c, "(4/4)")

	p.tmux("send-keys", "C-o", "C-o", "C-o")
	at(a, "(1/4)")

	p.key("Tab")
	at(b, "(2/4)")
	p.key("Tab")
	at(a, "(3/4)")
	p.key("Tab")
	at(c, "(4/4)")

	cmd("back 2")
	at(b, "(2/4)")
	cmd("aria-forward")
	at(a, "(3/4)")

	p.key("q")
	p.send(r.bin)
	p.key("Enter")
	waitFor("the pager paints again", func(s string) bool { return strings.Contains(s, "live") })
	p.key("C-o")
	at(b, "(2/4)")
	p.key("Tab")
	at(a, "(3/4)")

	cmd("attend " + d)
	at(d, "(4/4)")
	cmd("attend " + e)
	at(e, "(5/5)")
	r.run("kill", d)
	p.key("C-o")
	at(a, "(3/4)")
	if !strings.Contains(p.visible(), "passed over "+ariaHandle(d)) {
		t.Fatalf("the hop did not say it passed over the killed %s:\n%s", ariaHandle(d), p.visible())
	}
}

// A form is a place in the history like any figaro: ctrl+o and Tab land on it.
// Defends the pager refusing to hop onto a form ("is a form, not a figaro")
// after the daemon had already moved the shell.
func TestThePagerWalksOntoAForm(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns daemons; skipped under -short")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	r := newRestartRig(t)
	a, b := r.mint(), r.mint()
	var made struct {
		FormID string `json:"form_id"`
	}
	out := r.run("form", "new", "-j", "name=navtest", "canary=walked-onto-the-form")
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &made); err != nil || made.FormID == "" {
		t.Fatalf("form new -j printed no id: %q", out)
	}
	f := made.FormID

	p := newPane(t, r.env, r.bin, 120, 30)
	shell := smokeAtoi(strings.TrimSpace(p.tmuxOut("display", "-p", "-t", "smoke:0", "#{pane_pid}")))
	p.send(r.bin + " attend " + a + " && " + r.bin)
	p.key("Enter")

	waitFor := func(what string, ok func(screen string) bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for !ok(p.visible()) {
			if time.Now().After(deadline) {
				t.Fatalf("never: %s\n%s", what, p.visible())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	onForm := func(where string) {
		t.Helper()
		waitFor("attending the form "+where, func(s string) bool {
			return r.boundTo(shell) == f && strings.Contains(s, "attending "+ariaName(f)+" "+where)
		})
	}
	onFigaro := func(id, where string) {
		t.Helper()
		waitFor("attending "+ariaHandle(id)+" "+where, func(s string) bool {
			return r.boundTo(shell) == id && strings.Contains(s, "attending "+ariaHandle(id)+" "+where)
		})
	}
	waitFor("the pager paints", func(s string) bool { return strings.Contains(s, "live") })
	cmd := func(line string) {
		t.Helper()
		p.key("Escape")
		p.typeSlowly(":" + line)
		p.key("Enter")
	}

	cmd("attend " + f)
	onForm("(2/2)")
	cmd("attend " + b)
	onFigaro(b, "(3/3)")

	p.key("C-o")
	onForm("(2/3)")
	p.key("C-o")
	onFigaro(a, "(1/3)")
	p.key("Tab")
	onForm("(2/3)")
	p.key("Tab")
	onFigaro(b, "(3/3)")
}
