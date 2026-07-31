package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// Where does a row still exceed its width, and is it the gutter's doing or
// glamour's? A quoted row is compared with the SAME markdown rendered as prose:
// if prose overflows too, the gutter is not the cause.
func TestFarmerOverflowAttribution(t *testing.T) {
	corp := map[string]string{
		"innerquote": "> a quoted line inside the node, long enough to wrap somewhere around here\n\n> and another",
		"cjk":        strings.Repeat("日本語のテキスト", 6),
		"url":        "see https://example.com/" + strings.Repeat("path/", 20),
		"fence":      "```go\nfunc main() { println(\"a long line of code\") }\n```",
		"nested":     "- one\n  - two\n    - three item long enough to wrap at narrow widths\n",
		"prose":      strings.Repeat("word ", 60),
	}
	for name, md := range corp {
		var q, p []string
		for w := 20; w <= 200; w++ {
			over := func(typ livedoc.NodeType) bool {
				for _, r := range nodeProseRows(livedoc.Node{Type: typ, Markdown: md}, w, false) {
					if displayWidth(r) > w {
						return true
					}
				}
				return false
			}
			if over(livedoc.NodeThinking) {
				q = append(q, fmt.Sprint(w))
			}
			if over(livedoc.NodeProse) {
				p = append(p, fmt.Sprint(w))
			}
		}
		t.Logf("%-11s thinking overflows at %3d/181 widths, prose at %3d/181  (thinking: %s)",
			name, len(q), len(p), strings.Join(q[:farmerMin(len(q), 8)], ","))
	}
}

func farmerMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}
