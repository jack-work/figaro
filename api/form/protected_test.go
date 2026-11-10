package form_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func TestCheckGrantsNamesTheEntryAProviderWouldReadDifferently(t *testing.T) {
	with := func(v string) form.Snapshot {
		return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{form.GrantsKey: json.RawMessage(v)}, nil))
	}
	for _, bad := range []struct{ raw, names string }{
		{`[" a"]`, `" a"`}, {`["a "]`, `"a "`}, {`["a","\tb"]`, `"\tb"`}, {`["a",""]`, `""`},
		{`"a"`, "array of strings"}, {`[1]`, "array of strings"}, {`null`, "array of strings"},
	} {
		if err := form.CheckGrants(with(bad.raw)); err == nil || !strings.Contains(err.Error(), bad.names) {
			t.Errorf("%s: %v", bad.raw, err)
		}
	}
	for _, ok := range []string{`[]`, `["a","read:aria:*"]`} {
		if err := form.CheckGrants(with(ok)); err != nil {
			t.Errorf("control %s: %v", ok, err)
		}
	}
	if err := form.CheckGrants(form.Snapshot{}); err != nil {
		t.Errorf("control: unset: %v", err)
	}
}
