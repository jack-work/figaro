package form_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/stretchr/testify/require"
)

// Defends a law against the delta limits a figaro sets on itself: cut it
// and system.delta_key_bytes = 1 hides every law the figaro is under.
func TestALawRendersWholeWhateverTheKeyLimit(t *testing.T) {
	tmpls, err := form.LoadDefaultTemplates()
	require.NoError(t, err)
	law := strings.Repeat("Never write an em dash. ", 20)
	prev := form.FromMap(map[string]json.RawMessage{form.DeltaKeyBytesKey: raw(t, 16)})
	next := prev.Apply(form.Build(prev, map[string]json.RawMessage{"law.style": raw(t, law), "note": raw(t, law)}, nil))
	rendered, err := form.Render(next.Diff(prev), prev, tmpls)
	require.NoError(t, err)
	got := map[string]string{}
	for _, r := range rendered {
		got[r.Key] = r.Body
	}
	require.Equal(t, law, got["law.style"])
	require.Less(t, len(got["note"]), 32, "an ordinary key is still cut")
}

// Defends the repeal notice: a removed key renders nothing, so without it a
// figaro keeps obeying a law the owner took back.
func TestALawsRepealIsSaidAndOtherRemovalsStaySilent(t *testing.T) {
	tmpls, err := form.LoadDefaultTemplates()
	require.NoError(t, err)
	prev := form.FromMap(map[string]json.RawMessage{"law.style": raw(t, "x"), "law.spend": raw(t, "y"), "note": raw(t, "z")})
	for _, tc := range []struct {
		remove []string
		want   map[string]string
	}{
		{[]string{"law.style", "note"}, map[string]string{"law.style": form.LawRepealed}},
		{[]string{"law"}, map[string]string{"law.style": form.LawRepealed, "law.spend": form.LawRepealed}},
	} {
		next := prev.Apply(form.Build(prev, nil, tc.remove))
		rendered, err := form.Render(next.Diff(prev), prev, tmpls)
		require.NoError(t, err)
		got := map[string]string{}
		for _, r := range rendered {
			got[r.Key] = r.Body
		}
		require.Equal(t, tc.want, got, "removing %v", tc.remove)
	}
}
