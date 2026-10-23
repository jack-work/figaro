package render

import (
	"strings"
	"testing"
)

func plainRows(md string, width int) []string {
	rows := Prose(md, width)
	for i, r := range rows {
		rows[i] = strings.TrimRight(StripEscapes(r), " ")
	}
	return rows
}

func expectRows(t *testing.T, md string, width int, want []string) {
	t.Helper()
	got := plainRows(md, width)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Prose(%q, %d):\n got:\n%s\nwant:\n%s", md, width, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// glamour wraps a list item as one run with its marker, so continuation rows fell
// back to the marker's column instead of hanging under the item's text.
func TestListItemsHangUnderTheirText(t *testing.T) {
	expectRows(t, "1. At daemon start it loads the cache, if present, so it can answer before any call.\n2. Two\n", 40, []string{
		"  1. At daemon start it loads the cache,",
		"     if present, so it can answer before",
		"     any call.",
		"  2. Two",
	})
	expectRows(t, "- by the daemon whenever it reads an aria's metrics\n    - nested item long enough to wrap under itself\n", 40, []string{
		"   • by the daemon whenever it reads an",
		"     aria's metrics",
		"      • nested item long enough to wrap",
		"        under itself",
	})
}

// glamour's list block re-wraps with breakpoints " ,.;-+|", which split a file
// name or URL after its dot.
func TestListItemTextBreaksOnlyAtSpaces(t *testing.T) {
	for _, row := range plainRows("1. At daemon start it loads `$FIGARO_CACHE_DIR/models.json`, if present, so it can answer before any network call.\n", 62) {
		if strings.HasSuffix(row, "models.") {
			t.Errorf("a list row broke inside a file name: %q", row)
		}
	}
}

// glamour renders nothing for a paragraph inside a list item, so the two
// paragraphs of a loose item ran together with no space between them.
func TestLooseItemKeepsItsParagraphsApart(t *testing.T) {
	expectRows(t, "- first paragraph\n\n  second paragraph\n- next\n", 40, []string{
		"   • first paragraph",
		"",
		"     second paragraph",
		"   • next",
	})
}

func TestListMarkers(t *testing.T) {
	expectRows(t, "9. nine\n10. ten\n", 40, []string{
		"  9. nine",
		" 10. ten",
	})
	expectRows(t, "3) three\n", 40, []string{"  3) three"})
	expectRows(t, "- [ ] open\n- [x] done\n", 40, []string{
		" [ ] open",
		" [x] done",
	})
}

func TestListSitsBetweenParagraphsWithOneGap(t *testing.T) {
	expectRows(t, "Before.\n\n- a\n- b\n\nAfter.\n", 40, []string{
		"  Before.",
		"",
		"   • a",
		"   • b",
		"",
		"  After.",
	})
	rows := plainRows("> - quoted item\n\nAfter.\n", 40)
	if tail := strings.Join(rows[len(rows)-3:], "\n"); tail != "  │  • quoted item\n\n  After." {
		t.Errorf("a list closing a blockquote leaves a gap row inside the quote:\n%s", strings.Join(rows, "\n"))
	}
}

func TestLayoutMarksNeverReachTheScreen(t *testing.T) {
	md := "Text with \u2060 and \u2063 inside.\n\n- item \u2060\n\n  para\n\n| a | b |\n|---|---|\n| 1 | 2 |\n"
	for _, row := range Prose(md, 40) {
		if strings.ContainsAny(row, itemMark+paragraphMark) {
			t.Errorf("a layout mark reached the row: %q", row)
		}
	}
}

// glamour counts one column for a blockquote's indent and draws two, so a row
// filled to the width overflowed by one and its last word fell onto a row of
// its own, outside the quote.
func TestBlockquoteRowsKeepTheirBarAndTheirWidth(t *testing.T) {
	md := "> okay can you please give one final readthrough of my report having just seen my repo as a smoke test to make sure i didnt miss anything at all\n>\n> > a nested quote long enough to wrap around the width more than once, surely\n\nafter"
	for w := 30; w <= 120; w++ {
		rows := plainRows(md, w)
		for i, row := range rows {
			if n := len([]rune(row)); n > w {
				t.Errorf("width %d: row %q is %d cells", w, row, n)
			}
			if i < len(rows)-2 && !strings.HasPrefix(row, "  │") {
				t.Errorf("width %d: row %d %q fell out of the quote", w, i, row)
			}
		}
	}
}
