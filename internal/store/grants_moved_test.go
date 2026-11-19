package store

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func unmarkGrantsMoved(t *testing.T, root string) {
	t.Helper()
	f, err := readSchema(root)
	if err != nil {
		t.Fatal(err)
	}
	f.GrantsMoved = false
	if err := writeSchema(root, f); err != nil {
		t.Fatal(err)
	}
}

func boardWith(t *testing.T, be *XwalBackend, kv map[string]string) string {
	t.Helper()
	set := map[string]json.RawMessage{}
	for k, v := range kv {
		set[k] = json.RawMessage(v)
	}
	id, _, err := be.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"mantra": json.RawMessage(`"m"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.ApplyFormPrivileged(id, form.Build(form.Snapshot{}, set, nil)); err != nil {
		t.Fatal(err)
	}
	return id
}

func keyOf(t *testing.T, be *XwalBackend, id, key string) string {
	t.Helper()
	snap, err := be.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := snap.Get(key)
	return string(v)
}

// Defends review M1 on #171: a board from before system.grants became an
// object keeps its scopes, moved once at open, rather than failing closed.
func TestAListAtSystemGrantsMovesToSystemScopesOnceAtOpen(t *testing.T) {
	root := t.TempDir()
	be := openStore(t, root)
	old := boardWith(t, be, map[string]string{form.GrantsKey: `["read:*"]`})
	both := boardWith(t, be, map[string]string{form.GrantsKey: `["*:*"]`, form.ScopesKey: `["read:*"]`})
	granted := boardWith(t, be, map[string]string{form.OutfitGrantsKey: `["hk"]`, form.ScopesKey: `["send:*"]`})
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	unmarkGrantsMoved(t, root)

	be = openStore(t, root)
	if got := keyOf(t, be, old, form.ScopesKey); got != `["read:*"]` {
		t.Errorf("moved scopes = %s", got)
	}
	if got := keyOf(t, be, old, form.GrantsKey); got != "" {
		t.Errorf("the list stayed at %s: %s", form.GrantsKey, got)
	}
	if keyOf(t, be, both, form.GrantsKey) != `["*:*"]` || keyOf(t, be, both, form.ScopesKey) != `["read:*"]` {
		t.Errorf("a conflict was rewritten: grants %s scopes %s", keyOf(t, be, both, form.GrantsKey), keyOf(t, be, both, form.ScopesKey))
	}
	if keyOf(t, be, granted, form.OutfitGrantsKey) != `["hk"]` || keyOf(t, be, granted, form.ScopesKey) != `["send:*"]` {
		t.Errorf("an object at %s was touched", form.GrantsKey)
	}
	if f, err := readSchema(root); err != nil || !f.GrantsMoved {
		t.Fatalf("the move is not marked in schema.json: %+v %v", f, err)
	}

	late := boardWith(t, be, map[string]string{form.GrantsKey: `["send:*"]`})
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	be = openStore(t, root)
	defer be.Close()
	if got := keyOf(t, be, late, form.GrantsKey); got != `["send:*"]` {
		t.Errorf("a marked store moved again: %s = %s", form.GrantsKey, got)
	}
}
