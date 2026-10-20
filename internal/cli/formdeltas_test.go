package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

func fd(form string, kind livedoc.FormKind, event livedoc.FormEvent, val string) livedoc.FormDelta {
	d := livedoc.FormDelta{Kind: kind, Event: event, Form: form}
	if val != "" {
		d.Value = json.RawMessage(val)
	}
	return d
}

func fdPrev(form string, kind livedoc.FormKind, event livedoc.FormEvent, prev, val string) livedoc.FormDelta {
	d := fd(form, kind, event, val)
	if prev != "" {
		d.Prev = json.RawMessage(prev)
	}
	return d
}

func rowTexts(deltas map[string]livedoc.FormDelta, width int) []string {
	rows := deltaRows(deltas, false)
	keyw := metaKeyWidth(deltaEntries(deltas, false))
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, deltaRowText(r, keyw, width))
	}
	return out
}

func wantTexts(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("want %d rows, got %d: %q", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d:\n want %q\n  got %q", i, want[i], got[i])
		}
	}
}

// The board's rows draw first and unprefixed; every other form's key is dotted
// onto its id.
func TestFormDeltaRows(t *testing.T) {
	deltas := map[string]livedoc.FormDelta{
		"@f1.status": fdPrev("@f1", livedoc.FormStudied, livedoc.FormSet, `"open"`, `"merged"`),
		"@f1.sha":    fd("@f1", livedoc.FormStudied, livedoc.FormSet, `"8b12f128"`),
		"a1.phase":   fdPrev("a1", livedoc.FormBound, livedoc.FormSet, `"seed"`, `"canary"`),
	}
	wantTexts(t, rowTexts(deltas, 120),
		"phase       seed -> canary",
		"@f1.sha     ∅ -> 8b12f128",
		"@f1.status  open -> merged",
	)
}

// A removed key still has an old side; a dead form takes a banner row.
func TestFormDeltaRemovedVersusDeleted(t *testing.T) {
	wantTexts(t, rowTexts(map[string]livedoc.FormDelta{
		"@f1.brief": fdPrev("@f1", livedoc.FormStudied, livedoc.FormRemoved, `"ship it"`, ""),
	}, 120), "@f1.brief  ship it -> ∅")

	wantTexts(t, rowTexts(map[string]livedoc.FormDelta{
		"@f1": fd("@f1", livedoc.FormStudied, livedoc.FormDeleted, ""),
	}, 120), "⊘ @f1")
}

func TestRoleRecastIsARow(t *testing.T) {
	wantTexts(t, rowTexts(map[string]livedoc.FormDelta{
		"@r1.target-aria": fdPrev("@r1", livedoc.FormRole, livedoc.FormSet, `"a3"`, `"a9"`),
	}, 120), "@r1.target-aria  a3 -> a9")
}

func TestFormDeltaValuesAreSingleLined(t *testing.T) {
	rows := rowTexts(map[string]livedoc.FormDelta{
		"a1.credo": fd("a1", livedoc.FormBound, livedoc.FormSet, `"one\ntwo\n\nthree"`),
	}, 300)
	if len(rows) != 1 || !strings.HasSuffix(rows[0], "∅ -> one…") {
		t.Fatalf("a multi-line value is its first line and an ellipsis: %q", rows)
	}
}

// A form value is untrusted text: no control sequence may reach a row or the yank.
func TestFormDeltaStripsControlSequences(t *testing.T) {
	deltas := map[string]livedoc.FormDelta{
		"a1.k":            fd("a1", livedoc.FormBound, livedoc.FormSet, `"safe\u001b[2Jhidden"`),
		"a1.x\u001b[31my": fd("a1", livedoc.FormBound, livedoc.FormSet, `"v"`),
	}
	rows := deltaRows(deltas, false)
	var lines []string
	for _, r := range rows {
		keyw := metaKeyWidth(deltaEntries(deltas, false))
		lines = append(lines, deltaRowFull(r), deltaRowText(r, keyw, 40), deltaRowText(r, keyw, 300))
	}
	for _, l := range lines {
		for _, r := range l {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				t.Fatalf("control %q reached the row: %q", r, l)
			}
		}
	}
}

func TestFormDeltaEmptyStringIsNotAbsent(t *testing.T) {
	wantTexts(t, rowTexts(map[string]livedoc.FormDelta{
		"a1.k": fdPrev("a1", livedoc.FormBound, livedoc.FormSet, `""`, `"v"`),
	}, 120), `k  "" -> v`)
}

// A narrow pane elides the transition rather than wrapping it, and the new
// value survives.
func TestFormDeltaRowFitsItsWidth(t *testing.T) {
	deltas := map[string]livedoc.FormDelta{
		"a1.mantra": fdPrev("a1", livedoc.FormBound, livedoc.FormSet,
			`"`+strings.Repeat("o", 60)+`"`, `"`+strings.Repeat("n", 40)+`"`),
	}
	for _, w := range []int{20, 34, 48, 72, 200} {
		row := rowTexts(deltas, w)[0]
		if got := len([]rune(row)); got > w {
			t.Fatalf("width %d: row is %d columns: %q", w, got, row)
		}
		if w <= 72 && !strings.Contains(row, "…") {
			t.Fatalf("width %d: a transition too long for the row must say so: %q", w, row)
		}
	}
	if full := deltaRowFull(deltaRows(deltas, false)[0]); !strings.Contains(full, strings.Repeat("o", 60)) {
		t.Fatalf("the row's own text is never elided: %q", full)
	}
}

// The copy and the hash of a delta row are taken from the whole row, not the
// clipped one.
func TestDeltaRowIdentityIsNotClipped(t *testing.T) {
	long := func(tail string) livedoc.FormDelta {
		return fd("a1", livedoc.FormBound, livedoc.FormSet, `"`+strings.Repeat("x", 240)+tail+`"`)
	}
	a := deltaEntries(map[string]livedoc.FormDelta{"a1.brief": long("A")}, false)[0]
	b := deltaEntries(map[string]livedoc.FormDelta{"a1.brief": long("B")}, false)[0]
	if a.full() == b.full() {
		t.Fatalf("two rows that differ at column 250 render identically:\n%s", a.full())
	}
	if a.hash() == b.hash() {
		t.Fatal("two different rows hash the same")
	}
}
