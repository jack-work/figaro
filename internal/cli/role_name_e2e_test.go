package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// A role minted with name=figaro is typed as @figaro, on camera, at every verb
// that takes a role. Each verb here reaches the role, or the aria it points at,
// through that name alone.
func TestARoleIsAddressedByItsNameAtEveryVerb(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	rig := newSendRig(t, "http://127.0.0.1:9")
	target := rig.mint(t, "target")

	out, err := rig.cmd("form", "new", "-j", "name=figaro", "target-aria="+target).Output()
	if err != nil {
		t.Fatalf("form new: %v\n%s", err, out)
	}
	var minted struct {
		FormID string `json:"form_id"`
	}
	if err := json.Unmarshal(lastLine(out), &minted); err != nil || minted.FormID == "" {
		t.Fatalf("form new -j printed no form id: %q", out)
	}

	run := func(args ...string) string {
		t.Helper()
		out, err := rig.cmd(args...).CombinedOutput()
		if err != nil {
			t.Fatalf("figaro %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	if got := run("status", "@figaro"); !strings.Contains(got, minted.FormID) {
		t.Fatalf("status @figaro did not show the role %s:\n%s", minted.FormID, got)
	}
	if got := run("form", "show", "@figaro"); !strings.Contains(got, target) {
		t.Fatalf("form show @figaro did not show the role's target:\n%s", got)
	}
	run("set", "--id", "@figaro", "note", "by name")
	if got := run("state", "--id", minted.FormID); !strings.Contains(got, "by name") {
		t.Fatalf("set --id @figaro did not land on the role:\n%s", got)
	}
	if got := run("send", "-f", "-j", "--id", "@figaro", "--", "hello"); !strings.Contains(got, target) {
		t.Fatalf("send --id @figaro did not reach the role's aria %s:\n%s", target, got)
	}
	holder := rig.mint(t, "holder")
	run("cast", holder, "@figaro")
	if got := run("state", "--id", minted.FormID); !strings.Contains(got, holder) {
		t.Fatalf("cast @figaro did not point the role at %s:\n%s", holder, got)
	}
}

func lastLine(out []byte) []byte {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return []byte(lines[len(lines)-1])
}
