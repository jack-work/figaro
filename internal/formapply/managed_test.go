package formapply

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func snapshot(t *testing.T, raw string) form.Snapshot {
	t.Helper()
	var s form.Snapshot
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestManagedLeavesPreserveOccupancyAndUnmanagedValues(t *testing.T) {
	current := snapshot(t, `{"charter":{"purpose":"old","note":"keep"},"target-aria":"holder","handoff":"keep.md","system":{"provider":"keep"},"comment_1":{"body":"keep"}}`)
	desired := snapshot(t, `{"charter":{"purpose":"new"},"target-aria":"wrong","system":{"provider":"wrong"}}`)
	managed := []string{"charter.purpose"}
	patch, err := Plan(current, desired, managed)
	if err != nil {
		t.Fatal(err)
	}
	got := current.Apply(patch)
	want := snapshot(t, `{"charter":{"purpose":"new","note":"keep"},"target-aria":"holder","handoff":"keep.md","system":{"provider":"keep"},"comment_1":{"body":"keep"}}`)
	if !got.Diff(want).IsIdentity() {
		t.Fatalf("wrong managed result: %s", got.Root().Raw())
	}
	repeated, err := Plan(got, desired, managed)
	if err != nil || !repeated.IsIdentity() {
		t.Fatalf("second application must be a no-op: %v, %v", repeated, err)
	}
	if !reflect.DeepEqual(managed, []string{"charter.purpose"}) {
		t.Fatal("planner mutated its caller's managed paths")
	}
}

func TestOnlyExplicitlyManagedMissingValuesAreRemoved(t *testing.T) {
	current := snapshot(t, `{"charter":{"purpose":"old","obsolete":"remove","note":"keep"}}`)
	desired := snapshot(t, `{"charter":{"purpose":"new"}}`)
	patch, err := Plan(current, desired, []string{"charter.purpose", "charter.obsolete", "charter.absent"})
	if err != nil {
		t.Fatal(err)
	}
	got := current.Apply(patch)
	want := snapshot(t, `{"charter":{"purpose":"new","note":"keep"}}`)
	if !got.Diff(want).IsIdentity() {
		t.Fatalf("unmanaged data changed: %s", got.Root().Raw())
	}
}

func TestManagedPathsCannotClaimRuntimeStateOrOverlap(t *testing.T) {
	for _, paths := range [][]string{
		nil, {""}, {"charter..purpose"}, {" charter"}, {"charter.*"}, {"charter[0]"},
		{"system"}, {"system.provider"}, {"target-aria"}, {"aria_id"}, {"handoff"},
		{"comment_1.body"}, {"thread_1"}, {"credentials"}, {"permissions"},
		{"charter", "charter.purpose"}, {"charter", "charter-extra", "charter.purpose"},
		{"charter.purpose", "charter.purpose"},
	} {
		t.Run(strings.Join(paths, ","), func(t *testing.T) {
			if p, err := Plan(form.Snapshot{}, form.Snapshot{}, paths); err == nil || !p.IsIdentity() {
				t.Fatalf("managed paths %q accepted: %v", paths, err)
			}
		})
	}
}

func TestManagedChildCannotReplaceAnUnmanagedScalarAncestor(t *testing.T) {
	for _, pair := range [][2]string{
		{`{"charter":"keep"}`, `{"charter":{"purpose":"new"}}`},
		{`{"charter":{"purpose":"keep"}}`, `{"charter":"wrong"}`},
		{`{"charter":null}`, `{}`},
		{`{"charter":[]}`, `{}`},
	} {
		if _, err := Plan(snapshot(t, pair[0]), snapshot(t, pair[1]), []string{"charter.purpose"}); err == nil {
			t.Fatalf("accepted a scalar/list ancestor: %v", pair)
		}
	}
}
