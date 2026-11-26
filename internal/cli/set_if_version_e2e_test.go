package cli

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// Two claimers read the same version of a work-order form; only the first
// conditional write lands, and the second is refused without touching it.
func TestTwoClaimsAtOneVersionLetOnlyTheFirstLand(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	rig := newSendRig(t, "http://127.0.0.1:9")
	out, err := rig.cmd("form", "new", "-j", "-S", "queue.w1.state=posted").Output()
	if err != nil {
		t.Fatalf("form new: %v\n%s", err, out)
	}
	var made struct {
		FormID string `json:"form_id"`
	}
	if err := json.Unmarshal(lastLine(out), &made); err != nil || made.FormID == "" {
		t.Fatalf("form new -j printed no form id: %q", out)
	}
	id := made.FormID

	out, err = rig.cmd("state", "show", "--id", id, "--version").Output()
	if err != nil {
		t.Fatalf("state show --version: %v\n%s", err, out)
	}
	version := strings.TrimSpace(string(out))
	if n, err := strconv.ParseUint(version, 10, 64); err != nil || n == 0 {
		t.Fatalf("state show --version printed %q", out)
	}

	rig.must(t, "set", "--id", id, "--if-version", version, "queue.w1.claimed_by", "first")
	if out, err := rig.cmd("set", "--id", id, "--if-version", version, "queue.w1.claimed_by", "second").CombinedOutput(); err == nil {
		t.Fatalf("a second claim at the same version landed: %s", out)
	}

	out, err = rig.cmd("get", "--id", id, "queue.w1.claimed_by").Output()
	if err != nil {
		t.Fatalf("get: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "first" {
		t.Fatalf("claimed_by = %q, want the first claim", got)
	}
}
