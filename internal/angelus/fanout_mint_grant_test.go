package angelus_test

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
)

func giveMintGrant(t *testing.T, r licenseRig, id string) {
	t.Helper()
	if _, err := r.backend.ApplyForm(id, *heldPatch(t, map[string]any{form.ScopesKey: []string{"*:*"}})); err != nil {
		t.Fatal(err)
	}
}

func TestALabeledNewIsRefusedWithoutMintWithKeyGrantsOn(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	keyGrants(r, true)
	_, err := r.createAs(t, r.x)
	if err == nil || !strings.Contains(err.Error(), "mint: "+r.x+" may not create a top-level figaro; fork yourself instead") {
		t.Fatalf("an unscoped figaro minted: its implicit *:* is no license: %v", err)
	}
	if err := r.bindNullAs(t, r.x); err == nil || !strings.Contains(err.Error(), "may not create a top-level figaro") {
		t.Fatalf("an unscoped figaro bound a fresh figaro: %v", err)
	}
}

func TestALabeledNewIsAllowedWithMintWrittenWithKeyGrantsOn(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	keyGrants(r, true)
	if _, err := r.backend.ApplyForm(r.x, *heldPatch(t, map[string]any{form.ScopesKey: []string{"read:*", "send:*", "fork:*", "set:*", "use:*", "mint:*"}})); err != nil {
		t.Fatal(err)
	}
	if _, err := r.createAs(t, r.x); err != nil {
		t.Fatalf("a figaro with mint:* written could not mint: %v", err)
	}
}

func TestARoleCarryingTheRetiredKeyLicensesNobodyWithKeyGrantsOn(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	keyGrants(r, true)
	target, _ := json.Marshal(r.x)
	if _, _, err := r.backend.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"target-aria":      target,
		form.FanoutMintKey: json.RawMessage(`true`),
	}, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.createAs(t, r.x); err == nil {
		t.Fatal("system.fanout.mint on a held role still licensed its holder")
	}
}

func TestAnUnlabeledNewIsAllowedWithKeyGrantsOn(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	keyGrants(r, true)
	if _, err := r.createAs(t, ""); err != nil {
		t.Fatalf("the owner at a terminal could not mint: %v", err)
	}
}

func TestTheRetiredMintKeyIsRefusedToEveryWriterWithKeyGrantsOn(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	keyGrants(r, true)
	on := *heldPatch(t, map[string]any{form.FanoutMintKey: true})
	if _, err := r.h[rpc.MethodSet](localOwner(), withAria(t, r.x, rpc.SetRequest{Patch: on})); err == nil || !strings.Contains(err.Error(), "mint:*") {
		t.Fatalf("the owner wrote the retired key, or was not told where the license went: %v", err)
	}
	giveMintGrant(t, r, r.root)
	if err := r.setAs(t, r.root, r.x, on); err == nil {
		t.Fatal("an ancestor wrote the retired key")
	}
	if _, err := r.backend.ApplyForm(r.x, on); err != nil {
		t.Fatal(err)
	}
	off := form.Build(form.Snapshot{}, nil, []string{form.FanoutMintKey})
	if _, err := r.h[rpc.MethodSet](localOwner(), withAria(t, r.x, rpc.SetRequest{Patch: off})); err != nil {
		t.Fatalf("the owner could not clear the retired key: %v", err)
	}
}

func TestAFigaroGivesMintOnlyIfItHoldsItWithKeyGrantsOn(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	keyGrants(r, true)
	if _, err := r.backend.ApplyForm(r.root, *heldPatch(t, map[string]any{form.ScopesKey: []string{"read:*", "send:*", "fork:*", "set:*", "delete:*", "use:*"}})); err != nil {
		t.Fatal(err)
	}
	err := r.setAs(t, r.root, r.x, *heldPatch(t, map[string]any{form.ScopesKey: []string{"mint:*"}}))
	if err == nil {
		t.Fatal("a figaro without mint gave mint")
	}
	giveMintGrant(t, r, r.root)
	if err := r.setAs(t, r.root, r.x, *heldPatch(t, map[string]any{form.ScopesKey: []string{"read:*", "use:*", "mint:*"}})); err != nil {
		t.Fatalf("a figaro holding *:* could not give mint: %v", err)
	}
	if _, err := r.createAs(t, r.x); err != nil {
		t.Fatalf("a figaro given mint could not mint: %v", err)
	}
}

func TestUnderAPolicyBothThePolicyAndTheGrantMustAllowANewWithKeyGrantsOn(t *testing.T) {
	r := newLicenseRig(t, guardPolicy)
	keyGrants(r, true)
	if _, err := r.createAs(t, r.x); err == nil || !strings.Contains(err.Error(), "may not create a top-level figaro") {
		t.Fatalf("a policy granting mint let a figaro with no mint written new: %v", err)
	}
	giveMintGrant(t, r, r.x)
	if _, err := r.createAs(t, r.x); err != nil {
		t.Fatalf("policy and grant both allow: %v", err)
	}
	text := strings.ReplaceAll(mintlessPolicy, "SELF", strconv.Itoa(os.Getuid()))
	if err := os.WriteFile(r.policy, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.a.Policies.Reload(r.policy); err != nil {
		t.Fatal(err)
	}
	if _, err := r.createAs(t, r.x); err == nil {
		t.Fatal("the grant let a figaro new under a policy that denies mint")
	}
}

// Gluck's rollout call: with [authz] enforce_key_grants off, the retired
// key still licenses, and a mint the grant would refuse is only counted.
func TestWithKeyGrantsOffTheMintKeyStillLicenses(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	keyGrants(r, false)
	giveMint(t, r, r.x)
	if _, err := r.createAs(t, r.x); err != nil {
		t.Fatalf("system.fanout.mint stopped licensing with the switch off: %v", err)
	}
	if n := r.a.Policies.WouldRefused(authz.KeyGrants); n != 1 {
		t.Fatalf("would-refuse counter = %d, want 1", n)
	}
}
