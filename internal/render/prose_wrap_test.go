package render

import (
	"strings"
	"testing"
)

// The premature-break guard.
//
// THE BUG IT HOLDS THE DOOR ON. glamour wraps a paragraph TWICE: once in
// ParagraphElement.Finish, once again over the whole document block in
// BlockElement.Finish. Through glamour v1.0.0 the first pass was
// muesli/reflow's wordwrap, whose Write() writes a BREAKPOINT rune ('-', its
// only default) straight into the buffer without adding it to lineLen — so a
// line came back ONE CELL TOO WIDE PER HYPHEN it contained. The second pass
// (x/ansi.Wordwrap, breakpoints " ,.;-+|") then re-wrapped that one line at
// the same limit and pushed its last word down alone. It never re-joined the
// following line, so the tail of the paragraph came out as an orphan:
//
//	Three hundred and forty-nine workshops. In one town. Making
//	one
//	thing.
//
// and, when the overhang landed on one of the second pass's own breakpoints,
// as a lone comma or a lone hyphen on its own row. Every reported case carried
// a hyphen earlier in the line — forty-nine, nineteenth-century, mid-1850s,
// 1.5-second — which is what made it look like a terminal-width bug for three
// rounds: it depends on the TEXT, not on the width.
//
// Measured at HEAD~ over these four paragraphs and widths 30..100: 38
// premature breaks. Under glamour v2 (lipgloss.Wrap in the first pass): 0.
var wrapCorpus = []string{
	"Three hundred and forty-nine workshops. In one town. Making one thing.",
	"The name \"catlinite,\" note, honors George Catlin, the nineteenth-century American painter who documented Plains peoples — which is to say the stone that Indigenous nations had been quarrying for two millennia is known to geology by the surname of a white man who saw it in the 1830s.",
	"pipe factory there in 1825 and is said to have begun carving native bruyère around 1840; the trade's own historians settle on the mid-1850s — 1854 or 1856 — as the real inflection point, and are frank that the name of the man who cut the first briar pipe is simply gone.",
	"I need to respect the rate limit of one request per second, so I'll batch the queries together in a single bash command with 1.5-second delays between each request to stay safely under the limit.",
}

// prematureBreak re-wraps a paragraph's own ink greedily at the widest row the
// renderer itself produced, and reports the surplus rows.
//
// The oracle is SELF-CALIBRATING on purpose. Comparing against the width we
// asked for cannot work: glamour spends part of that width on its document
// margin, so a correct row is always a few cells short of it and a naive
// "did this row have room for the next word" test fires on every line. Taking
// the budget from the block's own widest row asks the only question that is
// actually a defect: given the width THIS BLOCK chose, did it break earlier
// than it had to?
func prematureBreak(rows []string) (int, string) {
	var ink []string
	budget := 0
	for _, r := range rows {
		t := strings.TrimSpace(stripEscapesForWidth(r))
		if t == "" {
			continue
		}
		ink = append(ink, t)
		if n := cells(t); n > budget {
			budget = n
		}
	}
	if len(ink) == 0 {
		return 0, ""
	}
	var lines []string
	cur := ""
	for _, w := range strings.Fields(strings.Join(ink, " ")) {
		switch {
		case cur == "":
			cur = w
		case cells(cur)+1+cells(w) <= budget:
			cur += " " + w
		default:
			lines = append(lines, cur)
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) < len(ink) {
		return len(ink) - len(lines), strings.Join(ink, " | ")
	}
	return 0, ""
}

func stripEscapesForWidth(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = skipEscape(s, i)
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func TestProse_NoPrematureBreak(t *testing.T) {
	for w := 30; w <= 100; w++ {
		for i, md := range wrapCorpus {
			rows := Prose(md, w)
			if n, detail := prematureBreak(rows); n > 0 {
				t.Errorf("width %d, paragraph %d: %d row(s) broken early:\n  %s", w, i, n, detail)
			}
			for _, r := range rows {
				if n := cells(strings.TrimRight(stripEscapesForWidth(r), " ")); n > w {
					t.Errorf("width %d, paragraph %d: row of %d cells: %q", w, i, n, stripEscapesForWidth(r))
				}
			}
		}
	}
}

// TestProse_HyphenDoesNotWidenTheLine is the mechanism, asserted directly.
//
// The two texts are the SAME LENGTH — 'x' against '-' in the same slots — so
// any difference in how they wrap is the hyphen's doing and nothing else.
// Under reflow's miscount the hyphenated text wrapped up to three cells wider
// (one per hyphen on the line), and that surplus is what the second pass
// orphaned.
func TestProse_HyphenDoesNotWidenTheLine(t *testing.T) {
	plain := "aaxaa bbxbb ccxcc dddd eeee ffff gggg hhhh iiii jjjj kkkk"
	hyphen := "aa-aa bb-bb cc-cc dddd eeee ffff gggg hhhh iiii jjjj kkkk"
	for w := 20; w <= 60; w++ {
		wp, wh := widest(Prose(plain, w)), widest(Prose(hyphen, w))
		if wh > wp {
			t.Errorf("width %d: plain text wrapped at %d cells, hyphenated at %d — the hyphens bought %d cells they do not occupy",
				w, wp, wh, wh-wp)
		}
	}
}

func widest(rows []string) int {
	m := 0
	for _, r := range rows {
		if n := cells(strings.TrimRight(stripEscapesForWidth(r), " ")); n > m {
			m = n
		}
	}
	return m
}
