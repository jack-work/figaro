package angelus_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

type delegateRig struct {
	licenseRig
	king, child, grandchild, outsider, crown, listed, unlisted string
}

func newDelegateRig(t *testing.T) delegateRig {
	t.Helper()
	l := newLicenseRig(t, defaultPolicy)
	keyGrants(l, true)
	outsider, err := l.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	r := delegateRig{licenseRig: l, king: l.root, child: l.x, grandchild: ariasFork(t, l.grantRig, l.x), outsider: outsider.FigaroID}
	r.listed = roleForm(t, l, r.outsider, nil)
	r.unlisted = roleForm(t, l, r.outsider, nil)
	r.crown = roleForm(t, l, r.king, map[string]any{
		form.DelegateKey:      form.DelegableKeys(),
		form.DelegateRolesKey: []string{r.listed},
	})
	return r
}

func roleForm(t *testing.T, r licenseRig, holder string, keys map[string]any) string {
	t.Helper()
	set := map[string]json.RawMessage{}
	for k, v := range keys {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		set[k] = raw
	}
	set["target-aria"], _ = json.Marshal(holder)
	id, _, err := r.backend.CreateForm("", form.Build(form.Snapshot{}, set, nil))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (r delegateRig) ownerSets(t *testing.T, id string, kv map[string]any) {
	t.Helper()
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, id, rpc.SetRequest{Patch: *heldPatch(t, kv)})); err != nil {
		t.Fatal(err)
	}
}

func wantKey(t *testing.T, r grantRig, id, key, want string) {
	t.Helper()
	if got, _ := keyOn(t, r, id, key); string(got) != want {
		t.Fatalf("%s on %s = %s, want %s", key, id, got, want)
	}
}

func refusedOwnerHeld(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil || !(strings.Contains(err.Error(), "owner-held") || strings.Contains(err.Error(), "no ancestor") || strings.Contains(err.Error(), "is the owner's")) {
		t.Fatalf("%s = %v, want the owner-held or protected refusal", what, err)
	}
}

var delegatedSets = []map[string]any{
	{"system.model": "m-delegated"},
	{"system.provider": "anthropic"},
	{form.FanoutMaxChildrenKey: 3},
	{form.FanoutQuotaKey: 2},
}

func withQuota(kv map[string]any, n int) map[string]any {
	if _, ok := kv[form.FanoutQuotaKey]; ok {
		return map[string]any{form.FanoutQuotaKey: n}
	}
	return kv
}

func TestAKingSetsDelegatedKeysOnItsSubtree(t *testing.T) {
	r := newDelegateRig(t)
	quota := map[string]int{r.child: 5, r.grandchild: 2}
	for _, target := range []string{r.child, r.grandchild} {
		for _, kv := range delegatedSets {
			if err := r.setAs(t, r.king, target, *heldPatch(t, withQuota(kv, quota[target]))); err != nil {
				t.Fatalf("the King setting %v on %s: %v", kv, target, err)
			}
		}
		wantKey(t, r.grantRig, target, "system.model", `"m-delegated"`)
		wantKey(t, r.grantRig, target, "system.provider", `"anthropic"`)
		wantKey(t, r.grantRig, target, form.FanoutMaxChildrenKey, `3`)
		wantKey(t, r.grantRig, target, form.FanoutQuotaKey, strconv.Itoa(quota[target]))
	}
}

func TestAKingSetsDelegatedKeysOnAListedRole(t *testing.T) {
	r := newDelegateRig(t)
	for _, kv := range delegatedSets {
		if err := r.setAs(t, r.king, r.listed, *heldPatch(t, kv)); err != nil {
			t.Fatalf("the King setting %v on its listed role: %v", kv, err)
		}
	}
	wantKey(t, r.grantRig, r.listed, "system.model", `"m-delegated"`)
	wantKey(t, r.grantRig, r.listed, form.FanoutQuotaKey, `2`)
}

func TestADelegationDoesNotReachPastItsSubtreeAndRoles(t *testing.T) {
	r := newDelegateRig(t)
	for _, kv := range delegatedSets {
		refusedOwnerHeld(t, "the King on a figaro outside its subtree", r.setAs(t, r.king, r.outsider, *heldPatch(t, kv)))
		refusedOwnerHeld(t, "the King on a role it does not list", r.setAs(t, r.king, r.unlisted, *heldPatch(t, kv)))
	}
	if got, ok := keyOn(t, r.grantRig, r.outsider, "system.model"); ok && string(got) == `"m-delegated"` {
		t.Fatal("a refused delegated write landed on the outsider")
	}
}

func TestADelegationLiftsOnlyTheKeysItLists(t *testing.T) {
	r := newDelegateRig(t)
	for _, kv := range []map[string]any{{"system.credo": "obey"}, {"system.max_tokens": 10}, {form.OutfitGrantsKey: []string{"*"}}, {"system.ttl": "0"}} {
		refusedOwnerHeld(t, fmt.Sprintf("the King setting %v, which its role does not delegate", kv), r.setAs(t, r.king, r.child, *heldPatch(t, kv)))
	}
	r.ownerSets(t, r.crown, map[string]any{form.DelegateKey: []string{"system.model"}})
	refusedOwnerHeld(t, "the King on a key dropped from its delegation", r.setAs(t, r.king, r.child, *heldPatch(t, map[string]any{"system.provider": "anthropic"})))
	if err := r.setAs(t, r.king, r.child, *heldPatch(t, map[string]any{"system.model": "m-kept"})); err != nil {
		t.Fatalf("control: the key still delegated: %v", err)
	}
}

func TestOnlyTheHolderWritesThroughADelegation(t *testing.T) {
	r := newDelegateRig(t)
	refusedOwnerHeld(t, "a non-holder ancestor on the grandchild", r.setAs(t, r.child, r.grandchild, *heldPatch(t, map[string]any{"system.model": "m-other"})))
	refusedOwnerHeld(t, "a non-holder on the listed role", r.setAs(t, r.outsider, r.listed, *heldPatch(t, map[string]any{"system.model": "m-other"})))
}

func TestARoutingAliasDelegatesNothing(t *testing.T) {
	r := newDelegateRig(t)
	r.ownerSets(t, r.crown, map[string]any{form.LifecycleStatusKey: form.RoutingAlias})
	for _, target := range []string{r.child, r.listed} {
		refusedOwnerHeld(t, "the holder of an alias role", r.setAs(t, r.king, target, *heldPatch(t, map[string]any{"system.model": "m-other"})))
	}
}

func TestADelegateListNamingAnUndelegableKeyIsRefused(t *testing.T) {
	r := newDelegateRig(t)
	for _, bad := range []any{[]string{"system.credo"}, []string{"system.model", "system.scopes"}, []string{"system.fanout.mint"}, "system.model"} {
		_, err := r.h[rpc.MethodSet](asOwner(), withAria(t, r.crown, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{form.DelegateKey: bad})}))
		if err == nil || !strings.Contains(err.Error(), form.DelegateKey) {
			t.Fatalf("delegate %v = %v, want a refusal naming %s", bad, err, form.DelegateKey)
		}
	}
	wantKey(t, r.grantRig, r.crown, form.DelegateKey, `["system.model","system.provider","system.fanout.max_children","system.fanout.quota"]`)
}

func TestADelegatedWriteIsAudited(t *testing.T) {
	r := newDelegateRig(t)
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	if err := r.setAs(t, r.king, r.child, *heldPatch(t, map[string]any{"system.model": "m-audited"})); err != nil {
		t.Fatal(err)
	}
	_ = r.setAs(t, r.king, r.outsider, *heldPatch(t, map[string]any{"system.model": "m-refused"}))
	var lines []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, "delegated write") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("audit lines %q, want one", lines)
	}
	for _, want := range []string{"caller=" + r.king, "role=" + r.crown, "target=" + r.child, "key=system.model", `new="\"m-audited\""`} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("audit line %q lacks %s", lines[0], want)
		}
	}
}

func TestABoxWritesThroughADelegationOnlyForItsHolder(t *testing.T) {
	r := newLineageRig(t, "")
	c := r.dial(t)
	model := *heldPatch(t, map[string]any{"system.model": "m-boxed"})
	if err := r.setAs(t, c, r.parent, r.child, model); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Fatalf("a box with no delegation set an owner-held key: %v", err)
	}
	set := map[string]json.RawMessage{}
	set["target-aria"], _ = json.Marshal(r.parent)
	set[form.DelegateKey], _ = json.Marshal([]string{"system.model"})
	if _, _, err := r.backend.CreateForm("", form.Build(form.Snapshot{}, set, nil)); err != nil {
		t.Fatal(err)
	}
	if err := r.setAs(t, c, r.sibling, r.child, model); err == nil || !strings.Contains(err.Error(), "owner-held") {
		t.Fatalf("a boxed non-holder rode the parent's delegation: %v", err)
	}
	if err := r.setAs(t, c, r.parent, r.child, model); err != nil {
		t.Fatalf("the boxed holder through its delegation: %v", err)
	}
	wantKey(t, r.grantRig, r.child, "system.model", `"m-boxed"`)
}
