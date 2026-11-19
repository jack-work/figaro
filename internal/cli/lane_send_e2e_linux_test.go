//go:build linux

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (r *restartRig) enforceAll() {
	r.t.Helper()
	f, err := os.OpenFile(filepath.Join(r.root, "config", "config.toml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("[authz]\nenforce_key_grants = true\nenforce_child_policy = true\nenforce_lineage_reach = true\nenforce_quota = true\n"); err != nil {
		r.t.Fatal(err)
	}
}

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
	r.enforceAll()
	r.env = append(r.env, "ANTHROPIC_BASE_URL="+prov.srv.URL, "FIGARO_NO_BIND=1")
	parent := r.newEmptyAria()
	stranger := r.newEmptyAria()
	lane := r.laneOf(parent)

	t.Logf("lane %s under %s holds %s", lane, parent, grantsOf(t, r, lane))
	if out, err := r.as(lane, "send", "-f", "--id", parent, "--", "lane to its parent"); err != nil {
		t.Fatalf("a default lane could not send to its parent: %v\n%s", err, out)
	}
	prov.awaitAsk(t, 0, 30*time.Second, "lane to its parent")
	t.Logf("lane -> parent: sent and asked of the provider")
	if out, err := r.as(lane, "send", "-f", "--id", stranger, "--", "lane to a stranger"); err == nil {
		t.Fatalf("a default lane sent to a figaro outside its grant:\n%s", out)
	}

	desk := r.roleFor(stranger, "desk")
	if out, err := r.as(lane, "send", "-f", "--id", desk, "--", "lane to a role it was not given"); err == nil {
		t.Fatalf("a default lane sent to a role outside its grant:\n%s", out)
	}
	r.run("set", "--id", parent, "system.child_policy", `{"system.scopes": ["send:parent", "send:role:`+desk+`", "read:aria:self", "write:aria:self#@self", "delete:aria:self#@self"]}`)
	clerk := r.laneOf(parent)
	t.Logf("lane %s holds %s; role %s is held by %s", clerk, grantsOf(t, r, clerk), desk, stranger)
	n := prov.count()
	if out, err := r.as(clerk, "send", "-f", "--id", desk, "--", "lane to its role"); err != nil {
		t.Fatalf("a lane holding send:role:%s could not send to it: %v\n%s", desk, err, out)
	}
	prov.awaitAsk(t, n, 30*time.Second, "lane to its role")
	t.Logf("lane -> %s: sent, resolved by the daemon, asked of the provider", desk)
}

// Guards a holder-run hard succession, the one succession/hard.md prescribes:
// the successor keeps the holder's scopes and child policy, whatever outfits
// the role and the caller dress it in. It does not assert the abdication.
func TestAHolderRunSucceedKeepsTheHoldersGrantAndChildPolicy(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	prov := newScriptedProvider(t, func(string) (bool, string) { return false, sseText("noted") })
	r := newRestartRig(t)
	r.enforceAll()
	r.env = append(r.env, "ANTHROPIC_BASE_URL="+prov.srv.URL, "FIGARO_NO_BIND=1")
	for name, body := range map[string]string{
		"seatgrants": "[system]\nscopes = [\"*:*\"]\n",
		"route":      "[system]\nprovider = \"anthropic\"\nmodel = \"claude-sonnet-4-5\"\n",
	} {
		if err := os.WriteFile(filepath.Join(r.root, "config", "outfits", name+".toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	holder := r.newAria()
	policy := `{"system.scopes":["send:parent","read:aria:self"]}`
	r.run("set", "--id", holder, "system.scopes", `["*:*"]`)
	r.run("set", "--id", holder, "system.child_policy", policy)
	seat := r.roleFor(holder, "seat")
	r.run("set", "--id", seat, "succession-policy.outfit", "default,seatgrants")
	r.run("set", "--id", seat, "system.grants.outfits", `["route"]`)

	out, _ := r.as(holder, "succeed", seat, "-O", "route", "--abdication", "linger", "--stay", "-j", "--", "you hold the seat")
	t.Logf("holder-run succeed report: %s", strings.TrimSpace(out))
	var rep struct {
		Successor string `json:"successor"`
	}
	if err := json.NewDecoder(strings.NewReader(out)).Decode(&rep); err != nil || rep.Successor == "" {
		t.Fatalf("succeed -j named no successor: %v\n%s", err, out)
	}
	var board struct {
		System map[string]json.RawMessage `json:"system"`
	}
	if err := json.Unmarshal([]byte(r.run("state", "show", "--id", rep.Successor, "-j")), &board); err != nil {
		t.Fatal(err)
	}
	if got := compactJSON(t, board.System["scopes"]); got != `["*:*"]` {
		t.Errorf("the successor holds scopes %s, want the holder's", got)
	}
	if got := compactJSON(t, board.System["child_policy"]); got != policy {
		t.Errorf("the successor holds child policy %s, want the holder's %s", got, policy)
	}
	t.Logf("successor %s: scopes %s, child_policy %s", rep.Successor, compactJSON(t, board.System["scopes"]), compactJSON(t, board.System["child_policy"]))
}

func compactJSON(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var b bytes.Buffer
	if len(raw) == 0 {
		return ""
	}
	if err := json.Compact(&b, raw); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// Defends the rollout default through a real daemon: with no switch in its
// config, a figaro's fork is born with its parent's scopes, and doctor
// admission shows every switch off and counts the child policy it would have
// applied.
func TestTheEnforcementSwitchesShipOffInTheDaemon(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	prov := newScriptedProvider(t, func(string) (bool, string) { return false, sseText("noted") })
	r := newRestartRig(t)
	r.env = append(r.env, "ANTHROPIC_BASE_URL="+prov.srv.URL, "FIGARO_NO_BIND=1")
	parent := r.newEmptyAria()
	r.run("set", "--id", parent, "system.scopes", `["*:*"]`)
	lane := r.laneOf(parent)
	var board struct {
		System map[string]json.RawMessage `json:"system"`
	}
	if err := json.Unmarshal([]byte(r.run("state", "show", "--id", lane, "-j")), &board); err != nil {
		t.Fatal(err)
	}
	if got := compactJSON(t, board.System["scopes"]); got != `["*:*"]` {
		t.Errorf("with the switches off a figaro's fork holds %s, want its parent's", got)
	}
	var doc struct {
		Switches []struct {
			Name        string `json:"name"`
			On          bool   `json:"on"`
			WouldRefuse uint64 `json:"would_refuse"`
		} `json:"switches"`
	}
	if err := json.Unmarshal([]byte(r.run("doctor", "admission", "--json")), &doc); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, s := range doc.Switches {
		seen[s.Name] = true
		if s.On {
			t.Errorf("%s is on with no config", s.Name)
		}
		if s.Name == "enforce_child_policy" && s.WouldRefuse == 0 {
			t.Error("the child policy the fork was not given was not counted")
		}
	}
	for _, name := range []string{"enforce_key_grants", "enforce_child_policy", "enforce_lineage_reach", "enforce_quota"} {
		if !seen[name] {
			t.Errorf("doctor admission does not show %s", name)
		}
	}

	r.enforceAll()
	r.run("state", "outfit", "--refresh")
	if err := json.Unmarshal([]byte(r.run("doctor", "admission", "--json")), &doc); err != nil {
		t.Fatal(err)
	}
	for _, s := range doc.Switches {
		if !s.On {
			t.Errorf("%s stayed off after the config turned it on and the daemon re-read it", s.Name)
		}
	}
	if got := grantsOf(t, r, r.laneOf(parent)); got != `["send:parent","read:aria:self","write:aria:self#@self","delete:aria:self#@self"]` {
		t.Errorf("with enforce_child_policy on a figaro's fork holds %s, want the lane set", got)
	}
}

func grantsOf(t *testing.T, r *restartRig, id string) string {
	t.Helper()
	var board struct {
		System map[string]json.RawMessage `json:"system"`
	}
	if err := json.Unmarshal([]byte(r.run("state", "show", "--id", id, "-j")), &board); err != nil {
		t.Fatal(err)
	}
	return compactJSON(t, board.System["scopes"])
}

// Shows an outfit's child policy through the real CLI: the owner dresses a
// fork in an outfit whose [system.child_policy] uses a dotted key, and the
// lane that fork makes is born with the outfit's keys, path by path.
func TestAnOutfitsChildPolicyReachesALaneThroughTheCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	prov := newScriptedProvider(t, func(string) (bool, string) { return false, sseText("noted") })
	r := newRestartRig(t)
	r.enforceAll()
	r.env = append(r.env, "ANTHROPIC_BASE_URL="+prov.srv.URL, "FIGARO_NO_BIND=1")
	body := "[system.child_policy]\nsystem.scopes = [\"send:parent\", \"read:aria:self\"]\nmantra = \"a lane\"\n"
	if err := os.WriteFile(filepath.Join(r.root, "config", "outfits", "lanes.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := r.newEmptyAria()
	r.run("set", "--id", parent, "system.child_policy", `{"system.scopes": ["send:parent", "read:*"]}`)
	raw := r.run("fork", parent, "-O", "lanes", "-j")
	var out struct {
		Alternative string `json:"alternative"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &out); err != nil || out.Alternative == "" {
		t.Fatalf("fork -O lanes -j printed %q", raw)
	}
	seat := out.Alternative
	r.run("set", "--id", seat, "system.scopes", `["*:*"]`)
	var board struct {
		System map[string]json.RawMessage `json:"system"`
	}
	if err := json.Unmarshal([]byte(r.run("state", "show", "--id", seat, "-j")), &board); err != nil {
		t.Fatal(err)
	}
	t.Logf("dressed %s carries child_policy %s", seat, compactJSON(t, board.System["child_policy"]))
	lane := r.laneOf(seat)
	if got := grantsOf(t, r, lane); got != `["send:parent","read:aria:self"]` {
		t.Errorf("the lane holds %s, want the outfit's policy", got)
	}
	var laneBoard struct {
		Mantra string `json:"mantra"`
	}
	if err := json.Unmarshal([]byte(r.run("state", "show", "--id", lane, "-j")), &laneBoard); err != nil || laneBoard.Mantra != "a lane" {
		t.Errorf("the lane's mantra %q (%v), want the outfit's", laneBoard.Mantra, err)
	}
	t.Logf("lane %s holds %s", lane, grantsOf(t, r, lane))
}

// Defends retire and succeed as verbs under enforce_key_grants: the lifetime
// they write is the daemon's to give, so a figaro retires its own fork with
// --ttl, and a holder-run succeed abdicates, with no grant on system.ttl.
func TestRetireAndSucceedWriteTheirLifetimeWithNoKeyGrant(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	prov := newScriptedProvider(t, func(string) (bool, string) { return false, sseText("noted") })
	r := newRestartRig(t)
	r.enforceAll()
	r.env = append(r.env, "ANTHROPIC_BASE_URL="+prov.srv.URL, "FIGARO_NO_BIND=1")
	holder := r.newAria()
	r.run("set", "--id", holder, "system.scopes", `["*:*"]`)
	lane := r.laneOf(holder)
	if out, err := r.as(holder, "retire", lane, "--ttl", "10m"); err != nil {
		t.Fatalf("a figaro could not retire its own fork with --ttl: %v\n%s", err, out)
	}
	seat := r.roleFor(holder, "seat")
	out, err := r.as(holder, "succeed", seat, "--abdication", "linger", "--stay", "-j", "--", "you hold the seat")
	var rep struct {
		Abdicated bool   `json:"abdicated"`
		FailedAt  string `json:"failed_at"`
	}
	_ = json.NewDecoder(strings.NewReader(out)).Decode(&rep)
	if err != nil || !rep.Abdicated || rep.FailedAt != "" {
		t.Fatalf("a holder-run succeed did not abdicate: %v\n%s", err, out)
	}
	t.Logf("holder-run succeed: %s", strings.TrimSpace(out))
}
