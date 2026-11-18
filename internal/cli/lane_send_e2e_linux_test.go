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

// Defends the send of a lane, end to end through the real CLI: every send
// carries the sender's datetime in its context, and that key is not a write
// the lane needs a grant for on the figaro it sends to.
func TestADefaultLaneSendsToItsParent(t *testing.T) {
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
}
