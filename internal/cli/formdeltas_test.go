package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

func fd(form string, kind livedoc.FormKind, event livedoc.FormEvent, val string) livedoc.FormDelta {
	d := livedoc.FormDelta{Kind: kind, Event: event, Form: form}
	if val != "" {
		d.Value = json.RawMessage(val)
	}
	return d
}

// One row per key in the "Figaro saw:" voice, the board's rows first and
// unprefixed, a studied form's rows named by its id.
func TestFormDeltaLinesGroupByForm(t *testing.T) {
	deltas := map[string]livedoc.FormDelta{
		"@f1.status": fd("@f1", livedoc.FormStudied, livedoc.FormSet, `"merged"`),
		"@f1.sha":    fd("@f1", livedoc.FormStudied, livedoc.FormSet, `"8b12f128"`),
		"a1.phase":   fd("a1", livedoc.FormBound, livedoc.FormSet, `"canary"`),
	}
	lines := formDeltaLines(deltas, 120, false)
	if len(lines) != 3 {
		t.Fatalf("want one row per key, got %d: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], `Figaro saw: phase -> "canary"`) || strings.Contains(lines[0], "a1") {
		t.Fatalf("the board's row draws first, unprefixed: %q", lines[0])
	}
	if !strings.Contains(lines[1], `Figaro saw: @f1 sha ->`) || !strings.Contains(lines[2], `Figaro saw: @f1 status ->`) {
		t.Fatalf("the studied form's rows are named and sorted: %q", lines[1:])
	}
}

// A removed key says removed; a deleted form says deleted; they are not
// the same thing and must not read the same.
func TestFormDeltaLinesRemovedVersusDeleted(t *testing.T) {
	removed := formDeltaLines(map[string]livedoc.FormDelta{
		"@f1.brief": fd("@f1", livedoc.FormStudied, livedoc.FormRemoved, ""),
	}, 120, false)
	if len(removed) != 1 || !strings.Contains(removed[0], "@f1 brief removed") {
		t.Fatalf("a removed key draws as removed: %q", removed)
	}
	deleted := formDeltaLines(map[string]livedoc.FormDelta{
		"@f1": fd("@f1", livedoc.FormStudied, livedoc.FormDeleted, ""),
	}, 120, false)
	if len(deleted) != 1 || !strings.Contains(deleted[0], "@f1 deleted") {
		t.Fatalf("a dead form draws as deleted: %q", deleted)
	}
}

// The two sentences render as prose, and their raw material does not draw
// beside them.
func TestFormDeltaSentences(t *testing.T) {
	forked := formDeltaLines(map[string]livedoc.FormDelta{
		"a2.aria_id":            fd("a2", livedoc.FormBound, livedoc.FormSet, `"a2"`),
		"a2.system.forked_from": fd("a2", livedoc.FormBound, livedoc.FormSet, `"a1"`),
	}, 120, false)
	if len(forked) != 1 || !strings.Contains(forked[0], "forked from a1") {
		t.Fatalf("the fork reads as a sentence naming the parent: %q", forked)
	}
	recast := formDeltaLines(map[string]livedoc.FormDelta{
		"@r1.target-aria": fd("@r1", livedoc.FormRole, livedoc.FormSet, `"a9"`),
	}, 120, false)
	if len(recast) != 1 || !strings.Contains(recast[0], "role @r1 recast to figaro a9") {
		t.Fatalf("the recast reads as a sentence: %q", recast)
	}
}

// Collapsed caps each value; expanded shows it whole. Both are one row
// per key, and deltasExpandable is the difference between the two.
func TestFormDeltaExpansion(t *testing.T) {
	long := `"` + strings.Repeat("x", 100) + `"`
	deltas := map[string]livedoc.FormDelta{
		"@f1.blob": fd("@f1", livedoc.FormStudied, livedoc.FormSet, long),
		"@f1.tiny": fd("@f1", livedoc.FormStudied, livedoc.FormSet, `"y"`),
	}
	collapsed := formDeltaLines(deltas, 300, false)
	if len(collapsed) != 2 {
		t.Fatalf("collapsed is one row per key: %q", collapsed)
	}
	for _, l := range collapsed {
		if strings.Contains(l, strings.Repeat("x", 100)) {
			t.Fatalf("collapsed must cap the value: %q", l)
		}
	}
	expanded := formDeltaLines(deltas, 300, true)
	whole := false
	for _, l := range expanded {
		if strings.Contains(l, strings.Repeat("x", 100)) {
			whole = true
		}
	}
	if !whole {
		t.Fatalf("expansion shows the value whole: %q", expanded)
	}
	if !deltasExpandable(deltas, 300) {
		t.Fatal("a capped value must make the node expandable")
	}
	small := map[string]livedoc.FormDelta{
		"@f1.k": fd("@f1", livedoc.FormStudied, livedoc.FormSet, `"v"`),
	}
	if deltasExpandable(small, 120) {
		t.Fatal("a delta that fits collapsed has nothing to reveal; the gesture must stay inert")
	}
}
