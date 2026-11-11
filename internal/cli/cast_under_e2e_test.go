package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// cast --under mints the lieutenant's role as a fork of the boss's role, so
// the org tree is the role forms' lineage. The new role inherits the boss
// role's keys but not its seat, and the boss stays where it was.
func TestCastUnderForksTheBossRoleWithoutItsSeat(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	rig := newSendRig(t, "http://127.0.0.1:9")
	boss := rig.mint(t, "boss")
	lieutenant := rig.mint(t, "lieutenant")

	out, err := rig.cmd("form", "new", "-j", "name=boss", "dept=ops", "target-aria="+boss).Output()
	if err != nil {
		t.Fatalf("form new: %v\n%s", err, out)
	}
	var bossRole struct {
		FormID string `json:"form_id"`
	}
	if err := json.Unmarshal(lastLine(out), &bossRole); err != nil || bossRole.FormID == "" {
		t.Fatalf("form new -j printed no form id: %q", out)
	}

	out, err = rig.cmd("cast", lieutenant, "--under", "@boss", "-S", "name=lieutenant", "-j").CombinedOutput()
	if err != nil {
		t.Fatalf("cast --under: %v\n%s", err, out)
	}
	var cast struct {
		AriaID string `json:"aria_id"`
		RoleID string `json:"role_id"`
	}
	if err := json.Unmarshal(lastLine(out), &cast); err != nil || cast.RoleID == "" {
		t.Fatalf("cast --under -j printed no role id: %q", out)
	}
	if cast.AriaID != lieutenant {
		t.Fatalf("cast --under cast %s, want %s", cast.AriaID, lieutenant)
	}

	out, err = rig.cmd("ls", "-j").Output()
	if err != nil {
		t.Fatalf("ls -j: %v\n%s", err, out)
	}
	var rows []struct {
		ID         string `json:"id"`
		Parent     string `json:"parent"`
		TargetAria string `json:"target_aria"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("ls -j: %v\n%s", err, out)
	}
	bare := func(id string) string { return strings.TrimPrefix(id, "@") }
	find := func(id string) (int, bool) {
		for i, r := range rows {
			if bare(r.ID) == bare(id) {
				return i, true
			}
		}
		return 0, false
	}
	bi, ok := find(bossRole.FormID)
	if !ok {
		t.Fatalf("boss role %s missing from ls -j:\n%s", bossRole.FormID, out)
	}
	li, ok := find(cast.RoleID)
	if !ok {
		t.Fatalf("lieutenant role %s missing from ls -j:\n%s", cast.RoleID, out)
	}
	if got := bare(rows[li].Parent); got != bare(bossRole.FormID) {
		t.Fatalf("lieutenant role's parent = %q, want the boss role %s", got, bossRole.FormID)
	}
	if rows[li].TargetAria != lieutenant {
		t.Fatalf("lieutenant role points at %q, want %s", rows[li].TargetAria, lieutenant)
	}
	if rows[bi].TargetAria != boss {
		t.Fatalf("boss role moved to %q, want it still at %s", rows[bi].TargetAria, boss)
	}

	state, err := rig.cmd("state", "--id", cast.RoleID).CombinedOutput()
	if err != nil {
		t.Fatalf("state --id %s: %v\n%s", cast.RoleID, err, state)
	}
	if !strings.Contains(string(state), "ops") || !strings.Contains(string(state), "lieutenant") {
		t.Fatalf("lieutenant role did not inherit dept=ops and carry name=lieutenant:\n%s", state)
	}
}
