//go:build linux

package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func (r *restartRig) as(figaro string, args ...string) (string, error) {
	c := r.cmd(args...)
	c.Env = append(c.Env, "FIGARO_ARIA="+figaro)
	out, err := c.CombinedOutput()
	return string(out), err
}

func (r *restartRig) laneOf(parent string) string {
	r.t.Helper()
	raw, err := r.as(parent, "fork", parent, "-j")
	var out struct {
		Alternative string `json:"alternative"`
	}
	if err != nil || json.Unmarshal([]byte(strings.TrimSpace(raw)), &out) != nil || out.Alternative == "" {
		r.t.Fatalf("%s forking itself: %v\n%s", parent, err, raw)
	}
	return out.Alternative
}

func (r *restartRig) roleFor(holder, name string) string {
	r.t.Helper()
	raw := r.run("cast", holder, "-S", "name="+name, "-j")
	var out struct {
		RoleID string `json:"role_id"`
	}
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if json.Unmarshal([]byte(lines[len(lines)-1]), &out) != nil || out.RoleID == "" {
		r.t.Fatalf("cast %s -S name=%s -j printed no role: %q", holder, name, raw)
	}
	return out.RoleID
}

// Defends the sends of a lane, end to end through the real CLI: every send
// carries the sender's datetime in its context, which is not a write the lane
// needs a grant for on its receiver; and a role resolves on the daemon under
// the send grant, so a lane holding send:role:@R and no read on R reaches R's
// holder.
func TestADefaultLaneSendsToItsParentAndToItsRole(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	prov := newScriptedProvider(t, func(string) (bool, string) { return false, sseText("noted") })
	r := newRestartRig(t)
	r.env = append(r.env, "ANTHROPIC_BASE_URL="+prov.srv.URL, "FIGARO_NO_BIND=1")
	parent := r.newEmptyAria()
	stranger := r.newEmptyAria()
	lane := r.laneOf(parent)

	if out, err := r.as(lane, "send", "-f", "--id", parent, "--", "lane to its parent"); err != nil {
		t.Fatalf("a default lane could not send to its parent: %v\n%s", err, out)
	}
	prov.awaitAsk(t, 0, 30*time.Second, "lane to its parent")
	if out, err := r.as(lane, "send", "-f", "--id", stranger, "--", "lane to a stranger"); err == nil {
		t.Fatalf("a default lane sent to a figaro outside its grant:\n%s", out)
	}

	desk := r.roleFor(stranger, "desk")
	if out, err := r.as(lane, "send", "-f", "--id", desk, "--", "lane to a role it was not given"); err == nil {
		t.Fatalf("a default lane sent to a role outside its grant:\n%s", out)
	}
	r.run("set", "--id", parent, "system.child_policy", `{"system.scopes": ["send:parent", "send:role:`+desk+`", "read:aria:self", "write:aria:self#@self", "delete:aria:self#@self"]}`)
	clerk := r.laneOf(parent)
	n := prov.count()
	if out, err := r.as(clerk, "send", "-f", "--id", desk, "--", "lane to its role"); err != nil {
		t.Fatalf("a lane holding send:role:%s could not send to it: %v\n%s", desk, err, out)
	}
	prov.awaitAsk(t, n, 30*time.Second, "lane to its role")
}
