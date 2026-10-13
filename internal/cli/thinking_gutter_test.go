package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/render"
)

// Three properties: every non-blank row carries the rule, every rule sits in
// the same column, and the words are exactly glamour's words.
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

				// The oracle is glamour's own output at the same
				// width, not the raw markdown: glamour drops fence
				// languages and re-flows text.
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
					if !strings.HasPrefix(plain[at:], "│") {
						t.Fatalf("%v/%s w=%d: row %d has no rule: %q", typ, name, w, i, plain)
					}
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

// words strips the rule, spacing and markdown punctuation.
func words(s string) string {
	s = stripSGRForTest(s)
	for _, drop := range []string{"│", "`", "|", "-", "*", ">", "─", "•"} {
		s = strings.ReplaceAll(s, drop, " ")
	}
	return strings.Join(strings.Fields(s), " ")
}

func TestProseIsNotQuoted(t *testing.T) {
	rows := nodeProseRows(livedoc.Node{Type: livedoc.NodeProse, Markdown: "plain prose, no rule"}, 60)
	for i, r := range rows {
		if strings.Contains(stripSGRForTest(r), "│") {
			t.Fatalf("prose row %d drew a rule: %q", i, stripSGRForTest(r))
		}
	}
}

// glamour itself overruns the width it is given on nested lists and fences,
// so the assertion is the delta: the gutter costs its columns and nothing more.
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
				// The rule stands in glamour's margin where there
				// is one (2 net); a flush row pays the full 4.
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
