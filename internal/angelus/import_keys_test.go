package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
)

// An import births an aria from a form the caller wrote: the keys a set
// would refuse that caller are refused here too.
func TestAnImportCarriesOnlyKeysItsCallerMaySet(t *testing.T) {
	r := newRigConfigured(t, remoteSetsPolicy, "")
	plain := map[string]any{"mantra": "carried"}
	for name, kv := range map[string]map[string]any{
		"owner-held provider": {"system.provider": "anthropic"},
		"owner-held wire dir": {"system.environment.figaro_wire_dir": t.TempDir()},
		"owner-held decide":   {"system.decide.base_url": "http://127.0.0.1:1/"},
		"protected sandbox":   {"system.sandbox": "judge"},
		"protected grants":    {form.GrantsKey: []string{"x"}},
	} {
		for _, where := range []string{"form", "outfit"} {
			req := rpc.ImportRequest{Outfit: "mock", Messages: []message.Message{}}
			req.Form = *heldPatch(t, plain)
			req.OutfitPatch = *heldPatch(t, map[string]any{"system.model": "m"})
			if where == "form" {
				req.Form = *heldPatch(t, kv)
			} else {
				req.OutfitPatch = *heldPatch(t, kv)
			}
			before := len(r.backend.ConversationIDs())
			raw, _ := json.Marshal(req)
			_, err := r.h[rpc.MethodImport](as("user", "remote"), raw)
			if err == nil || !(strings.Contains(err.Error(), "owner-held") || strings.Contains(err.Error(), "protected")) {
				t.Errorf("%s in the %s: a non-owner import passed: %v", name, where, err)
			}
			if after := len(r.backend.ConversationIDs()); after != before {
				t.Errorf("%s in the %s: a refused import left an aria", name, where)
			}
			if _, err := r.h[rpc.MethodImport](asOwner(), raw); err != nil {
				t.Errorf("control: %s in the %s: the owner's import: %v", name, where, err)
			}
		}
	}
	req := rpc.ImportRequest{Outfit: "mock", Form: *heldPatch(t, plain)}
	raw, _ := json.Marshal(req)
	if _, err := r.h[rpc.MethodImport](as("user", "remote"), raw); err != nil {
		t.Fatalf("control: a non-owner import of plain keys: %v", err)
	}
}
