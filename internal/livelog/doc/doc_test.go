package doc

import "testing"

func TestDiffApply_RoundTrip(t *testing.T) {
	cases := []struct{ a, b string }{
		{"", "hello"},
		{"hello", "hello world"},   // append
		{"hello world", "hello"},   // truncate
		{"abcdef", "abXYef"},       // middle replace
		{"café", "café au lait"},   // multibyte append
		{"日本語テスト", "日本語のテスト"}, // multibyte middle insert
		{"same", "same"},           // no change
	}
	for _, c := range cases {
		d, ok := Diff(c.a, c.b)
		if c.a == c.b {
			if ok {
				t.Errorf("Diff(%q,%q): expected no-change", c.a, c.b)
			}
			continue
		}
		if !ok {
			t.Errorf("Diff(%q,%q): expected a change", c.a, c.b)
		}
		if got := Apply(c.a, d); got != c.b {
			t.Errorf("Apply(Diff(%q,%q)) = %q, want %q", c.a, c.b, got, c.b)
		}
	}
}

func TestApply_ClampsOutOfRange(t *testing.T) {
	if got := Apply("abc", Delta{At: 99, Del: 99, Ins: "X"}); got != "abcX" {
		t.Errorf("out-of-range apply = %q, want %q", got, "abcX")
	}
}

func patchBody(d *Doc, id, old, new string) {
	delta, _ := Diff(old, new)
	d.Apply(Patch(id, delta))
}

func TestDoc_AppendPatchStatusSeal(t *testing.T) {
	d := New()
	d.Apply(Append(Block{ID: "a", Kind: "text", Status: StatusActive}))
	d.Apply(Append(Block{ID: "b", Kind: "tool", Status: StatusActive}))
	patchBody(d, "a", "", "hello")
	patchBody(d, "a", "hello", "hello!")
	d.Apply(SetStatus("b", StatusOK))

	bs := d.Blocks()
	if len(bs) != 2 {
		t.Fatalf("len=%d want 2", len(bs))
	}
	if bs[0].Body != "hello!" {
		t.Errorf("block a body=%q want hello!", bs[0].Body)
	}
	if bs[1].Status != StatusOK {
		t.Errorf("block b status=%q want ok", bs[1].Status)
	}

	// After sealing, the tail is immutable: patches/status to sealed blocks no-op.
	d.Apply(Seal())
	patchBody(d, "a", "hello!", "MUTATED")
	d.Apply(SetStatus("a", StatusError))
	bs = d.Blocks()
	if bs[0].Body != "hello!" || bs[0].Status != StatusActive {
		t.Errorf("sealed block mutated: %+v", bs[0])
	}
	if d.Sealed() != 2 {
		t.Errorf("sealed=%d want 2", d.Sealed())
	}
}

func TestDoc_UnknownIDIgnored(t *testing.T) {
	d := New()
	d.Apply(SetStatus("missing", StatusOK)) // must not panic
	patchBody(d, "missing", "", "x")        // must not panic
	if d.Len() != 0 {
		t.Errorf("len=%d want 0", d.Len())
	}
}

// Replaying the same event stream must yield an identical block list — the
// property catch-up relies on.
func TestDoc_ReplayDeterministic(t *testing.T) {
	events := []Event{
		Append(Block{ID: "a", Kind: "text"}),
		Patch("a", mustDelta("", "one ")),
		Append(Block{ID: "b", Kind: "tool"}),
		Patch("a", mustDelta("one ", "one two")),
		SetStatus("b", StatusOK),
		Seal(),
	}
	d1, d2 := New(), New()
	for _, e := range events {
		d1.Apply(e)
	}
	for _, e := range events {
		d2.Apply(e)
	}
	a, b := d1.Blocks(), d2.Blocks()
	if len(a) != len(b) {
		t.Fatalf("len mismatch %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Body != b[i].Body || a[i].Status != b[i].Status {
			t.Errorf("block %d mismatch: %+v vs %+v", i, a[i], b[i])
		}
	}
}

func mustDelta(a, b string) Delta {
	d, _ := Diff(a, b)
	return d
}
