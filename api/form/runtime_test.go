package form_test

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func legacySnapshot() form.Snapshot {
	m := map[string]json.RawMessage{"mantra": json.RawMessage(`"keep me"`), "system.cwd": json.RawMessage(`"/tmp"`)}
	for i, r := range form.RuntimeRenames() {
		raw, _ := json.Marshal(map[string]int{"n": i})
		m[r.Old] = raw
	}
	return form.FromMap(m)
}

func TestMigrationMovesEveryValueToItsNewNameAndLeavesTheRest(t *testing.T) {
	before := legacySnapshot()
	after := before.Apply(form.MigrateRuntimeNames(before))
	for _, r := range form.RuntimeRenames() {
		if after.Has(r.Old) {
			t.Errorf("%s survived the migration", r.Old)
		}
		was, _ := before.Get(r.Old)
		now, ok := after.Get(r.New)
		if !ok || string(now) != string(was) {
			t.Errorf("%s = %s, want %s", r.New, now, was)
		}
	}
	for _, k := range []string{"mantra", "system.cwd"} {
		a, _ := before.Get(k)
		b, _ := after.Get(k)
		if string(a) != string(b) {
			t.Errorf("%s changed: %s -> %s", k, a, b)
		}
	}
	if p := form.MigrateRuntimeNames(after); !p.IsIdentity() {
		t.Errorf("a second migration is not a no-op: %v", p.Entries())
	}
	if form.HoldsRuntimeLegacy(after) {
		t.Error("migrated form still reports a legacy name")
	}
}

func TestReverseMigrationRoundTrips(t *testing.T) {
	before := legacySnapshot()
	after := before.Apply(form.MigrateRuntimeNames(before))
	back := after.Apply(form.RevertRuntimeNames(after))
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(back)
	if string(a) != string(b) {
		t.Errorf("round trip:\n%s\n%s", a, b)
	}
}

func TestMigrationKeepsAValueAlreadyAtTheNewName(t *testing.T) {
	s := form.FromMap(map[string]json.RawMessage{
		"system.tombstone": json.RawMessage(`"old"`),
		form.TombstoneKey:  json.RawMessage(`"new"`),
	})
	after := s.Apply(form.MigrateRuntimeNames(s))
	if v, _ := after.Get(form.TombstoneKey); string(v) != `"new"` || after.Has("system.tombstone") {
		t.Errorf("tombstone %s, legacy held %v", v, after.Has("system.tombstone"))
	}
}

func TestReadersFallBackToTheOldName(t *testing.T) {
	s := form.FromMap(map[string]json.RawMessage{"system.forked_from": json.RawMessage(`"abc"`)})
	if v, ok := form.RuntimeGet(s, form.ForkedFromKey); !ok || string(v) != `"abc"` {
		t.Errorf("fallback read %s %v", v, ok)
	}
	if !form.RuntimeHas(s, form.ForkedFromKey) || form.RuntimeHas(s, form.StudiesKey) {
		t.Error("RuntimeHas")
	}
	both := form.FromMap(map[string]json.RawMessage{"system.forked_from": json.RawMessage(`"old"`), form.ForkedFromKey: json.RawMessage(`"new"`)})
	if v, _ := form.RuntimeGet(both, form.ForkedFromKey); string(v) != `"new"` {
		t.Errorf("the new name loses to the old: %s", v)
	}
	p := form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.forked_from": json.RawMessage(`"abc"`)}, nil)
	if e, ok := form.RuntimeEntry(p, form.ForkedFromKey); !ok || e.NewString() != "abc" {
		t.Errorf("an old birth patch is not read: %v %v", e, ok)
	}
}

func TestRuntimeSetDropsTheOldName(t *testing.T) {
	s := form.FromMap(map[string]json.RawMessage{"system.studies": json.RawMessage(`["a"]`)})
	after := s.Apply(form.RuntimeSet(s, map[string]json.RawMessage{form.StudiesKey: json.RawMessage(`["b"]`)}))
	if after.Has("system.studies") {
		t.Error("old name kept")
	}
	if v, _ := after.Get(form.StudiesKey); string(v) != `["b"]` {
		t.Errorf("studies %s", v)
	}
}

func TestARetireRequestIsRecordedAtTheRuntimeName(t *testing.T) {
	set := form.Build(form.Snapshot{}, map[string]json.RawMessage{form.RetireRequestKey: json.RawMessage(`"manual"`), "mantra": json.RawMessage(`"x"`)}, nil)
	s := form.Snapshot{}.Apply(form.RecordRetireRequest(form.Snapshot{}, set))
	if s.Has(form.RetireRequestKey) {
		t.Error("the request key was kept")
	}
	if v, _ := s.Get(form.RetiredKey); string(v) != `"manual"` {
		t.Errorf("runtime.retired = %s", v)
	}
	if v, _ := s.Get("mantra"); string(v) != `"x"` {
		t.Errorf("the rest of the patch was lost: %s", v)
	}
	undo := form.Build(s, nil, []string{form.RetireRequestKey})
	s = s.Apply(form.RecordRetireRequest(s, undo))
	if s.Has(form.RetiredKey) || s.Has(form.RetireRequestKey) {
		t.Error("undo left a retirement behind")
	}
	legacy := form.FromMap(map[string]json.RawMessage{form.RetireRequestKey: json.RawMessage(`"context"`)})
	legacy = legacy.Apply(form.RecordRetireRequest(legacy, form.Build(legacy, nil, []string{form.RetireRequestKey})))
	if legacy.Has(form.RetireRequestKey) || legacy.Has(form.RetiredKey) {
		t.Error("undo on an unmigrated form left a retirement behind")
	}
}

func TestRuntimeKeysAreHiddenFromTheModel(t *testing.T) {
	for k, want := range map[string]bool{"runtime": true, "runtime.forked_from": true, "system.cwd": true, "mantra": false, "runtimes": false} {
		if form.HarnessKey(k) != want {
			t.Errorf("HarnessKey(%q) = %v", k, !want)
		}
	}
}
