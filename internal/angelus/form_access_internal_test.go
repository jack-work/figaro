package angelus

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func accessPaths() []string {
	paths := []string{"system", "system.added_tomorrow", "mantra", "notes.today", "x[0]", "turn.state"}
	add := func(k string) { paths = append(paths, k, k+".child", k+"[0]") }
	for _, k := range form.WellKnownKeys() {
		add(strings.ReplaceAll(k.Key, "<id>", "a1"))
	}
	for _, k := range form.OwnerHeldKeys() {
		add(k)
	}
	for _, k := range boxWritable {
		add(k)
	}
	for _, k := range boxHeldKeys {
		add(k.key)
	}
	return paths
}

// guardOracle restates, independently of deny(), what refuseCallerKeys and
// CheckWritable refused before access existed.
func guardOracle(c callerKeys, path string) (managed, refused bool) {
	if _, ok := form.OwnerHeld(path); ok && !c.owns {
		return false, true
	}
	if c.boxed {
		for _, k := range boxHeldKeys {
			if rpc.KeyUnder(path, k.key) {
				return false, true
			}
		}
		if strings.HasPrefix(path, "system.") {
			listed := false
			for _, k := range boxWritable {
				if rpc.KeyUnder(path, k) {
					listed = true
				}
			}
			if !listed {
				return false, true
			}
		}
	}
	return false, false
}

func checkWritableRefuses(path string) bool {
	raw, _ := json.Marshal("v")
	p := form.Build(form.Snapshot{}, map[string]json.RawMessage{path: raw}, nil)
	return form.CheckWritable(p, false) != nil
}

// The deny list a form read carries is the guard's own list, so the client can
// never be told a key is writable that the daemon refuses, or the reverse.
func TestTheAccessDenyListIsTheWriteGuard(t *testing.T) {
	callers := map[string]callerKeys{
		"owner":     {owns: true},
		"non-owner": {},
		"owner box": {owns: true, boxed: true, box: "b"},
		"other box": {boxed: true, box: "b"},
	}
	for name, c := range callers {
		a := &rpc.FormAccess{Mode: rpc.FormWritable, Deny: c.deny()}
		for _, path := range accessPaths() {
			d, denied := a.Refusal(path)
			_, refused := guardOracle(c, path)
			guardSays := c.refusal(path) != nil
			if guardSays != refused {
				t.Errorf("%s %s: refuseCallerKeys %v, the rules before access %v", name, path, guardSays, refused)
			}
			managed := checkWritableRefuses(path)
			if refused || managed {
				if !denied {
					t.Errorf("%s %s: the guard refuses it but access allows it", name, path)
				}
				if managed && !refused && d.Kind != rpc.DenySystemManaged {
					t.Errorf("%s %s: denied as %s, want system-managed", name, path, d.Kind)
				}
				continue
			}
			if denied {
				t.Errorf("%s %s: access denies it (%s) but nothing refuses the write", name, path, d.Kind)
			}
		}
	}
}

func TestACatalogAndAnIntrinsicReadOnly(t *testing.T) {
	h := &handlers{}
	for _, id := range catalogIDs {
		if a := h.formAccess(t.Context(), id, nil); a.Writable() {
			t.Errorf("%s reads writable", id)
		}
	}
	runtime, _ := json.Marshal(rpc.FormRequest{Intrinsic: "runtime"})
	if a := h.formAccess(t.Context(), "n1", runtime); a.Writable() {
		t.Error("an intrinsic form reads writable")
	}
}
