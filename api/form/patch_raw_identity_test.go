package form_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jack-work/figaro/api/form"
)

func scalar(before, after string) form.Patch {
	return form.Patch{Scalar: &form.ScalarPatch{
		Before: form.NewValue(json.RawMessage(before)),
		After:  form.NewValue(json.RawMessage(after)),
	}}
}

func TestIsRawIdentity(t *testing.T) {
	cases := []struct {
		name          string
		p             form.Patch
		raw, semantic bool
	}{
		{"zero patch", form.Patch{}, true, true},
		{"same bytes", scalar(`"a"`, `"a"`), true, true},
		{"changed scalar", scalar(`"a"`, `"b"`), false, false},
		{"key order only", scalar(`{"a":1,"b":2}`, `{"b":2,"a":1}`), false, true},
		{"object update of same bytes", form.Patch{Object: &form.ObjectPatch{
			Update: map[string]form.Patch{"k": scalar(`1`, `1`)}}}, true, true},
		{"object update that changes", form.Patch{Object: &form.ObjectPatch{
			Update: map[string]form.Patch{"k": scalar(`1`, `2`)}}}, false, false},
		{"object set", form.Patch{Object: &form.ObjectPatch{
			Set: map[string]form.Value{"k": form.NewValue(json.RawMessage(`1`))}}}, false, false},
		{"list update of same bytes", form.Patch{List: &form.ListPatch{
			Update: map[string]form.Patch{"0": scalar(`"x"`, `"x"`)}}}, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.raw, c.p.IsRawIdentity())
			assert.Equal(t, c.semantic, c.p.IsIdentity())
			// The one-way guarantee replay relies on: raw identity implies identity.
			if c.p.IsRawIdentity() {
				assert.True(t, c.p.IsIdentity())
			}
		})
	}
}
