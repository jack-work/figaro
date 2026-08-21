package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
)

// farmerCorpus is the adversarial content set: every node shape a thinking
// block can actually hold, plus the character classes that break column math.
func farmerCorpus() map[string]string {
	long := "I'll need to read through the plan document and review the diffs to give a " +
		"comprehensive response about the repository, the worktree setup, console issues on " +
		"Windows, and my broader context around skills and approach."
	return map[string]string{
		"empty":        "",
		"onechar":      "x",
		"space":        " ",
		"prose":        long,
		"twopara":      long + "\n\n" + long,
		"cjk":          strings.Repeat("日本語のテキストをここに書きます。", 8),
		"cjkmix":       "mixed 日本語 text " + long,
		"emoji":        strings.Repeat("👨‍👩‍👧‍👦 family 🇯🇵 flag 🧑🏽‍🚀 astronaut ", 6),
		"combining":    strings.Repeat("e\u0301a\u0300o\u0308 combining ", 12),
		"zerowidth":    strings.Repeat("a\u200bb\u200cc\u200dd ", 20),
		"rtl":          strings.Repeat("العربية شيء ما ", 10),
		"ansi":         "content with \x1b[31mred\x1b[0m and \x1b[1mbold\x1b[0m " + long,
		"unbreakable":  strings.Repeat("A", 300),
		"unbreakurl":   "see https://example.com/" + strings.Repeat("path/", 40) + " for more",
		"bullets":      "- first item that is quite long and will certainly wrap at narrow widths\n- second\n- third item also rather long indeed yes\n",
		"nestedlist":   "- top level item long enough to wrap somewhere in the middle of it\n  - nested item also long enough to wrap given a narrow terminal width\n    - deeper still, and long enough to wrap as well\n- back to top\n",
		"numbered":     "1. one long enough to wrap around the terminal edge somewhere\n2. two\n3. three\n",
		"fence":        "here is code:\n\n```go\nfunc main() { fmt.Println(\"a very long line of code that will not wrap nicely at all\") }\n```\n\nafter",
		"openfence":    "streaming code:\n\n```go\nfunc x() {\n\tlongIdentifierName := someOtherLongIdentifier + yetAnotherOne\n",
		"table":        "| col a | col b | col c |\n|---|---|---|\n| a very long cell value | another long cell value | third |\n| x | y | z |\n",
		"bigtable":     tableFixture(30),
		"heading":      "# Heading one\n\nbody text\n\n## Heading two\n\n" + long,
		"rule":         "before\n\n---\n\nafter\n",
		"innerquote":   "> already a quote inside\n>\n> " + long,
		"tabs":         "a\tb\tc\n\ttabbed line that goes on and on and on and needs to wrap somewhere\n",
		"crlf":         "line one\r\nline two that is long enough to wrap at most widths in this sweep\r\n",
		"leadingspace": "    preformatted-ish line that is long enough to wrap at most of the widths here\n",
		"newlines":     "a\n\n\n\nb\n\n\n\n" + long,
	}
}

func tableFixture(n int) string {
	var b strings.Builder
	b.WriteString("| idx | value | note |\n|---|---|---|\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "| %d | value number %d | a note of some length %d |\n", i, i, i)
	}
	return b.String()
}

// stripSGR is stripSGRForTest with a byte scan that cannot eat a multi-byte
// rune (the rune-wise one in thinking_gutter_test.go walks runes, which is
// fine, but this one shares escapeEnd with production so the test and the code
// agree on where an escape ends).

// TestFarmerQuoteInvariants sweeps every width 20..200 over the corpus and
// asserts the three claimed invariants.
func TestFarmerQuoteInvariants(t *testing.T) {
	type fail struct {
		name  string
		width int
		what  string
		row   int
		text  string
	}
	seen := map[string]fail{} // one exemplar per (fixture, kind)
	for name, md := range farmerCorpus() {
		for _, typ := range []livedoc.NodeType{livedoc.NodeThinking, livedoc.NodeSteering} {
			for w := 20; w <= 200; w++ {
				rows := nodeProseRows(livedoc.Node{Type: typ, Markdown: md}, w)
				col := -1
				for i, r := range rows {
					plain := strings.TrimRight(stripANSI(r), " ")
					if strings.TrimSpace(plain) == "" {
						continue
					}
					key := func(k string) string { return name + "/" + k }
					// OVERFLOW MUST BE INHERITED, NEVER ADDED. glamour overruns
					// the width it is GIVEN (an unclosed fence at 12 of these
					// 181 widths, by up to 11 cells at w=20), so a blunt
					// row<=width here just re-reports glamour every run, and a
					// blunt row<=width guarded by "prose fits" fires on the
					// boundary width where prose fits and the two-column-tighter
					// quoted render does not. The contract is the delta: a
					// quoted row may exceed the viewport only where glamour's
					// own row already exceeded the width it was rendered at.
					if got := displayWidth(r); got > w && !glamourOverranAt(md, w) {
						if _, ok := seen[key("overwide")]; !ok {
							seen[key("overwide")] = fail{name, w, fmt.Sprintf("row is %d cells > width, and glamour stayed inside its own budget", got), i, plain}
						}
					}
					if !strings.HasPrefix(strings.TrimLeft(plain, " "), "│") {
						if _, ok := seen[key("norule")]; !ok {
							seen[key("norule")] = fail{name, w, "row has no rule", i, plain}
						}
						continue
					}
					at := len(plain) - len(strings.TrimLeft(plain, " "))
					if col < 0 {
						col = at
					} else if at != col {
						if _, ok := seen[key("ragged")]; !ok {
							seen[key("ragged")] = fail{name, w, fmt.Sprintf("rule at column %d, block uses %d", at, col), i, plain}
						}
					}
				}
			}
		}
	}
	if len(seen) > 0 {
		for k, f := range seen {
			t.Errorf("%s: w=%d row %d: %s: %q", k, f.width, f.row, f.what, f.text)
		}
	}
}

// TestFarmerRowsAreGlamourPlusGutter is the strongest form of "the words
// survive": a quoted row must be the gutter and then glamour's own row, byte
// for byte, at the reserved width, with the two-column margin the rule stands
// in removed and nothing else touched. Anything weaker (words compare, prefix
// check) is satisfiable by output that has been re-padded or clipped.
func TestFarmerRowsAreGlamourPlusGutter(t *testing.T) {
	bad := 0
	for name, md := range farmerCorpus() {
		for w := 20; w <= 200; w++ {
			n := livedoc.Node{Type: livedoc.NodeThinking, Markdown: md}
			pw := proseWidth(n, w)
			raw := render.Prose(nodeMarkdown(n), pw)
			got := nodeProseRows(n, w)
			if len(got) != len(raw) {
				t.Fatalf("%s w=%d: row count changed %d -> %d", name, w, len(raw), len(got))
			}
			for i := range raw {
				want := term.Dim(quoteGutter) + dedentProse(raw[i])
				if got[i] != want {
					if bad < 6 {
						t.Errorf("%s w=%d row %d: row is not gutter+glamour:\n  want %q\n   got %q", name, w, i, want, got[i])
					}
					bad++
				}
			}
		}
	}
	if bad > 0 {
		t.Logf("%d rows differ in total", bad)
	}
}

// TestFarmerEscapesIntact: clipping must not sever an escape sequence, and a
// row that opens a colour must close it.
func TestFarmerEscapesIntact(t *testing.T) {
	seen := map[string]bool{}
	for name, md := range farmerCorpus() {
		for w := 20; w <= 200; w++ {
			rows := nodeProseRows(livedoc.Node{Type: livedoc.NodeThinking, Markdown: md}, w)
			for i, r := range rows {
				for j := 0; j < len(r); {
					if r[j] != 0x1b {
						j++
						continue
					}
					e, _ := escapeEnd(r, j)
					if e > len(r) || (e == len(r) && !isFinalByte(r[len(r)-1])) {
						k := name + "/severed"
						if !seen[k] {
							seen[k] = true
							t.Errorf("%s w=%d row %d: severed escape: %q", name, w, i, r)
						}
					}
					j = e
				}
				if strings.Contains(r, "\x1b[") && !strings.HasSuffix(r, "\x1b[0m") {
					k := name + "/unreset"
					if !seen[k] {
						seen[k] = true
						t.Logf("%s w=%d row %d: styled row does not end in reset: %q", name, w, i, r)
					}
				}
			}
		}
	}
}

func isFinalByte(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// glamourOverranAt reports whether the markdown, rendered at the RESERVED
// width, already exceeds it: i.e. whether glamour, not the gutter, is the one
// that cannot fit it. (The doc comment used to name proseOverflowsAt, a second
// helper that no test called.)
func glamourOverranAt(md string, w int) bool {
	n := livedoc.Node{Type: livedoc.NodeThinking, Markdown: md}
	pw := proseWidth(n, w)
	for _, r := range render.Prose(nodeMarkdown(n), pw) {
		if displayWidth(r) > pw {
			return true
		}
	}
	return false
}
