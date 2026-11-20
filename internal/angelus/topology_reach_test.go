package angelus_test

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authn"
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

// The peer check pins a figaro's tools to it; their retire beyond the
// lineage rests on the same written grant as a labelled one's, ttl included.
func TestAFigarosToolsRetireBeyondItsLineageOnlyByAWrittenGrant(t *testing.T) {
	r := newRigConfigured(t, defaultPolicy, "")
	mk := func() string {
		out, err := r.create(asOwner(), "mock")
		if err != nil {
			t.Fatal(err)
		}
		return out.FigaroID
	}
	x, y := mk(), mk()
	retire := withAria(t, y, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{store.SystemRetiredKey: "manual", store.SystemTTLKey: "10m"})})
	if _, err := r.h[rpc.MethodSet](authn.WithToolOf(localOwner(), x), retire); err == nil {
		t.Fatalf("an unscoped figaro's tools retired %s in another lineage", y)
	}
	if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, x, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{form.ScopesKey: []string{"*:*"}})})); err != nil {
		t.Fatal(err)
	}
	if _, err := r.h[rpc.MethodSet](authn.WithToolOf(localOwner(), x), retire); err != nil {
		t.Fatalf("a written fleet grant did not reach %s from the tools: %v", y, err)
	}
}

// The review's H1: the grant that carries a tool's retire beyond the lineage
// must cover the key it writes, so a keyed grant on another key (mantra)
// neither retires a stranger nor shortens its lifetime.
func TestAToolsLifetimeGrantBeyondItsLineageCoversTheKeyItWrites(t *testing.T) {
	for _, row := range []struct {
		name   string
		scopes func(y string) []string
		keys   map[string]any
		ok     bool
	}{
		{"write:*#mantra retires", func(string) []string { return []string{"read:*", "write:*#mantra"} }, map[string]any{store.SystemRetiredKey: "manual"}, false},
		{"write:*#mantra shortens", func(string) []string { return []string{"read:*", "write:*#mantra"} }, map[string]any{store.SystemTTLKey: "1s"}, false},
		{"write:aria:<y>#mantra shortens", func(y string) []string { return []string{"read:*", "write:aria:" + y + "#mantra"} }, map[string]any{store.SystemTTLKey: "1s"}, false},
		{"write:*#system.ttl shortens", func(string) []string { return []string{"read:*", "write:*#system.ttl"} }, map[string]any{store.SystemTTLKey: "1s"}, true},
		{"write:* retires with a ttl", func(string) []string { return []string{"read:*", "write:*"} }, map[string]any{store.SystemRetiredKey: "manual", store.SystemTTLKey: "1s"}, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			r := newRigConfigured(t, defaultPolicy, "")
			mk := func() string {
				out, err := r.create(asOwner(), "mock")
				if err != nil {
					t.Fatal(err)
				}
				return out.FigaroID
			}
			x, y := mk(), mk()
			owner := func(id string, keys map[string]any) {
				if _, err := r.h[rpc.MethodSet](asOwner(), withAria(t, id, rpc.SetRequest{Patch: *heldPatch(t, keys)})); err != nil {
					t.Fatal(err)
				}
			}
			owner(y, map[string]any{store.SystemTTLKey: "1h"})
			owner(x, map[string]any{form.ScopesKey: row.scopes(y)})
			if _, err := r.h[rpc.MethodSet](authn.WithToolOf(localOwner(), x), withAria(t, y, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"mantra": "x"})})); err != nil && strings.Contains(row.name, "mantra") {
				t.Fatalf("sanity: the keyed mantra grant did not reach %s: %v", y, err)
			}
			_, err := r.h[rpc.MethodSet](authn.WithToolOf(localOwner(), x), withAria(t, y, rpc.SetRequest{Patch: *heldPatch(t, row.keys)}))
			if (err == nil) != row.ok {
				t.Fatalf("%v from %s's tools on %s: %v, want ok=%v", row.keys, x, y, err, row.ok)
			}
		})
	}
}
