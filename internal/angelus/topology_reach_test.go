package angelus_test

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

func (r grantRig) setLabeled(t *testing.T, figaro, target string, keys map[string]any) error {
	t.Helper()
	raw, err := rpc.WithCaller(withAria(t, target, rpc.SetRequest{Patch: *heldPatch(t, keys)}), figaro, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.h[rpc.MethodSet](asOwner(), raw)
	return err
}

// F2's control row: under a policy, a labeled figaro on the owner's door
// retired another lineage's figaro. Retire reaches its own lineage only.
func TestALabeledFigaroRetiresOnlyInItsOwnLineage(t *testing.T) {
	r := newRigConfigured(t, defaultPolicy, "")
	mk := func() string {
		out, err := r.create(asOwner(), "mock")
		if err != nil {
			t.Fatal(err)
		}
		return out.FigaroID
	}
	x, y := mk(), mk()
	retire := map[string]any{store.SystemRetiredKey: "manual"}
	err := r.setLabeled(t, x, y, retire)
	if err == nil || !strings.Contains(err.Error(), "own lineage") {
		t.Fatalf("an unrelated figaro retired %s: %v", y, err)
	}
	if err := r.setLabeled(t, x, x, retire); err != nil {
		t.Fatalf("a figaro could not retire itself: %v", err)
	}
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, x, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{form.ScopesKey: []string{"*:*"}})})); err != nil {
		t.Fatal(err)
	}
	if err := r.setLabeled(t, x, y, retire); err != nil {
		t.Fatalf("a written fleet grant did not reach %s: %v", y, err)
	}
}
