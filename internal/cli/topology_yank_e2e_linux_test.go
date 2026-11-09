//go:build linux

package cli

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// y and M-y in :ls reach the terminal's clipboard (OSC 52, which tmux keeps
// as a paste buffer): the attended fork's handle, then its full id.
func TestLsYankSendsTheHandleThenTheIDToTheClipboard(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns daemons; skipped under -short")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	r := newRestartRig(t)
	top := r.newEmptyAria()
	mid := r.forkOf(top)
	handle := r.ariaField(mid, "handle")

	p := newPane(t, r.env, r.bin, 110, 40)
	p.tmux("set", "-s", "set-clipboard", "on")
	p.send(r.bin + " attend " + mid + " && " + r.bin + " listen")
	p.key("Enter")
	waitVisible(t, p, "the pager never painted", "live")
	p.send(":ls")
	p.key("Enter")
	waitVisible(t, p, ":ls never opened", handle+" (")
	clipboard := func(want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			got := strings.TrimSpace(p.tmuxOut("show-buffer"))
			if got == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("clipboard %q, want %q:\n%s", got, want, p.visible())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	p.key("y")
	clipboard(handle)
	p.key("M-y")
	clipboard(mid)
}
