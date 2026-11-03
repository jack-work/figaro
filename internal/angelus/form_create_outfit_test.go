package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

// `figaro form new -O <outfit>` sends no patch of its own: the outfit is the
// whole of what the form is born with, so the emptiness check must see the
// dressed patch, not the bare one.
func TestAFormBornOfAnOutfitAloneIsNotEmpty(t *testing.T) {
	r := newGrantRig(t)

	raw, err := r.call(asOwner(), rpc.MethodFormCreate, rpc.FormCreateRequest{Outfits: []string{"mock"}})
	if err != nil {
		t.Fatalf("an outfit-only form was refused: %v", err)
	}
	var resp rpc.FormCreateResponse
	b, _ := json.Marshal(raw)
	if err := json.Unmarshal(b, &resp); err != nil || resp.FormID == "" {
		t.Fatalf("an outfit-only form came back without an id: %s (%v)", b, err)
	}
	if v := keyOnForm(t, r, resp.FormID, "system.model"); v != "m" {
		t.Errorf("the outfit-only form does not carry its outfit: system.model = %s", v)
	}

	for name, req := range map[string]rpc.FormCreateRequest{
		"no patch, no outfit":       {},
		"an empty patch, no outfit": {Patch: &rpc.FormPatch{}},
	} {
		if _, err := r.call(asOwner(), rpc.MethodFormCreate, req); err == nil || !strings.Contains(err.Error(), "names nothing") {
			t.Errorf("%s: want the empty-form refusal, got %v", name, err)
		}
	}
}

func keyOnForm(t *testing.T, r grantRig, id, key string) string {
	t.Helper()
	snap, err := r.a.Backend.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	v := snap.Lookup(key)
	if v == nil {
		return ""
	}
	return *v
}
