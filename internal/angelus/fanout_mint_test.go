package angelus_test

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
)

func localOwner() context.Context { return authz.WithDoor(asOwner(), authz.DoorLocal) }

type licenseRig struct {
	grantRig
	root, x string
}

func newLicenseRig(t *testing.T, policy string) licenseRig {
	t.Helper()
	r := newRigWith(t, policy)
	root, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	return licenseRig{grantRig: r, root: root.FigaroID, x: ariasFork(t, r, root.FigaroID)}
}

func (r licenseRig) createAs(t *testing.T, figaro string) (rpc.CreateResponse, error) {
	t.Helper()
	raw, _ := json.Marshal(rpc.CreateRequest{Outfits: []string{"mock"}, Cwd: r.cwd})
	if figaro != "" {
		raw = claimed(t, figaro, raw)
	}
	out, err := r.h[rpc.MethodCreate](localOwner(), raw)
	if err != nil {
		return rpc.CreateResponse{}, err
	}
	return out.(rpc.CreateResponse), nil
}

func (r licenseRig) bindNullAs(t *testing.T, figaro string) error {
	t.Helper()
	raw, _ := json.Marshal(rpc.FormBindRequest{Parent: "null", Cwd: r.cwd})
	_, err := r.h[rpc.MethodFormBind](localOwner(), claimed(t, figaro, raw))
	return err
}

func (r licenseRig) setAs(t *testing.T, figaro, target string, p form.Patch) error {
	t.Helper()
	_, err := r.h[rpc.MethodSet](localOwner(), claimed(t, figaro, withAria(t, target, rpc.SetRequest{Patch: p})))
	return err
}

func mintPatch(t *testing.T, v bool) form.Patch {
	t.Helper()
	return *heldPatch(t, map[string]any{form.FanoutMintKey: v})
}

func giveMint(t *testing.T, r licenseRig, id string) {
	t.Helper()
	if _, err := r.backend.ApplyForm(id, mintPatch(t, true)); err != nil {
		t.Fatal(err)
	}
}

func mintRole(t *testing.T, r licenseRig, holder string) string {
	t.Helper()
	target, _ := json.Marshal(holder)
	id, _, err := r.backend.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"target-aria":      target,
		form.FanoutMintKey: json.RawMessage(`true`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestALabeledNewIsRefusedWithoutMint(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	before := len(r.backend.ConversationIDs())
	_, err := r.createAs(t, r.x)
	if err == nil || !strings.Contains(err.Error(), "mint: "+r.x+" may not create a top-level figaro; fork yourself instead") {
		t.Fatalf("a labeled new with no mint = %v, want the mint refusal", err)
	}
	if err := r.bindNullAs(t, r.x); err == nil || !strings.Contains(err.Error(), "may not create a top-level figaro") {
		t.Fatalf("a labeled bind null with no mint = %v, want the mint refusal", err)
	}
	if got := len(r.backend.ConversationIDs()); got != before {
		t.Fatalf("a refused mint left %d figaros, want %d", got, before)
	}
}

func TestALabeledNewIsAllowedWithOwnMint(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	giveMint(t, r, r.x)
	if _, err := r.createAs(t, r.x); err != nil {
		t.Fatalf("a figaro holding mint could not new: %v", err)
	}
	if err := r.bindNullAs(t, r.x); err != nil {
		t.Fatalf("a figaro holding mint could not bind null: %v", err)
	}
}

func TestALabeledNewIsAllowedThroughAHeldRole(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	mintRole(t, r, r.x)
	if _, err := r.createAs(t, r.x); err != nil {
		t.Fatalf("the holder of a role carrying mint could not new: %v", err)
	}
	if _, err := r.createAs(t, r.root); err == nil {
		t.Fatal("a figaro holding no role with mint created through someone else's role")
	}
}

func TestAnUnlabeledNewIsAllowed(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	if _, err := r.createAs(t, ""); err != nil {
		t.Fatalf("the owner at a terminal, no label, could not new: %v", err)
	}
}

func TestAFigaroCannotSetItsOwnMint(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	giveMint(t, r, r.root)
	err := r.setAs(t, r.x, r.x, mintPatch(t, true))
	if err == nil || !strings.Contains(err.Error(), "no ancestor") {
		t.Fatalf("a figaro granted itself mint: %v", err)
	}
	if v, ok := keyOn(t, r.grantRig, r.x, form.FanoutMintKey); ok {
		t.Fatalf("mint landed as %s despite the refusal", v)
	}
	if err := r.setAs(t, r.root, r.root, mintPatch(t, false)); err == nil {
		t.Fatal("a figaro rewrote its own mint")
	}
}

func TestAnAncestorWithoutMintCannotGrantIt(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	err := r.setAs(t, r.root, r.x, mintPatch(t, true))
	if err == nil || !strings.Contains(err.Error(), "does not hold mint itself") {
		t.Fatalf("an ancestor without mint granted it: %v", err)
	}
	if v, ok := keyOn(t, r.grantRig, r.x, form.FanoutMintKey); ok {
		t.Fatalf("mint landed as %s despite the refusal", v)
	}
	raw, _ := json.Marshal(rpc.ForkRequest{FigaroID: r.x, Patch: ptrPatch(mintPatch(t, true))})
	if _, err := r.h[rpc.MethodFork](localOwner(), claimed(t, r.x, raw)); err == nil || !strings.Contains(err.Error(), "does not hold mint itself") {
		t.Fatalf("a fork -S granted mint its writer lacks: %v", err)
	}
	giveMint(t, r, r.root)
	if err := r.setAs(t, r.root, r.x, mintPatch(t, true)); err != nil {
		t.Fatalf("control: an ancestor holding mint grants it: %v", err)
	}
	if _, err := r.createAs(t, r.x); err != nil {
		t.Fatalf("the granted figaro could not new: %v", err)
	}
}

func TestAnUnrelatedFigaroCannotPutMintOnARole(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	giveMint(t, r, r.root)
	role := mintRole(t, r, r.root)
	if _, err := r.backend.ApplyForm(role, form.Build(form.Snapshot{}, nil, []string{form.FanoutMintKey})); err != nil {
		t.Fatal(err)
	}
	if err := r.setAs(t, r.x, role, mintPatch(t, true)); err == nil {
		t.Fatal("a figaro put mint on a role form")
	}
	if _, err := r.h[rpc.MethodSet](localOwner(), withAria(t, role, rpc.SetRequest{Patch: mintPatch(t, true)})); err != nil {
		t.Fatalf("the owner, unlabeled, could not set mint on a role form: %v", err)
	}
	if v, _ := keyOn(t, r.grantRig, role, form.FanoutMintKey); string(v) != "true" {
		t.Fatalf("set --id @role landed elsewhere: the role's mint = %s", v)
	}
}

func TestAClaimedForkOfAMintHolderIsBornWithoutIt(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	giveMint(t, r, r.x)
	raw, _ := json.Marshal(rpc.ForkRequest{FigaroID: r.x})
	out, err := r.h[rpc.MethodFork](localOwner(), claimed(t, r.x, raw))
	if err != nil {
		t.Fatal(err)
	}
	kid := out.(rpc.ForkResponse).Alternative
	if v, ok := keyOn(t, r.grantRig, kid, form.FanoutMintKey); ok {
		t.Fatalf("a claimed fork inherited mint = %s", v)
	}
	if _, err := r.createAs(t, kid); err == nil {
		t.Fatal("the fork of a mint holder could new")
	}
	raw, _ = json.Marshal(rpc.ForkRequest{FigaroID: r.x, Patch: ptrPatch(mintPatch(t, true))})
	out, err = r.h[rpc.MethodFork](localOwner(), claimed(t, r.x, raw))
	if err != nil {
		t.Fatalf("control: a mint holder grants its fork mint: %v", err)
	}
	if v, _ := keyOn(t, r.grantRig, out.(rpc.ForkResponse).Alternative, form.FanoutMintKey); string(v) != "true" {
		t.Fatalf("granted fork mint = %s, want true", v)
	}
}

func TestMintMustBeABoolean(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	p := *heldPatch(t, map[string]any{form.FanoutMintKey: "yes"})
	if _, err := r.h[rpc.MethodSet](localOwner(), withAria(t, r.x, rpc.SetRequest{Patch: p})); err == nil || !strings.Contains(err.Error(), "must be true or false") {
		t.Fatalf("mint = \"yes\" was accepted: %v", err)
	}
}

const mintlessPolicy = `
[roles.owner]
grants = ["read:*", "listen:*", "send:*", "fork:*", "set:*", "create:*", "kill:*", "admin:*", "use:*", "join:*", "decide:*", "adopt:*", "lease:*", "say:*"]

[[bind]]
role = "owner"
principals = ["user:SELF"]
`

func TestUnderAPolicyBothThePolicyAndTheKeyMustAllowANew(t *testing.T) {
	r := newLicenseRig(t, guardPolicy)
	if _, err := r.createAs(t, r.x); err == nil || !strings.Contains(err.Error(), "may not create a top-level figaro") {
		t.Fatalf("a policy granting mint let a figaro without the key new: %v", err)
	}
	giveMint(t, r, r.x)
	if _, err := r.createAs(t, r.x); err != nil {
		t.Fatalf("policy and key both allow: %v", err)
	}
	text := strings.ReplaceAll(mintlessPolicy, "SELF", strconv.Itoa(os.Getuid()))
	if err := os.WriteFile(r.policy, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.a.Policies.Reload(r.policy); err != nil {
		t.Fatal(err)
	}
	if _, err := r.createAs(t, r.x); err == nil {
		t.Fatal("the key let a figaro new under a policy that denies mint")
	}
}

func ptrPatch(p form.Patch) *form.Patch { return &p }
