package livedoc

import (
	"testing"
	"unicode/utf8"
)

func TestApply(t *testing.T) {
	cases := []struct {
		name      string
		blob      string
		d         Delta
		want      string
	}{
		{"append", "abc", Delta{At: 3, Del: 0, Ins: "de"}, "abcde"},
		{"prepend", "abc", Delta{At: 0, Del: 0, Ins: "X"}, "Xabc"},
		{"middle replace", "abcd", Delta{At: 1, Del: 2, Ins: "ZZ"}, "aZZd"},
		{"delete tail", "abcd", Delta{At: 2, Del: 2, Ins: ""}, "ab"},
		{"full replace", "abc", Delta{At: 0, Del: 3, Ins: "xyz"}, "xyz"},
		{"empty into empty", "", Delta{At: 0, Del: 0, Ins: "hi"}, "hi"},
		{"clamp over-At", "ab", Delta{At: 99, Del: 0, Ins: "z"}, "abz"},
		{"clamp over-Del", "ab", Delta{At: 1, Del: 99, Ins: "z"}, "az"},
		{"clamp neg-At", "ab", Delta{At: -5, Del: 0, Ins: "z"}, "zab"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Apply(tc.blob, tc.d); got != tc.want {
				t.Fatalf("Apply(%q,%+v) = %q, want %q", tc.blob, tc.d, got, tc.want)
			}
		})
	}
}

func TestDiff_RoundTrip(t *testing.T) {
	cases := []struct{ name, old, new string }{
		{"equal", "same", "same"},
		{"append", "hello", "hello, world"},
		{"prepend", "world", "hello world"},
		{"middle", "the quick fox", "the slow fox"},
		{"delete middle", "abcXYZdef", "abcdef"},
		{"to empty", "abc", ""},
		{"from empty", "", "abc"},
		{"spinner frame flip", "## build\n⠋ running", "## build\n⠙ running"},
		{"braille to check", "⠋ running", "✓ done"},
		{"emoji edit", "status: 🔴 down", "status: 🟢 up"},
		{"multibyte tail", "café", "cafés"},
		{"combining", "é acute", "é grave"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := Diff(tc.old, tc.new)
			if tc.old == tc.new {
				if ok {
					t.Fatalf("Diff of equal strings returned ok=true (%+v)", d)
				}
				return
			}
			if !ok {
				t.Fatal("Diff of differing strings returned ok=false")
			}
			if got := Apply(tc.old, d); got != tc.new {
				t.Fatalf("Apply(old, Diff(old,new)) = %q, want %q (delta %+v)", got, tc.new, d)
			}
			assertRuneAligned(t, tc.old, tc.new, d)
		})
	}
}

func TestDiff_SingleRegionMinimal(t *testing.T) {
	// A one-glyph spinner flip in a large doc must produce a tiny delta,
	// not a whole-doc resend — the core compression property.
	prefix := "# Deploy\n\n## build\n"
	suffix := " building\n\n## test\nqueued\n\n## deploy\nqueued\n"
	old := prefix + "⠋" + suffix
	new := prefix + "⠙" + suffix
	d, ok := Diff(old, new)
	if !ok {
		t.Fatal("expected a delta")
	}
	if d.Del != len("⠋") || d.Ins != "⠙" || d.At != len(prefix) {
		t.Fatalf("expected minimal single-glyph splice, got %+v", d)
	}
}

// assertRuneAligned checks the delta never splits a glyph and keeps new
// valid UTF-8.
func assertRuneAligned(t *testing.T, old, new string, d Delta) {
	t.Helper()
	if d.At < 0 || d.At > len(old) || d.At+d.Del > len(old) {
		t.Fatalf("delta out of range: %+v (old len %d)", d, len(old))
	}
	if d.At < len(old) && !utf8.RuneStart(old[d.At]) {
		t.Fatalf("At=%d splits a rune in old", d.At)
	}
	if end := d.At + d.Del; end < len(old) && !utf8.RuneStart(old[end]) {
		t.Fatalf("At+Del=%d splits a rune in old", end)
	}
	if !utf8.ValidString(d.Ins) {
		t.Fatalf("Ins is not valid UTF-8: %q", d.Ins)
	}
	if !utf8.ValidString(new) {
		t.Fatalf("new is not valid UTF-8")
	}
}

// FuzzDiffApply: for any pair of valid-UTF-8 strings, Diff then Apply
// round-trips, the delta is rune-aligned, and the result stays valid
// UTF-8. Seeded with glyph-heavy cases.
func FuzzDiffApply(f *testing.F) {
	seeds := [][2]string{
		{"", ""},
		{"abc", "abc"},
		{"hello", "hello world"},
		{"⠋ running", "✓ done"},
		{"café", "cafés"},
		{"# h\n\n```bash\nline1\n```", "# h\n\n```bash\nline1\nline2\n```"},
		{"status: 🔴", "status: 🟢"},
	}
	for _, s := range seeds {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, old, new string) {
		if !utf8.ValidString(old) || !utf8.ValidString(new) {
			t.Skip() // protocol only ever carries valid UTF-8 blobs
		}
		d, ok := Diff(old, new)
		if !ok {
			if old != new {
				t.Fatalf("ok=false but strings differ")
			}
			return
		}
		if got := Apply(old, d); got != new {
			t.Fatalf("round-trip failed: Apply(old,Diff)=%q want %q (delta %+v)", got, new, d)
		}
		assertRuneAligned(t, old, new, d)
	})
}
