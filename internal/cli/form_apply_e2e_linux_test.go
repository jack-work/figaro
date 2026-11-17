//go:build linux

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

func TestManagedRoleCLIUsesAnExistingDaemonAndPreservesUnmanagedState(t *testing.T) {
	if testing.Short() {
		t.Skip("starts an isolated fixture daemon")
	}
	r := newRestartRig(t)
	out, err := r.cmd("form", "apply", "--id", "@00000000000000000000000000000000", "-O", "role", "--managed", "charter", "--check").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "daemon unavailable") {
		t.Fatalf("missing daemon was not deferred: %v: %s", err, out)
	}
	if _, err := os.Stat(r.socket()); !os.IsNotExist(err) {
		t.Fatalf("check autospawned a daemon: %v", err)
	}
	if err := os.WriteFile(filepath.Join(r.root, "config", "outfits", "role.toml"), []byte("[charter]\npurpose = \"new\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	holder := r.mint()
	created := r.run("form", "new", "-S", "name=managed-fixture,target-aria="+holder+",charter.purpose=old,charter.note=keep,handoff=keep.md", "-j")
	var born struct {
		FormID string `json:"form_id"`
	}
	for _, line := range strings.Split(created, "\n") {
		if json.Unmarshal([]byte(line), &born) == nil && born.FormID != "" {
			break
		}
	}
	if born.FormID == "" {
		t.Fatalf("fixture role was not created: %s", created)
	}
	apply := func(check bool) rpc.FormApplyResponse {
		t.Helper()
		args := []string{"form", "apply", "--id", born.FormID, "-O", "role", "--managed", "charter.purpose", "-j"}
		if check {
			args = append(args, "--check")
		}
		var result rpc.FormApplyResponse
		raw := r.run(args...)
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatalf("not a JSON apply result: %s: %v", raw, err)
		}
		return result
	}
	if checked := apply(true); checked.Outcome != "would-apply" {
		t.Fatalf("wrong check result: %+v", checked)
	}
	if got := strings.TrimSpace(r.run("get", "--id", born.FormID, "charter.purpose")); got != "old" {
		t.Fatalf("check changed the role: %s", got)
	}
	manifest := filepath.Join(r.root, "roles.toml")
	if err := os.WriteFile(manifest, []byte(fmt.Sprintf(`
[roles.declared]
id = %q
outfits = ["role"]
managed = ["charter.purpose"]
mode = "apply"

[roles.missing]
id = "@00000000000000000000000000000000"
outfits = ["role"]
managed = ["charter.purpose"]
`, born.FormID)), 0o600); err != nil {
		t.Fatal(err)
	}
	dry, err := r.cmd("form", "apply", "--manifest", manifest, "--check").CombinedOutput()
	if err == nil || !strings.Contains(string(dry), "declared "+born.FormID+": would-apply") || !strings.Contains(string(dry), "set: charter.purpose") || !strings.Contains(string(dry), "missing @00000000000000000000000000000000: error:") {
		t.Fatalf("dry run did not plan the existing role and refuse the missing one: %v: %s", err, dry)
	}
	if got := strings.TrimSpace(r.run("get", "--id", born.FormID, "charter.purpose")); got != "old" {
		t.Fatalf("manifest --check changed the role: %s", got)
	}
	applied := apply(false)
	if applied.Outcome != rpc.OutcomeApplied {
		t.Fatalf("wrong apply result: %+v", applied)
	}
	for key, want := range map[string]string{"charter.purpose": "new", "charter.note": "keep", "handoff": "keep.md", "target-aria": holder} {
		if got := strings.TrimSpace(r.run("get", "--id", born.FormID, key)); got != want {
			t.Fatalf("%s: %s, want %q", key, got, want)
		}
	}
	if repeated := apply(false); repeated.Outcome != rpc.OutcomeUnchanged || repeated.Version != applied.Version {
		t.Fatalf("repeat was not idempotent: %+v", repeated)
	}
	if out, err := r.cmd("form", "apply", "--manifest", manifest).CombinedOutput(); err == nil || !strings.Contains(string(out), "declared "+born.FormID+": unchanged (version "+fmt.Sprint(applied.Version)+")") {
		t.Fatalf("manifest apply was not idempotent or hid the missing role: %v: %s", err, out)
	}
	if out, err := r.cmd("ls", "-j").CombinedOutput(); err != nil || strings.Contains(string(out), "00000000000000000000000000000000") {
		t.Fatalf("manifest created a role it did not find: %v: %s", err, out)
	}
}
