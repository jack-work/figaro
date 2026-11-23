package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro/wire"
)

func bindRig(t *testing.T, quota int, policy map[string]any) licenseRig {
	t.Helper()
	r := newLicenseRig(t, defaultPolicy)
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	giveMint(t, r, r.x)
	keys := map[string]any{form.FanoutQuotaKey: quota}
	if policy != nil {
		keys[form.ChildPolicyKey] = policy
	}
	if _, err := r.backend.ApplyForm(r.x, *heldPatch(t, keys)); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r licenseRig) bindAs(t *testing.T, figaro string, p *form.Patch) (string, error) {
	t.Helper()
	req := rpc.FormBindRequest{Parent: "null", Outfits: []string{"mock"}, Patch: p, Cwd: r.cwd}
	if p != nil {
		req.Outfits = nil
	}
	raw, _ := json.Marshal(req)
	out, err := r.h[rpc.MethodFormBind](localOwner(), claimed(t, figaro, raw))
	if err != nil {
		return "", err
	}
	return out.(rpc.FormBindResponse).FigaroID, nil
}

func TestABoundChildCanForkWithinThePolicyQuotaOfItsMinter(t *testing.T) {
	r := bindRig(t, 4, map[string]any{form.FanoutQuotaKey: 1, form.FanoutMaxChildrenKey: 1})
	kid, err := r.bindAs(t, r.x, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := keyOn(t, r.grantRig, kid, form.FanoutQuotaKey); string(v) != `1` {
		t.Fatalf("a bound child got quota %s, want the minter's policy 1", v)
	}
	raw, _ := json.Marshal(rpc.ForkRequest{FigaroID: kid})
	if _, err := r.h[rpc.MethodFork](localOwner(), claimed(t, kid, raw)); err != nil {
		t.Fatalf("a bound child with quota 1 could not fork: %v", err)
	}
}

func TestABoundChildOfASilentPolicyIsBornAsAForkIs(t *testing.T) {
	r := bindRig(t, 4, nil)
	kid, err := r.bindAs(t, r.x, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{form.FanoutQuotaKey, form.FanoutMaxChildrenKey} {
		if v, _ := keyOn(t, r.grantRig, kid, key); string(v) != `0` {
			t.Fatalf("a silent policy gave a bound child %s=%s, want the fork default 0", key, v)
		}
	}
}

func TestABoundLogKeepsAQuotaOfZeroUnderAPolicyThatGivesMore(t *testing.T) {
	r := bindRig(t, 4, map[string]any{form.FanoutQuotaKey: 2, form.FanoutMaxChildrenKey: 2})
	log, err := r.bindAs(t, r.x, logBirth())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{form.FanoutQuotaKey, form.FanoutMaxChildrenKey} {
		if v, _ := keyOn(t, r.grantRig, log, key); string(v) != `0` {
			t.Fatalf("a bound log got %s=%s, want 0", key, v)
		}
	}
}

func TestABindStillCountsAgainstTheMintersBudget(t *testing.T) {
	r := bindRig(t, 3, map[string]any{form.FanoutQuotaKey: 1, form.FanoutMaxChildrenKey: 1})
	id, err := r.bindAs(t, r.x, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := r.backend.Node(id); n.Present != r.x {
		t.Fatalf("the bound child is drawn under %q, not its minter %s", n.Present, r.x)
	}
	if _, err := r.bindAs(t, r.x, nil); err == nil || !strings.Contains(err.Error(), "free of quota 3") {
		t.Fatalf("a bind past the minter's budget was admitted: %v", err)
	}
}
