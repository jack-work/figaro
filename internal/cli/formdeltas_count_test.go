package cli

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// deltaRowCount is the cheap answer to a question deltaRows used to be asked
// for its length. The two must agree on every shape, or the collapsed glyph
// and the selection's refs disagree with the rows a reader opens.
func TestDeltaRowCountAgreesWithDeltaRows(t *testing.T) {
	set := func(form string, kind livedoc.FormKind, event livedoc.FormEvent, key, val string) (string, livedoc.FormDelta) {
		d := livedoc.FormDelta{Form: form, Kind: kind, Event: event}
		if val != "" {
			d.Value = json.RawMessage(`"` + val + `"`)
		}
		k := key
		if key != "" {
			k = form + "." + key
		} else {
			k = form
		}
		return k, d
	}
	join := func(pairs ...map[string]livedoc.FormDelta) map[string]livedoc.FormDelta {
		out := map[string]livedoc.FormDelta{}
		for _, p := range pairs {
			for k, v := range p {
				out[k] = v
			}
		}
		return out
	}
	one := func(form string, kind livedoc.FormKind, event livedoc.FormEvent, key, val string) map[string]livedoc.FormDelta {
		k, d := set(form, kind, event, key, val)
		return map[string]livedoc.FormDelta{k: d}
	}

	bound := func(key, val string) map[string]livedoc.FormDelta {
		return one("a1", livedoc.FormBound, livedoc.FormSet, key, val)
	}
	unbound := func(key, val string) map[string]livedoc.FormDelta {
		return one("@f2", livedoc.FormStudied, livedoc.FormSet, key, val)
	}

	cases := []struct {
		name   string
		deltas map[string]livedoc.FormDelta
	}{
		{"empty", nil},
		{"one key", bound("mantra", "sing")},
		{"two forms", join(bound("mantra", "sing"), unbound("role", "scribe"))},
		{"a fork and nothing else", join(
			bound(forkedFromKey, "abcd1234"), bound("aria_id", "beef5678"))},
		{"a fork plus state", join(
			bound(forkedFromKey, "abcd1234"), bound("aria_id", "beef5678"), bound("mantra", "sing"))},
		{"a dead form", join(
			bound("mantra", "sing"), one("@f2", livedoc.FormStudied, livedoc.FormDeleted, "", ""))},
		{"a removed key", join(
			bound("mantra", "sing"), one("a1", livedoc.FormBound, livedoc.FormRemoved, "mode", ""))},
	}
	for _, tc := range cases {
		for _, lift := range []bool{false, true} {
			want := len(deltaRows(tc.deltas, lift))
			got := adornRowCount(tc.deltas, lift)
			if got != want {
				t.Errorf("%s (lift=%v): counted %d rows, deltaRows draws %d", tc.name, lift, got, want)
			}
			if any := anyDeltaRow(tc.deltas, lift, lift && forkParentOf(tc.deltas) != ""); any != (want > 0) {
				t.Errorf("%s (lift=%v): anyDeltaRow says %v, deltaRows draws %d", tc.name, lift, any, want)
			}
		}
	}
}
