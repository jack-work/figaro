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

func TestTheOldNameIsReadWhenAloneAndBothNamesAreRefused(t *testing.T) {
	with := func(kv map[string]json.RawMessage) form.Snapshot {
		return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, kv, nil))
	}
	old := with(map[string]json.RawMessage{form.RetiredGrantsKey: json.RawMessage(`["read:*"]`)})
	raw, key, ok, err := form.OwnScopes(old)
	if err != nil || !ok || key != form.RetiredGrantsKey || string(raw) != `["read:*"]` {
		t.Fatalf("old name alone: %s %s %v %v", raw, key, ok, err)
	}
	if err := form.CheckScopes(with(map[string]json.RawMessage{form.RetiredGrantsKey: json.RawMessage(`[" a"]`)})); err == nil || !strings.Contains(err.Error(), form.RetiredGrantsKey) {
		t.Errorf("a malformed old key is named by its own name: %v", err)
	}
	both := with(map[string]json.RawMessage{form.RetiredGrantsKey: json.RawMessage(`["*:*"]`), form.ScopesKey: json.RawMessage(`["read:*"]`)})
	if _, _, ok, err := form.OwnScopes(both); err == nil || ok || !strings.Contains(err.Error(), "both set") {
		t.Fatalf("both: %v %v", ok, err)
	}
	if err := form.CheckScopes(both); err == nil {
		t.Fatal("CheckScopes passed a board with both names")
	}
}

func TestBothNamesAreProtected(t *testing.T) {
	for _, k := range []string{form.ScopesKey, form.RetiredGrantsKey, form.RetiredGrantsKey + "[0]"} {
		if _, ok := form.Protected(k); !ok {
			t.Errorf("%s is not protected", k)
		}
	}
}

func TestAWriteOfTheOldNameIsRefusedAndItsRemovalIsNot(t *testing.T) {
	old := form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{form.RetiredGrantsKey: json.RawMessage(`["read:*"]`)}, nil))
	set := old.Apply(form.Build(old, map[string]json.RawMessage{form.RetiredGrantsKey: json.RawMessage(`["*:*"]`)}, nil))
	if err := form.RefuseRetiredGrantsWrite(old, set); err == nil || !strings.Contains(err.Error(), form.ScopesKey) {
		t.Fatalf("set of the old name: %v", err)
	}
	fresh := form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{form.RetiredGrantsKey: json.RawMessage(`[]`)}, nil))
	if err := form.RefuseRetiredGrantsWrite(form.Snapshot{}, fresh); err == nil {
		t.Fatal("a first write of the old name passed")
	}
	moved := old.Apply(form.Build(old, map[string]json.RawMessage{form.ScopesKey: json.RawMessage(`["read:*"]`)}, []string{form.RetiredGrantsKey}))
	if err := form.RefuseRetiredGrantsWrite(old, moved); err != nil {
		t.Fatalf("moving to the new name: %v", err)
	}
	if err := form.RefuseRetiredGrantsWrite(old, old); err != nil {
		t.Fatalf("an untouched old key: %v", err)
	}
}
