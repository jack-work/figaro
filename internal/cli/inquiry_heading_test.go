package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

func TestInquiryHeadingNamesTheSender(t *testing.T) {
	for _, tc := range []struct{ sender, heading string }{
		{"Gluck", "> Gluck"},
		{"", "> input"},
		{"aria 1234abcd", "> figaro 1234abcd"},
	} {
		t.Run(tc.heading, func(t *testing.T) {
			m := aria.Message{Turn: 1, Inquiry: "please run the tests", Role: livedoc.RoleOutput,
				InquirySegments: []aria.InquirySegment{{Sender: tc.sender, Text: "please run the tests"}},
				FormDeltas:      adornDeltas("system.forked_from", "", "e87bb154", "mantra", "", "test the heading"),
				Nodes:           []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "done"}},
			}
			rows := plain(renderTurnRows(m, 100, 0, renderSettings{}))
			if !strings.HasPrefix(rows[0], tc.heading+" ⑂ e87bb154") || !strings.HasSuffix(rows[0], "Δ") {
				t.Fatalf("heading lost sender, fork or delta marker: %q", rows[0])
			}
			if rows[1] != "  please run the tests" {
				t.Fatalf("the heading must sit on the first line of the question: %q", rows[:3])
			}
		})
	}
}

func TestStickyHeadingKeepsSenderForkTurnAndDelta(t *testing.T) {
	tr := gutterPager(t, livedoc.Node{Type: livedoc.NodeThinking,
		Markdown: strings.Repeat("Thinking below the question.\n\n", 20)})
	m := aria.Turn{ID: 1, Inquiry: "what should happen here?", Sealed: true,
		InquirySegments: []aria.InquirySegment{{Sender: "Gluck", Text: "what should happen here?"}},
		FormDeltas:      adornDeltas("system.forked_from", "", "e87bb154", "mantra", "", "test the heading"),
		Nodes:           []livedoc.Node{{Type: livedoc.NodeThinking, Markdown: strings.Repeat("Thinking below the question.\n\n", 20)}},
	}
	tr.client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: m}}}, aria.Notify)
	tr.invalidateRows()
	tr.buildIndex()
	for _, width := range []int{100, 48, 26} {
		tr.resize(width, 18)
		span, _ := tr.nodeSpanOf(nodeRef{turn: 1, index: 0})
		tr.offset = span.first + 2
		tr.render()
		rows := headRowsOf(tr)
		if len(rows) != 3 {
			t.Fatalf("width %d: want metadata, text, rule; got %q", width, rows)
		}
		header := stripANSI(rows[0])
		if !strings.HasPrefix(header, "∨ Gluck ⑂ e87bb154") || !strings.HasSuffix(header, "1 Δ") {
			t.Fatalf("width %d: sticky metadata incomplete: %q", width, header)
		}
		for _, row := range rows {
			if ldrender.Width(row) > width {
				t.Fatalf("width %d: header row overflowed: %q", width, row)
			}
		}
		if !strings.Contains(stripANSI(rows[1]), "what should happen") {
			t.Fatalf("sticky lost the question: %q", rows[1])
		}
	}
}
