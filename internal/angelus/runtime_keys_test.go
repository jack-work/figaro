package angelus_test

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

// A birth patch is written by the daemon, past CheckWritable; a caller's
// part of it must not carry a runtime key, or a create could forge
// runtime.forked_from or plant runtime.tombstone.
func TestNoCallerWritesARuntimeKeyAtBirthOrAfter(t *testing.T) {
	r := newRigConfigured(t, defaultPolicy, "")
	made, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{form.ForkedFromKey, form.TombstoneKey, form.StudiesKey, "runtime.anything", "system.forked_from"} {
		p := heldPatch(t, map[string]any{key: "x"})
		_, err := r.call(asOwner(), rpc.MethodCreate, rpc.CreateRequest{Outfits: []string{"mock"}, Cwd: r.cwd, Patch: p})
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("the owner set %s at birth: %v", key, err)
		}
		_, err = r.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: made.FigaroID, Patch: p})
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("the owner set %s on a fork: %v", key, err)
		}
		if _, err := r.call(asOwner(), rpc.MethodSet, withAria(t, made.FigaroID, rpc.SetRequest{Patch: *p})); err == nil {
			t.Errorf("the owner set %s", key)
		}
	}
}
