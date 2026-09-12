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

// plainLines strips the styling so a golden row can be compared verbatim.
func plainLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = stripANSI(l)
	}
	return out
}

func wantRows(t *testing.T, got []string, want ...string) {
	t.Helper()
	plain := plainLines(got)
	if len(plain) != len(want) {
		t.Fatalf("want %d rows, got %d: %q", len(want), len(plain), plain)
	}
	for i := range want {
		if plain[i] != want[i] {
			t.Fatalf("row %d:\n want %q\n  got %q", i, want[i], plain[i])
		}
	}
}

// The table: Δ, key, old -> new, one row per key. The board's rows draw
// first and unprefixed; every other form's key is dotted onto its id. The
// key column is padded to the widest key so the arrows line up.
func TestFormDeltaTableRows(t *testing.T) {
	deltas := map[string]livedoc.FormDelta{
		"@f1.status": fdPrev("@f1", livedoc.FormStudied, livedoc.FormSet, `"open"`, `"merged"`),
		"@f1.sha":    fd("@f1", livedoc.FormStudied, livedoc.FormSet, `"8b12f128"`),
		"a1.phase":   fdPrev("a1", livedoc.FormBound, livedoc.FormSet, `"seed"`, `"canary"`),
	}
	wantRows(t, formDeltaLines(deltas, 120, true),
		"  Δ phase       seed -> canary",
		"  Δ @f1.sha     ∅ -> 8b12f128",
		"  Δ @f1.status  open -> merged",
	)
}

// A removed key still has an old side; a dead form is not a key at all,
// so it takes a banner row of its own.
func TestFormDeltaRemovedVersusDeleted(t *testing.T) {
	wantRows(t, formDeltaLines(map[string]livedoc.FormDelta{
		"@f1.brief": fdPrev("@f1", livedoc.FormStudied, livedoc.FormRemoved, `"ship it"`, ""),
	}, 120, true), "  Δ @f1.brief  ship it -> ∅")

	wantRows(t, formDeltaLines(map[string]livedoc.FormDelta{
		"@f1": fd("@f1", livedoc.FormStudied, livedoc.FormDeleted, ""),
	}, 120, true), "  ⊘ @f1")
}

// A fork is a banner: the glyph and the parent id, no sentence, first in
// the table, and the keys the fork patch wrote do not draw beside it.
func TestForkRowIsGlyphAndParent(t *testing.T) {
	wantRows(t, formDeltaLines(map[string]livedoc.FormDelta{
		"a2.aria_id":            fdPrev("a2", livedoc.FormBound, livedoc.FormSet, `"a1"`, `"a2"`),
		"a2.system.forked_from": fd("a2", livedoc.FormBound, livedoc.FormSet, `"a1"`),
		"a2.mantra":             fd("a2", livedoc.FormBound, livedoc.FormSet, `"new work"`),
	}, 120, true),
		"  ⑂ a1",
		"  Δ mantra  ∅ -> new work",
	)
}

// A role recast is an ordinary row now: target-aria, old to new.
func TestRoleRecastIsARow(t *testing.T) {
	wantRows(t, formDeltaLines(map[string]livedoc.FormDelta{
		"@r1.target-aria": fdPrev("@r1", livedoc.FormRole, livedoc.FormSet, `"a3"`, `"a9"`),
	}, 120, true), "  Δ @r1.target-aria  a3 -> a9")
}

// A multi-line value is one row. A form value can be a whole credo.
func TestFormDeltaValuesAreSingleLined(t *testing.T) {
	lines := formDeltaLines(map[string]livedoc.FormDelta{
		"a1.credo": fd("a1", livedoc.FormBound, livedoc.FormSet, `"one\ntwo\n\nthree"`),
	}, 300, true)
	if len(lines) != 1 || !strings.Contains(stripANSI(lines[0]), "one two three") {
		t.Fatalf("a multi-line value must flatten to one row: %q", plainLines(lines))
	}
}

// Collapsed caps the table at a few rows and counts the rest; expanded
// shows every row, whole. deltasExpandable is the difference.
func TestFormDeltaExpansion(t *testing.T) {
	long := `"` + strings.Repeat("x", 100) + `"`
	deltas := map[string]livedoc.FormDelta{
		"@f1.blob": fd("@f1", livedoc.FormStudied, livedoc.FormSet, long),
		"@f1.k1":   fd("@f1", livedoc.FormStudied, livedoc.FormSet, `"a"`),
		"@f1.k2":   fd("@f1", livedoc.FormStudied, livedoc.FormSet, `"b"`),
		"@f1.k3":   fd("@f1", livedoc.FormStudied, livedoc.FormSet, `"c"`),
		"@f1.k4":   fd("@f1", livedoc.FormStudied, livedoc.FormSet, `"d"`),
	}
	collapsed := plainLines(formDeltaLines(deltas, 300, false))
	if len(collapsed) != formDeltaRowCap+1 {
		t.Fatalf("collapsed shows %d rows plus the overflow count: %q", formDeltaRowCap, collapsed)
	}
	if collapsed[len(collapsed)-1] != "  ⋯ +2" {
		t.Fatalf("the held-back rows are counted: %q", collapsed)
	}
	for _, l := range collapsed {
		if strings.Contains(l, strings.Repeat("x", 100)) {
			t.Fatalf("collapsed must cap the value: %q", l)
		}
	}
	expanded := plainLines(formDeltaLines(deltas, 300, true))
	if len(expanded) != 5 {
		t.Fatalf("expansion shows every row: %q", expanded)
	}
	if !strings.Contains(strings.Join(expanded, "\n"), strings.Repeat("x", 100)) {
		t.Fatalf("expansion shows the value whole: %q", expanded)
	}
	if !deltasExpandable(deltas, 300) {
		t.Fatal("held-back rows must make the table expandable")
	}
	small := map[string]livedoc.FormDelta{
		"@f1.k": fd("@f1", livedoc.FormStudied, livedoc.FormSet, `"v"`),
	}
	if deltasExpandable(small, 120) {
		t.Fatal("a table that fits collapsed has nothing to reveal; the gesture must stay inert")
	}
}
