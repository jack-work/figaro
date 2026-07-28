package render

import "strings"

// Glyphs the dark style's table rules are drawn with. They are pinned here
// rather than read back out of the style because the style is a parsed JSON
// blob and these three runes are the whole of what a consumer needs to find a
// table in already-rendered rows. TestTableSpans_GlyphsMatchGlamour renders a
// real table and fails loudly if glamour ever draws it with something else.
const (
	tableColumnRule = '│' // between columns, on every physical row of the table
	tableRowRule    = '─' // the run under the header
	tableCenterRule = '┼' // where the header rule crosses a column rule
)

// TableSpans locates the markdown tables inside rows already rendered by Prose,
// as half-open [start, end) index ranges. It exists so a consumer can treat a
// table as one unit — clamp it, fold it, count it — without re-parsing the
// markdown or knowing anything about glamour.
//
// A table is a maximal run of consecutive rows that all carry a rule glyph AND
// that contains at least one CENTER rule. Requiring the center rule is what
// makes the test specific rather than merely suggestive:
//
//   - a thematic break ("---" in markdown) renders as a run of row rules with
//     no crossing, so it is not a table;
//   - a blockquote — which is how thinking and steering nodes are drawn — puts
//     a COLUMN rule at the head of every one of its lines, so column rules
//     alone would have identified every thinking block as a table.
//
// The cost is that a single-column table (no column rule to cross, so no center
// rule) is not recognised. That is the right trade: a one-column markdown table
// is a rounding error, and the failure mode is "we leave it alone", not "we
// mangle a thinking block".
func TableSpans(rows []string) [][2]int {
	var spans [][2]int
	for i := 0; i < len(rows); {
		if !hasRule(rows[i]) {
			i++
			continue
		}
		j, center := i, false
		for ; j < len(rows) && hasRule(rows[j]); j++ {
			if strings.ContainsRune(rows[j], tableCenterRule) {
				center = true
			}
		}
		if center {
			spans = append(spans, [2]int{i, j})
		}
		i = j
	}
	return spans
}

func hasRule(row string) bool {
	return strings.ContainsRune(row, tableColumnRule) ||
		strings.ContainsRune(row, tableRowRule) ||
		strings.ContainsRune(row, tableCenterRule)
}
