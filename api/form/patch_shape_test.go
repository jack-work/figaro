package form

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAPatchThatIsNotAPatchIsRefusedNamingTheShape(t *testing.T) {
	for name, raw := range map[string]string{
		"a bare key/value dict":         `{"mantra":"gateway check"}`,
		"a bare dict under Object":      `{"Object":{"mantra":"x"}}`,
		"a bare dict under an Update":   `{"Object":{"Update":{"mantra":{"x":1}}}}`,
		"a bare dict under a Scalar":    `{"Scalar":{"value":"x"}}`,
		"an unknown field in a List op": `{"List":{"Append":[]}}`,
		"not an object":                 `"mantra"`,
	} {
		err := CheckPatchJSON(json.RawMessage(raw))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), `{"Object":{"Set":`) {
			t.Errorf("%s: error does not name the expected shape: %v", name, err)
		}
	}
}

func TestEveryPatchShapeTheDaemonWritesPassesTheCheck(t *testing.T) {
	before := Snapshot{}.Apply(Build(Snapshot{}, map[string]json.RawMessage{"a": json.RawMessage(`1`), "b": json.RawMessage(`{"c":"x"}`)}, nil))
	after := before.Apply(Build(before, map[string]json.RawMessage{"b.c": json.RawMessage(`"y"`), "d": json.RawMessage(`[1]`)}, []string{"a"}))
	for name, p := range map[string]Patch{
		"a diff":  after.Diff(before),
		"a build": Build(Snapshot{}, map[string]json.RawMessage{"mantra": json.RawMessage(`"m"`)}, nil),
	} {
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckPatchJSON(raw); err != nil {
			t.Errorf("%s: %v (%s)", name, err, raw)
		}
	}
	for _, ok := range []string{``, `null`, `{}`, `{"object":{"set":{"a":1}}}`} {
		if err := CheckPatchJSON(json.RawMessage(ok)); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}
