package angelus_test

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// The store records system.retired at runtime.retired, so neither snapshot
// the key guard compares holds system.retired; its removal, which unretires,
// must still be judged as the owner-held write it is.
func TestOnlyTheOwnerUnretiresByRemovingTheRetireRequest(t *testing.T) {
	r, p, _, _ := guardChain(t)
	grantTo(t, r, "user:mallory", "read:*", "set:*")
	if _, err := r.call(asOwner(), rpc.MethodSet, withAria(t, p, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{store.SystemRetiredKey: store.RetiredManual})})); err != nil {
		t.Fatal(err)
	}
	retired := func() bool {
		snap, err := r.backend.FormState(p)
		if err != nil {
			t.Fatal(err)
		}
		return store.RetiredIn(snap)
	}
	if !retired() {
		t.Fatal("the owner's retire did not record")
	}

	unretire := form.Build(form.Snapshot{}, nil, []string{store.SystemRetiredKey})
	_, err := r.call(mallory(), rpc.MethodSet, withAria(t, p, rpc.SetRequest{Patch: unretire}))
	if err == nil || !strings.Contains(err.Error(), store.SystemRetiredKey) {
		t.Fatalf("a principal granted set:* unretired %s by removing system.retired: %v", p, err)
	}
	if !retired() {
		t.Fatal("a refused unretire left the figaro unretired")
	}

	if _, err := r.call(asOwner(), rpc.MethodSet, withAria(t, p, rpc.SetRequest{Patch: unretire})); err != nil || retired() {
		t.Fatalf("the owner could not unretire by removing system.retired: %v", err)
	}
}
