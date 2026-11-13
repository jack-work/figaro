package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkrpc"
)

const boxLeavePolicy = `
[roles.owner]
permissions = ["*:*"]

[[bind]]
role = "owner"
principals = ["user:SELF"]

[roles.box]
permissions = [
  "create:*",
  "use:sandbox:self",
  "read:sandboxed:self", "listen:sandboxed:self", "send:sandboxed:self", "fork:sandboxed:self", "set:sandboxed:self", "kill:sandboxed:self",
]

[[bind]]
role = "box"
principals = ["sandbox:judge@local:box"]
`

func sandboxPatch(v string) *form.Patch {
	raw, _ := json.Marshal(v)
	p := form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.sandbox": raw}, nil)
	return &p
}

func leaves(t *testing.T, c *jkrpc.Client, method, id string, req any) error {
	t.Helper()
	var out json.RawMessage
	return boxOnAria(t, c, method, id, req, &out)
}

// A box cannot take an aria out of its box, by any route that writes a
// form: not by set, not by a fork dressed in a patch, not by a cast that
// mints a role form. Each is refused, not quietly pinned back, and a fork
// or cast that names the box's own sandbox, or none, still goes through.
func TestABoxCannotLeaveItsBox(t *testing.T) {
	d := boxEndpointRigWith(t, boxLeavePolicy, "true")
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	c := d.dial(t)
	before := len(d.backend.ConversationIDs())
	for _, to := range []string{"", "local"} {
		if err := leaves(t, c, rpc.MethodSet, inBox.FigaroID, rpc.SetRequest{Patch: *sandboxPatch(to)}); err == nil {
			t.Errorf("set system.sandbox %q: the box moved its aria out", to)
		}
		if got := d.sandboxOf(t, inBox.FigaroID); got != "judge" {
			t.Fatalf("set %q: a refused move landed, sandbox %q", to, got)
		}
		if err := leaves(t, c, rpc.MethodFork, inBox.FigaroID, rpc.ForkRequest{FigaroID: inBox.FigaroID, Patch: sandboxPatch(to)}); err == nil {
			t.Errorf("fork with system.sandbox %q: the box forked out of its box", to)
		}
		if err := leaves(t, c, rpc.MethodCast, inBox.FigaroID, rpc.CastRequest{RolePatch: sandboxPatch(to)}); err == nil {
			t.Errorf("cast with system.sandbox %q: the box minted a form out of its box", to)
		}
	}
	if err := leaves(t, c, rpc.MethodFork, inBox.FigaroID, rpc.ForkRequest{FigaroID: inBox.FigaroID, Patch: sandboxPatch("judge")}); err != nil {
		t.Errorf("a fork that names the box's own sandbox: %v", err)
	}
	if err := leaves(t, c, rpc.MethodCast, inBox.FigaroID, rpc.CastRequest{RolePatch: mantraPatch("role")}); err != nil {
		t.Errorf("a cast that names no sandbox: %v", err)
	}
	before += 1
	for _, id := range d.backend.ConversationIDs() {
		if got := d.sandboxOf(t, id); got != "judge" && id != inBox.FigaroID {
			if m, _ := d.backend.Meta(id); m != nil && strings.HasPrefix(m.MintedBy, "sandbox:judge") {
				t.Errorf("the box left %s behind in sandbox %q", id, got)
			}
		}
	}
	if after := len(d.backend.ConversationIDs()); after != before {
		t.Errorf("refused writes left %d aria(s) behind", after-before)
	}
}
