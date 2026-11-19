package form_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func TestCheckScopesNamesTheEntryAProviderWouldReadDifferently(t *testing.T) {
	with := func(v string) form.Snapshot {
		return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{form.ScopesKey: json.RawMessage(v)}, nil))
	}
	for _, bad := range []struct{ raw, names string }{
		{`[" a"]`, `" a"`}, {`["a "]`, `"a "`}, {`["a","\tb"]`, `"\tb"`}, {`["a",""]`, `""`},
		{`"a"`, "array of strings"}, {`[1]`, "array of strings"}, {`null`, "array of strings"},
	} {
		if err := form.CheckScopes(with(bad.raw)); err == nil || !strings.Contains(err.Error(), bad.names) {
			t.Errorf("%s: %v", bad.raw, err)
		}
	}
	for _, ok := range []string{`[]`, `["a","read:aria:*"]`} {
		if err := form.CheckScopes(with(ok)); err != nil {
			t.Errorf("control %s: %v", ok, err)
		}
	}
	if err := form.CheckScopes(form.Snapshot{}); err != nil {
		t.Errorf("control: unset: %v", err)
	}
}

func TestAListAtSystemGrantsFailsTheScopesClosed(t *testing.T) {
	with := func(kv map[string]json.RawMessage) form.Snapshot {
		return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, kv, nil))
	}
	old := with(map[string]json.RawMessage{form.GrantsKey: json.RawMessage(`["read:*"]`)})
	if raw, ok, err := form.OwnScopes(old); err == nil || ok || raw != nil || !strings.Contains(err.Error(), form.ScopesKey) {
		t.Fatalf("a list at the old name was read as no scopes: %s %v %v", raw, ok, err)
	}
	if err := form.CheckScopes(old); err == nil {
		t.Fatal("CheckScopes passed a list at system.grants")
	}
	beside := with(map[string]json.RawMessage{form.GrantsKey + ".outfits": json.RawMessage(`["hk"]`), form.ScopesKey: json.RawMessage(`["read:*"]`)})
	if raw, ok, err := form.OwnScopes(beside); err != nil || !ok || string(raw) != `["read:*"]` {
		t.Fatalf("control: grants beside scopes: %s %v %v", raw, ok, err)
	}
}

func TestSystemGrantsIsOwnerHeldAndNotProtected(t *testing.T) {
	for _, k := range []string{form.GrantsKey, form.GrantsKey + ".anything", form.GrantsKey + "[0]"} {
		if _, ok := form.OwnerHeld(k); !ok {
			t.Errorf("%s is not owner-held", k)
		}
		if _, ok := form.Protected(k); ok {
			t.Errorf("%s is protected: an ancestor would write the owner's grants", k)
		}
	}
	if _, ok := form.Protected(form.ScopesKey); !ok {
		t.Error("control: system.scopes is not protected")
	}
}
