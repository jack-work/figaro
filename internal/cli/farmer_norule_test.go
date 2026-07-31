package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

func TestFarmerBlocksWithNoRuleAtAll(t *testing.T) {
	cases := map[string]string{
		"html":       "<div>some html block</div>",
		"onlyfence":  "```\ncode line\n```",
		"image":      "![alt text](https://example.com/img.png)",
		"linkref":    "[ref]: https://example.com\n\nsee [ref]",
		"setext":     "Title\n=====\n\nbody",
		"hr":         "---",
		"blank":      "\n\n",
		"onlyspace":  "   ",
		"onlyquote":  ">",
		"deepquote":  ">>> nested",
		"tabsonly":   "\t\t\t",
		"backslash":  "line one\\\nline two",
		"entity":     "&amp; &lt; &gt;",
		"footnote":   "text[^1]\n\n[^1]: the note",
		"checkbox":   "- [ ] todo item\n- [x] done item",
		"deflist":    "term\n: definition",
		"longword80": strings.Repeat("z", 79),
	}
	for name, md := range cases {
		for _, w := range []int{20, 40, 80, 120, 200} {
			rows := nodeProseRows(livedoc.Node{Type: livedoc.NodeThinking, Markdown: md}, w, false)
			nonblank, ruled := 0, 0
			for _, r := range rows {
				p := strings.TrimSpace(farmerStrip(r))
				if p == "" {
					continue
				}
				nonblank++
				if strings.HasPrefix(p, "│") {
					ruled++
				}
			}
			if nonblank > 0 && ruled != nonblank {
				t.Errorf("%s w=%d: %d/%d rows ruled; first rows: %q", name, w, ruled, nonblank, firstFew(rows))
			}
		}
	}
}

func firstFew(rows []string) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, strings.TrimRight(farmerStrip(r), " "))
		if len(out) >= 3 {
			break
		}
	}
	return out
}
