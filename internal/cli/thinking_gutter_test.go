package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

func TestProseIsNotQuoted(t *testing.T) {
	rows := nodeProseRows(livedoc.Node{Type: livedoc.NodeProse, Markdown: "plain prose, no rule"}, 60)
	for i, r := range rows {
		if strings.Contains(stripSGRForTest(r), "│") {
			t.Fatalf("prose row %d drew a rule: %q", i, stripSGRForTest(r))
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
