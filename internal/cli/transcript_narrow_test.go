package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

func narrowPager(t *testing.T, w, h int) []string {
	t.Helper()
	var nodes []livedoc.Node
	for n := range 12 {
		nodes = append(nodes, livedoc.Node{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("reply line %d", n)})
	}
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 3, Sealed: true, Inquiry: "Almaviva! Your scene: Rosina's balcony.",
		InquirySegments: []aria.InquirySegment{{Sender: "borough-envoy", Text: "Almaviva! Your scene: Rosina's balcony."}},
		Nodes:           nodes,
	}}}}, aria.Notify)
	ft := ldrender.NewFakeTerminal(w, h)
	view := &ariaView{settings: &renderSettings{sticky: true}}
	tr := newTranscript(ft, w, h, view, client, "2a34c3e4ab0d6c48917c7c3484e760f5", time.Time{})
	tr.status.metrics = aria.Metrics{Mantra: "serenade", ContextTokens: 150_300}
	tr.status.limit = 200_000
	tr.enter()
	tr.buildIndex()
	return painted(tr, ft)
}

// gen6 zooms a pane down to 32x9. There the bar wrapped onto three rows with
// a blank one between its halves, and the sticky question covered three of the
// five rows above the rule, leaving one line of the reply. The bar wraps onto
// two rows, and the header never covers more of the body than it leaves.
func TestANarrowShortPagerKeepsItsConversation(t *testing.T) {
	for _, tc := range []struct {
		w, h      int
		barRows   int
		question  bool
		heading   bool
		replyRows int
	}{
		{48, 14, 1, true, true, 4},
		{48, 9, 1, true, true, 2},
		{32, 9, 2, false, true, 2},
		{80, 6, 1, false, false, 2},
	} {
		t.Run(fmt.Sprintf("%dx%d", tc.w, tc.h), func(t *testing.T) {
			screen := narrowPager(t, tc.w, tc.h)
			shown := strings.Join(screen, "\n")
			rule := tc.h - tc.barRows - 1
			if !strings.HasSuffix(screen[rule], "live") {
				t.Fatalf("row %d is not the rule:\n%s", rule, shown)
			}
			for r := rule + 1; r < tc.h; r++ {
				if strings.TrimSpace(screen[r]) == "" {
					t.Fatalf("row %d of the bar is blank:\n%s", r, shown)
				}
			}
			if !strings.HasSuffix(screen[tc.h-1], "~150.3k/200.0k 75.2%") || len([]rune(screen[tc.h-1])) != tc.w {
				t.Fatalf("the context figure is not flush right on the last row:\n%s", shown)
			}
			if got := strings.Contains(shown, "Rosina's balcony"); got != tc.question {
				t.Fatalf("question shown = %v, want %v:\n%s", got, tc.question, shown)
			}
			if got := strings.HasPrefix(screen[0], "∨ borough-envoy"); got != tc.heading {
				t.Fatalf("heading shown = %v:\n%s", got, shown)
			}
			replies := strings.Count(shown, "reply line")
			if replies < tc.replyRows {
				t.Fatalf("%d lines of the reply showing, want at least %d:\n%s", replies, tc.replyRows, shown)
			}
			covered := 0
			for _, row := range screen[:rule] {
				if strings.HasPrefix(row, "∨") || strings.Contains(row, "Rosina") || strings.HasPrefix(row, ruleDouble) {
					covered++
				}
			}
			if covered > rule-covered {
				t.Fatalf("the header covers %d of %d body rows:\n%s", covered, rule, shown)
			}
		})
	}
}
