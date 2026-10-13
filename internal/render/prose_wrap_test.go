package render

import (
	"strings"
	"testing"
)

// glamour wraps a paragraph twice; a first pass that miscounts a breakpoint
// rune makes the second orphan the paragraph's tail on its own row.

var wrapCorpus = []string{
	"Three hundred and forty-nine workshops. In one town. Making one thing.",
	"The name \"catlinite,\" note, honors George Catlin, the nineteenth-century American painter who documented Plains peoples: which is to say the stone that Indigenous nations had been quarrying for two millennia is known to geology by the surname of a white man who saw it in the 1830s.",
	"pipe factory there in 1825 and is said to have begun carving native bruyère around 1840; the trade's own historians settle on the mid-1850s: 1854 or 1856, as the real inflection point, and are frank that the name of the man who cut the first briar pipe is simply gone.",
	"I need to respect the rate limit of one request per second, so I'll batch the queries together in a single bash command with 1.5-second delays between each request to stay safely under the limit.",
}

// The oracle re-wraps at the widest row the renderer produced: the asked-for
// width is spent partly on the document margin, so it cannot be the bound.
func prematureBreak(rows []string) (int, string) {
	var ink []string
	budget := 0
	for _, r := range rows {
		t := strings.TrimSpace(stripANSI(r))
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

func TestProse_NoPrematureBreak(t *testing.T) {
	for w := 30; w <= 100; w++ {
		for i, md := range wrapCorpus {
			rows := Prose(md, w)
			if n, detail := prematureBreak(rows); n > 0 {
				t.Errorf("width %d, paragraph %d: %d row(s) broken early:\n  %s", w, i, n, detail)
			}
			for _, r := range rows {
				if n := cells(strings.TrimRight(stripANSI(r), " ")); n > w {
					t.Errorf("width %d, paragraph %d: row of %d cells: %q", w, i, n, stripANSI(r))
				}
			}
		}
	}
}

// Same-length texts, 'x' against '-' in the same slots: any wrap difference is the hyphen's.
func TestProse_HyphenDoesNotWidenTheLine(t *testing.T) {
	plain := "aaxaa bbxbb ccxcc dddd eeee ffff gggg hhhh iiii jjjj kkkk"
	hyphen := "aa-aa bb-bb cc-cc dddd eeee ffff gggg hhhh iiii jjjj kkkk"
	for w := 20; w <= 60; w++ {
		wp, wh := widest(Prose(plain, w)), widest(Prose(hyphen, w))
		if wh > wp {
			t.Errorf("width %d: plain text wrapped at %d cells, hyphenated at %d: the hyphens bought %d cells they do not occupy",
				w, wp, wh, wh-wp)
		}
	}
}

func widest(rows []string) int {
	m := 0
	for _, r := range rows {
		if n := cells(strings.TrimRight(stripANSI(r), " ")); n > m {
			m = n
		}
	}
	return m
}
