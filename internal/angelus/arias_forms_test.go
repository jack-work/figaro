package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func mintForm(t *testing.T, r grantRig, parent, name, target string) string {
	t.Helper()
	keys := map[string]json.RawMessage{"brief": json.RawMessage(`"x"`)}
	if name != "" {
		raw, _ := json.Marshal(name)
		keys["name"] = raw
	}
	if target != "" {
		raw, _ := json.Marshal(target)
		keys["target-aria"] = raw
	}
	id, _, err := r.backend.CreateForm(parent, form.Build(form.Snapshot{}, keys, nil))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAriasListsARoleNobodyHoldsAndAQueueFormWithNoTarget(t *testing.T) {
	r := newGrantRig(t)
	vacant := mintForm(t, r, "", "Backend Knight", "gone-aria")
	queue := mintForm(t, r, "", "deploy form", "")
	bare := mintForm(t, r, "", "", "")
	for _, id := range []string{rpc.AriasFormID, rpc.GlobalAriasFormID} {
		snap, err := readArias(t, r, asOwner(), id)
		if err != nil {
			t.Fatal(err)
		}
		ids := "," + ariaIDsIn(snap) + ","
		for _, want := range []string{vacant, queue} {
			if !strings.Contains(ids, ","+want+",") {
				t.Errorf("%s has no row for form %s", id, want)
			}
		}
		if id == rpc.AriasFormID && strings.Contains(ids, ","+bare+",") {
			t.Errorf("%s lists a form with neither a name nor a target", id)
		}
	}
	paths := ariasPaths(t, r)
	snap, _ := readArias(t, r, asOwner(), rpc.AriasFormID)
	for key, want := range map[string]string{"kind": `"form"`, "name": `"Backend Knight"`, "target": `"gone-aria"`} {
		if raw, ok := snap.Get(paths[vacant] + "." + key); !ok || string(raw) != want {
			t.Errorf("vacant role row %s = %s, want %s", key, raw, want)
		}
	}
	if raw, ok := snap.Get(paths[queue] + ".kind"); !ok || string(raw) != `"form"` {
		t.Errorf("queue form row kind = %s", raw)
	}
	if _, ok := snap.Get(paths[queue] + ".target"); ok {
		t.Error("queue form row carries a target")
	}
}

func TestAFormWithoutAnAriaUnderItIsDrawnUnderItsParentForm(t *testing.T) {
	r := newGrantRig(t)
	king := mintForm(t, r, "", "King", "gone-aria")
	child := mintForm(t, r, king, "Lead", "")
	paths := ariasPaths(t, r)
	if got, want := paths[child], paths[king]+".forks."; !strings.HasPrefix(got, want) {
		t.Fatalf("child form drawn at %s, want under %s", got, paths[king])
	}
}
