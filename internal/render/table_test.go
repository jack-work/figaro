package render

import (
	"strings"
	"testing"
)

// TestTableSpans_GlyphsMatchGlamour is the tripwire under the pinned rule
// glyphs: it renders a real table and asserts TableSpans finds exactly it. If a
// glamour upgrade ever changes the border set, this fails here rather than
// silently turning every table-aware consumer into a no-op.
func TestTableSpans_GlyphsMatchGlamour(t *testing.T) {
	md := "before the table\n\n| state | meaning |\n|---|---|\n| dormant | nothing running |\n| active | a turn is in flight |\n\nafter the table\n"
	rows := Prose(md, 60)
	spans := TableSpans(rows)
	if len(spans) != 1 {
		t.Fatalf("want exactly 1 table span, got %d\n%s", len(spans), indentRows(rows))
	}
	first, last := spans[0][0], spans[0][1]
	// The span must cover the header, the rule and both data rows, and nothing
	// outside it may be table-ish.
	body := stripANSI(strings.Join(rows[first:last], "\n"))
	for _, want := range []string{"state", "meaning", "dormant", "active"} {
		if !strings.Contains(body, want) {
			t.Errorf("span misses %q\nspan:\n%s", want, indentRows(rows[first:last]))
		}
	}
	outside := stripANSI(strings.Join(append(append([]string{}, rows[:first]...), rows[last:]...), "\n"))
	for _, unwanted := range []string{"dormant", "active"} {
		if strings.Contains(outside, unwanted) {
			t.Errorf("span too narrow: %q left outside", unwanted)
		}
	}
	if !strings.Contains(outside, "before the table") || !strings.Contains(outside, "after the table") {
		t.Errorf("span too wide: it swallowed surrounding prose\n%s", indentRows(rows))
	}
}

// TestTableSpans_IgnoresBlockquotesAndRules is the reason the center rule is
// required. A thinking node is drawn as a blockquote, and glamour gives every
// blockquote line a leading COLUMN rule — the same glyph a table draws between
// its columns. Matching on column rules alone would have declared every
// thinking block a table.
func TestTableSpans_IgnoresBlockquotesAndRules(t *testing.T) {
	for _, tc := range []struct{ name, md string }{
		{"blockquote", blockquoteMD("weighing the options, at some length, so that it wraps more than once")},
		{"thematic break", "above\n\n---\n\nbelow\n"},
		{"code fence", "```go\nfunc main() {}\n```\n"},
	} {
		rows := Prose(tc.md, 40)
		if spans := TableSpans(rows); len(spans) != 0 {
			t.Errorf("%s: want no table spans, got %v\n%s", tc.name, spans, indentRows(rows))
		}
	}
}

func TestTableSpans_TwoTables(t *testing.T) {
	one := "| a | b |\n|---|---|\n| 1 | 2 |\n"
	md := one + "\nsome prose between them\n\n" + one
	rows := Prose(md, 50)
	if spans := TableSpans(rows); len(spans) != 2 {
		t.Fatalf("want 2 spans, got %d (%v)\n%s", len(spans), spans, indentRows(rows))
	}
}

func blockquoteMD(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(s, "\n") {
		b.WriteString("> " + l + "\n")
	}
	return b.String()
}
