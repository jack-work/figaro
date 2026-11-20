package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// #155 M1: a fork carries a copy of its parent's history, so anyone but the
// owner who forks, or form-forks, may keep or shorten the ttl and keepalive
// it inherits and never lengthen or drop them, by patch, outfit or a later
// set. Free choice stays with root births.
func TestANonOwnersForkOnlyShortensTheLifetimeItInherits(t *testing.T) {
	r := newRigWith(t, lifetimeLentPolicy)
	guest := as("user", "guest")
	resp, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	parent := resp.FigaroID
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, parent, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{
		store.SystemTTLKey: "1h", store.SystemKeepaliveKey: "30m"})})); err != nil {
		t.Fatal(err)
	}
	drop := func(key string) *form.Patch {
		p := form.Build(form.Snapshot{}, nil, []string{key})
		return &p
	}
	fork := func(p *form.Patch, outfits ...string) (string, error) {
		raw, _ := json.Marshal(rpc.ForkRequest{FigaroID: parent, Patch: p, Outfits: outfits})
		out, err := r.h[rpc.MethodFork](guest, raw)
		if err != nil {
			return "", err
		}
		return out.(rpc.ForkResponse).Alternative, nil
	}
	longer := func(what string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "never lengthens or drops") {
			t.Errorf("%s: %v", what, err)
		}
	}

	plain, err := fork(nil)
	if err != nil {
		t.Fatalf("a plain fork keeping the inherited lifetime: %v", err)
	}
	if v, _ := keyOn(t, r, plain, store.SystemTTLKey); string(v) != `"1h"` {
		t.Errorf("the plain fork's ttl = %s, want \"1h\"", v)
	}
	_, err = fork(drop(store.SystemTTLKey))
	longer("a fork dropping the ttl", err)
	_, err = fork(heldPatch(t, map[string]any{store.SystemTTLKey: "0"}))
	longer("a fork setting ttl 0", err)
	_, err = fork(heldPatch(t, map[string]any{store.SystemTTLKey: "2h"}))
	longer("a fork lengthening the ttl", err)
	_, err = fork(drop(store.SystemKeepaliveKey))
	longer("a fork dropping the keepalive", err)
	_, err = fork(heldPatch(t, map[string]any{store.SystemKeepaliveKey: "2h"}))
	longer("a fork lengthening the keepalive", err)
	_, err = fork(nil, "long-lived")
	longer("a fork whose outfit lengthens the lifetime", err)
	if _, err := fork(heldPatch(t, map[string]any{store.SystemTTLKey: "10m", store.SystemKeepaliveKey: "5m"})); err != nil {
		t.Errorf("a fork shortening its lifetime: %v", err)
	}

	set := func(kv map[string]any) error {
		_, err := r.h[rpc.MethodSet](guest, withAria(t, plain, rpc.SetRequest{Patch: *heldPatch(t, kv)}))
		return err
	}
	if _, err := r.h[rpc.MethodSet](guest, withAria(t, plain, rpc.SetRequest{Patch: *drop(store.SystemTTLKey)})); err == nil {
		t.Error("the guest unset the ttl of its fork of the owner's aria")
	}
	if err := set(map[string]any{store.SystemTTLKey: "0"}); err == nil {
		t.Error("the guest set ttl 0 on its fork of the owner's aria")
	}
	if err := set(map[string]any{store.SystemTTLKey: "2h"}); err == nil {
		t.Error("the guest lengthened the ttl of its fork of the owner's aria")
	}
	if err := set(map[string]any{store.SystemTTLKey: "20m"}); err != nil {
		t.Errorf("the guest was refused a shorter ttl on its own fork: %v", err)
	}

	own, err := r.create(guest, "mock")
	if err != nil {
		t.Fatal(err)
	}
	for _, ttl := range []string{"1h", "6h", "0"} {
		if _, err := r.h[rpc.MethodSet](guest, withAria(t, own.FigaroID, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{store.SystemTTLKey: ttl})})); err != nil {
			t.Errorf("the guest was refused ttl %s on its own root birth: %v", ttl, err)
		}
	}
}

func TestANonOwnersFormForkOnlyShortensTheLifetimeItInherits(t *testing.T) {
	r := newRigWith(t, lifetimeLentPolicy)
	guest := as("user", "guest")
	raw, _ := json.Marshal(rpc.FormCreateRequest{Patch: heldPatch(t, map[string]any{"name": "x", store.SystemTTLKey: "1h"})})
	out, err := r.h[rpc.MethodFormCreate](asOwner(), raw)
	if err != nil {
		t.Fatal(err)
	}
	parent := out.(rpc.FormCreateResponse).FormID
	copyOf := func(p *form.Patch) error {
		raw, _ := json.Marshal(rpc.FormCreateRequest{Parent: parent, Patch: p})
		_, err := r.h[rpc.MethodFormCreate](guest, raw)
		return err
	}
	bind := func(p *form.Patch) error {
		raw, _ := json.Marshal(rpc.FormBindRequest{Parent: parent, Patch: p, Cwd: r.cwd})
		_, err := r.h[rpc.MethodFormBind](guest, raw)
		return err
	}
	dropped := form.Build(form.Snapshot{}, map[string]json.RawMessage{"mantra": json.RawMessage(`"m"`)}, []string{store.SystemTTLKey})
	for what, err := range map[string]error{
		"form copy dropping the ttl": copyOf(&dropped),
		"form copy setting ttl 0":    copyOf(heldPatch(t, map[string]any{"mantra": "m", store.SystemTTLKey: "0"})),
		"bind dropping the ttl":      bind(&dropped),
		"bind lengthening the ttl":   bind(heldPatch(t, map[string]any{store.SystemTTLKey: "3h"})),
	} {
		if err == nil || !strings.Contains(err.Error(), "never lengthens or drops") {
			t.Errorf("%s: %v", what, err)
		}
	}
	if err := copyOf(heldPatch(t, map[string]any{"mantra": "m", store.SystemTTLKey: "10m"})); err != nil {
		t.Errorf("a form copy shortening its ttl: %v", err)
	}
	if err := bind(heldPatch(t, map[string]any{store.SystemTTLKey: "10m"})); err != nil {
		t.Errorf("a bind shortening its ttl: %v", err)
	}
}

// An outfit named on a set is the caller's write for the lifetime keys it
// carries: only a root birth takes them on the outfit's word.
func TestAnOutfitOnASetIsTheCallersLifetimeWrite(t *testing.T) {
	r := newRigWith(t, lifetimeLentPolicy)
	guest := as("user", "guest")
	resp, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	owners := resp.FigaroID
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, owners, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{store.SystemTTLKey: "1h"})})); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(rpc.ForkRequest{FigaroID: owners})
	out, err := r.h[rpc.MethodFork](guest, raw)
	if err != nil {
		t.Fatal(err)
	}
	fork := out.(rpc.ForkResponse).Alternative
	own, err := r.create(guest, "mock")
	if err != nil {
		t.Fatal(err)
	}
	set := func(id string, outfit string) error {
		_, err := r.h[rpc.MethodSet](guest, withAria(t, id, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"note": "n"}), Outfits: []string{outfit}}))
		return err
	}
	if err := set(fork, "long-lived"); err == nil {
		t.Error("the guest lengthened its fork of the owner's aria to 24h through an outfit")
	}
	if v, _ := keyOn(t, r, fork, store.SystemTTLKey); string(v) != `"1h"` {
		t.Errorf("the fork's ttl = %s, want \"1h\"", v)
	}
	if err := set(owners, "short-lived"); err == nil {
		t.Error("the guest set a 1m ttl on the owner's aria through an outfit")
	}
	if v, _ := keyOn(t, r, owners, store.SystemTTLKey); string(v) != `"1h"` {
		t.Errorf("the owner's ttl = %s, want \"1h\"", v)
	}
	if err := set(own.FigaroID, "long-lived"); err != nil {
		t.Errorf("the guest was refused an outfit's lifetime on its own root birth: %v", err)
	}
}

// form.create judged its patch before it folded in the outfits it names, so
// an outfit lengthened the lifetime a form copy inherits.
func TestAFormCopysOutfitIsHeldToTheLifetimeItInherits(t *testing.T) {
	r := newRigWith(t, lifetimeLentPolicy)
	guest := as("user", "guest")
	raw, _ := json.Marshal(rpc.FormCreateRequest{Patch: heldPatch(t, map[string]any{"name": "x", store.SystemTTLKey: "1h"})})
	out, err := r.h[rpc.MethodFormCreate](asOwner(), raw)
	if err != nil {
		t.Fatal(err)
	}
	parent := out.(rpc.FormCreateResponse).FormID
	create := func(parent string, outfits ...string) (string, error) {
		raw, _ := json.Marshal(rpc.FormCreateRequest{Parent: parent, Outfits: outfits, Patch: heldPatch(t, map[string]any{"mantra": "m"})})
		out, err := r.h[rpc.MethodFormCreate](guest, raw)
		if err != nil {
			return "", err
		}
		return out.(rpc.FormCreateResponse).FormID, nil
	}
	if _, err := create(parent, "long-lived"); err == nil || !strings.Contains(err.Error(), "never lengthens or drops") {
		t.Errorf("a form copy whose outfit lengthens the ttl: %v", err)
	}
	if _, err := create(parent, "short-lived"); err != nil {
		t.Errorf("a form copy whose outfit shortens the ttl: %v", err)
	}
	id, err := create("", "long-lived")
	if err != nil {
		t.Fatalf("a root form in an outfit with a lifetime: %v", err)
	}
	if v, _ := keyOn(t, r, id, store.SystemTTLKey); string(v) != `"24h"` {
		t.Errorf("the root form's ttl = %s, want the outfit's", v)
	}
}

// An interior fork cuts the form as it stood at the cut, which may be from
// before the owner gave the aria its lifetime; anyone but the owner gets the
// holder's lifetime when the cut has none or a longer one.
func TestANonOwnersInteriorForkIsHeldToTheHoldersLifetime(t *testing.T) {
	r := newRigWith(t, lifetimeLentPolicy)
	guest := as("user", "guest")
	resp, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	holder := resp.FigaroID
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, holder, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{
		store.SystemTTLKey: "1h", store.SystemKeepaliveKey: "30m"})})); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(rpc.ForkRequest{FigaroID: holder, AtLT: 1})
	out, err := r.h[rpc.MethodFork](guest, raw)
	if err != nil {
		t.Fatalf("the guest's fork at the genesis root: %v", err)
	}
	child := out.(rpc.ForkResponse).Alternative
	for key, want := range map[string]string{store.SystemTTLKey: `"1h"`, store.SystemKeepaliveKey: `"30m"`} {
		if v, _ := keyOn(t, r, child, key); string(v) != want {
			t.Errorf("the interior fork's %s = %s, want the holder's %s", key, v, want)
		}
	}
	out, err = r.h[rpc.MethodFork](asOwner(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := keyOn(t, r, out.(rpc.ForkResponse).Alternative, store.SystemTTLKey); ok {
		t.Errorf("the owner's interior fork took ttl %s; the owner forks as it likes", v)
	}
}

// A seat's role outfit was folded in after the fork's lifetime was judged,
// a guest counted as the holder's heir, and the heir took the holder's ttl
// but not its keepalive.
func TestASeatForkIsHeldToTheHoldersLifetime(t *testing.T) {
	r := newRigWith(t, strings.Replace(lifetimeLentPolicy, `"fork:*",`, `"fork:*", "succeed:*",`, 1))
	guest := as("user", "guest")
	resp, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	holder := resp.FigaroID
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, holder, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{
		store.SystemTTLKey: "1h", store.SystemKeepaliveKey: "30m"})})); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(rpc.FormCreateRequest{Patch: heldPatch(t, map[string]any{
		"name": "seat", "target-aria": holder, "succession-policy": map[string]any{"outfit": "lasting"}})})
	out, err := r.h[rpc.MethodFormCreate](asOwner(), raw)
	if err != nil {
		t.Fatal(err)
	}
	seat := out.(rpc.FormCreateResponse).FormID
	raw, _ = json.Marshal(rpc.ForkRequest{FigaroID: holder, Seat: seat})
	if _, err := r.h[rpc.MethodFork](guest, raw); err == nil || !strings.Contains(err.Error(), "never lengthens or drops") {
		t.Errorf("a guest's seat fork in a role outfit that lengthens the lifetime: %v", err)
	}
	fork, err := r.h[rpc.MethodFork](asOwner(), raw)
	if err != nil {
		t.Fatalf("the owner's seat fork: %v", err)
	}
	heir := fork.(rpc.ForkResponse).Alternative
	for key, want := range map[string]string{store.SystemTTLKey: `"1h"`, store.SystemKeepaliveKey: `"30m"`} {
		if v, _ := keyOn(t, r, heir, key); string(v) != want {
			t.Errorf("the heir's %s = %s, want the holder's %s", key, v, want)
		}
	}
}

// An import and a cast that mints its role are root births: the caller gives
// what it births a lifetime and renews it, as it does what it creates.
func TestAnImportAndACastsRoleAreTheCallersRootBirths(t *testing.T) {
	r := newRigWith(t, strings.Replace(lifetimeLentPolicy, `"fork:*",`, `"fork:*", "cast:*",`, 1))
	guest := as("user", "guest")
	raw, _ := json.Marshal(rpc.ImportRequest{Outfit: "mock", Form: *heldPatch(t, map[string]any{"mantra": "m", store.SystemTTLKey: "2h"})})
	out, err := r.h[rpc.MethodImport](guest, raw)
	if err != nil {
		t.Fatalf("the guest was refused a lifetime on its import: %v", err)
	}
	imported := out.(rpc.ImportResponse).FigaroID
	if _, err := r.h[rpc.MethodSet](guest, withAria(t, imported, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{store.SystemTTLKey: "6h"})})); err != nil {
		t.Errorf("the guest was refused a renewal of its import: %v", err)
	}

	own, err := r.create(guest, "mock")
	if err != nil {
		t.Fatal(err)
	}
	cast, err := r.h[rpc.MethodCast](guest, withAria(t, own.FigaroID, rpc.CastRequest{RolePatch: heldPatch(t, map[string]any{"name": "r", store.SystemTTLKey: "2h"})}))
	if err != nil {
		t.Fatalf("the guest was refused a lifetime on the role its cast mints: %v", err)
	}
	role := cast.(rpc.CastResponse).RoleID
	if _, err := r.h[rpc.MethodSet](guest, withAria(t, role, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{store.SystemTTLKey: "6h"})})); err != nil {
		t.Errorf("the guest was refused a renewal of the role it minted: %v", err)
	}
}
