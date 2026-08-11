package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/render"
)

// A thinking block draws a rule down its left edge. Three properties:
//
//	PRESENT  every non-blank row carries the rule
//	ALIGNED  every rule in the block sits in the SAME column
//	INTACT   the words are exactly the words, nothing trimmed
//
// All three matter because the first two versions of this test asserted only
// PRESENT, and the third only PRESENT and ALIGNED, and each time the broken
// output satisfied everything asserted. Output with the rule drawn one column
// too far left is PRESENT. Output with two characters chopped off the right is
// PRESENT and ALIGNED. A property that is true of the bug is not a test; INTACT
// is what catches a repair that pays for the rule with somebody's words.
//
// The corpus is deliberately not one paragraph: fences, tables, unbreakable
// tokens, URLs, CJK, emoji and mixed content are where the wrap decisions
// differ, and the earlier "0 of 81 widths" result held only for the one
// paragraph it was measured on.
//
// CANARY (watched): render at `width` instead of proseWidth(n, width) and every
// row overflows by four cells at every width.
func TestThinkingRuleIsPresentAlignedAndLossless(t *testing.T) {
	shapes := map[string]string{
		"paragraph": "I'll need to read through the plan document and review the diffs to give a " +
			"comprehensive response about the repository, the worktree setup, console issues on " +
			"Windows, and my broader context around skills and approach.",
		"long-token": "prefix longIdentifierNameThatCannotBeBrokenAcrossLinesAtAll suffix",
		"url":        "see https://example.com/a/very/long/path/that/will/not/break/anywhere for details",
		"fence":      "before\n```go\nfunc f() { return errors.New(\"a fairly long line of code\") }\n```\nafter",
		"table":      "| col | another column |\n|---|---|\n| a | b |\n| c | d |",
		"cjk":        "これは日本語のテキストで、折り返しの計算を確かめるためのものです。さらに続きます。",
		"emoji":      "status 🎉 shipped 🚀 and 🧪 tested 🔬 across 🌍 every 🖥 width we care about",
		"list":       "- first item that runs on a while\n- second item\n  - nested item that also runs on",
		"empty":      "",
		"blank":      "   ",
	}

	for _, typ := range []livedoc.NodeType{livedoc.NodeThinking, livedoc.NodeSteering} {
		for name, md := range shapes {
			for w := 20; w <= 200; w++ {
				n := livedoc.Node{Type: typ, Markdown: md}
				rows := nodeProseRows(n, w)

				// INTACT: the same words glamour rendered, in the same order.
				// The oracle is glamour's OWN output at the same width, not the
				// raw markdown: glamour legitimately drops a fence language and
				// re-flows text, and comparing against markdown would fail for
				// its choices instead of for this transform's mistakes.
				plainRows := render.Prose(nodeMarkdown(n), proseWidth(n, w))
				if got, want := words(strings.Join(rows, "\n")), words(strings.Join(plainRows, "\n")); got != want {
					t.Fatalf("%v/%s w=%d: words changed\n got: %q\nwant: %q", typ, name, w, got, want)
				}

				col := -1
				for i, r := range rows {
					plain := strings.TrimRight(stripSGRForTest(r), " ")
					if strings.TrimSpace(plain) == "" {
						continue
					}
					at := len(plain) - len(strings.TrimLeft(plain, " "))
					// PRESENT
					if !strings.HasPrefix(plain[at:], "│") {
						t.Fatalf("%v/%s w=%d: row %d has no rule: %q", typ, name, w, i, plain)
					}
					// ALIGNED
					if col < 0 {
						col = at
					} else if at != col {
						t.Fatalf("%v/%s w=%d: row %d rules at column %d, the block uses %d: %q",
							typ, name, w, i, at, col, plain)
					}
				}
			}
		}
	}
}

// words is visible text with the rule and all spacing removed: what a renderer
// may never change. Markdown punctuation is stripped so the comparison is about
// content, not about how glamour chose to draw a fence or a table.
func words(s string) string {
	s = stripSGRForTest(s)
	for _, drop := range []string{"│", "`", "|", "-", "*", ">", "─", "•"} {
		s = strings.ReplaceAll(s, drop, " ")
	}
	return strings.Join(strings.Fields(s), " ")
}

// TestProseIsNotQuoted guards the other side: ordinary assistant prose must NOT
// grow a rule. Without it, "every row has a rule" could be satisfied by giving
// every node one.
func TestProseIsNotQuoted(t *testing.T) {
	rows := nodeProseRows(livedoc.Node{Type: livedoc.NodeProse, Markdown: "plain prose, no rule"}, 60)
	for i, r := range rows {
		if strings.Contains(stripSGRForTest(r), "│") {
			t.Fatalf("prose row %d drew a rule: %q", i, stripSGRForTest(r))
		}
	}
}

// TestTheGutterCostsExactlyItsColumns is the honest form of "rows fit".
//
// A blunt `row <= width` is NOT true at this level and asserting it would be a
// lie with a green tick: glamour itself overruns the width it is given on a
// nested list, a fence, an indented block and an unclosed fence (by up to seven
// cells), so a quoted row inherits that. What this function is responsible for
// is the DELTA: the gutter costs its four columns, minus the two-column margin
// it stands in, and nothing more. Every painter owns the edge beyond that.
func TestTheGutterCostsExactlyItsColumns(t *testing.T) {
	shapes := []string{
		"a reasonably long sentence that will certainly need to wrap at narrow widths",
		"- nested\n  - list\n    - deeper item that runs on for a while here",
		"```\nunclosed fence with a long line of content inside it\n",
		"> a model-written blockquote that is long enough to wrap somewhere",
		"これは日本語のテキストで、折り返しの計算を確かめるためのものです。",
	}
	for _, md := range shapes {
		for w := 20; w <= 200; w++ {
			n := livedoc.Node{Type: livedoc.NodeThinking, Markdown: md}
			quoted := nodeProseRows(n, w)
			plain := render.Prose(nodeMarkdown(n), proseWidth(n, w))
			if len(quoted) != len(plain) {
				t.Fatalf("w=%d: %d quoted rows vs %d rendered", w, len(quoted), len(plain))
			}
			for i := range quoted {
				// The rule STANDS IN glamour's margin where there is one, so it
				// costs 2 net. A row with no margin to stand in, a hard-wrap
				// continuation chunk, or a row glamour emitted flush: pays the
				// full 4. Both are correct; what must never happen is paying
				// more than the gutter is wide.
				cost := quoteGutterCells
				if strings.HasPrefix(stripSGRForTest(plain[i]), proseIndent) {
					cost -= len(proseIndent)
				}
				want := displayWidth(plain[i]) + cost
				if got := displayWidth(quoted[i]); got != want {
					t.Fatalf("w=%d row %d: %d cells, want %d (gutter costs %d here): %q",
						w, i, got, want, cost, stripSGRForTest(quoted[i]))
				}
				if got := displayWidth(quoted[i]); got > w {
					t.Fatalf("w=%d row %d: %d cells, past the edge: %q", w, i, got, stripSGRForTest(quoted[i]))
				}
			}
		}
	}
}

func stripSGRForTest(s string) string {
	out, r, i := make([]rune, 0, len(s)), []rune(s), 0
	for i < len(r) {
		if r[i] == 0x1b {
			for i < len(r) && !((r[i] >= 'A' && r[i] <= 'Z') || (r[i] >= 'a' && r[i] <= 'z')) {
				i++
			}
			i++
			continue
		}
		out = append(out, r[i])
		i++
	}
	return string(out)
}
