package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
)

func TestForkHistoryPTY(t *testing.T) {
	if testing.Short() {
		t.Skip("drives replay through tmux")
	}
	page := aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 138, Sealed: true, Inquiry: "inspect the fork seam",
		InquirySegments: []aria.InquirySegment{{Sender: "Gluck", Text: "inspect the fork seam"}},
		FormDeltas:      adornDeltas("system.forked_from", "", "6a2a5d694665e751f5533e46ecbec399", "system.model", "old-model", "new-model", "system.provider", "old-provider", "new-provider"),
		Nodes:           []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "FORKHISTORYBODY", FormDeltas: adornDeltas("operations.subscription_policy", "", "keep the legitimate policy change")}},
	}}}}
	p := newAdornPaneFor(t, page, "FORKHISTORYBODY")
	if p.height() != adornRows {
		t.Fatalf("pane height %d, want %d", p.height(), adornRows)
	}
	rows := p.rows()
	heading := p.only(t, rows, "Gluck")
	if !strings.Contains(rows[heading], forkGlyph+" "+ariaName("6a2a5d694665e751f5533e46ecbec399")) {
		t.Fatalf("birth marker did not reach the input heading: %s", p.dump())
	}
	p.key("C-n")
	p.key("Enter")
	rows = p.rows()
	for _, key := range []string{"system.model", "system.provider"} {
		p.only(t, rows, key)
	}
	if hasRow(rows, "system.forked_from") || hasRow(rows, "operations.subscription_policy") {
		t.Fatalf("question section mixed in the lifted fork or node delta: %s", p.dump())
	}
	if countGlyph(rows, deltaGlyph) > 1 {
		t.Fatalf("a small question delta grew extra rows: %s", p.dump())
	}
	t.Logf("fork heading and two question changes:\n%s", p.capture(false))
	p.key("Enter")
	p.key("C-n")
	p.key("Enter")
	rows = p.rows()
	p.only(t, rows, "operations.subscription_poli")
	if hasRow(rows, "system.model") || hasRow(rows, "system.provider") {
		t.Fatalf("node section retained the question's deltas: %s", p.dump())
	}
	t.Logf("separate legitimate policy change:\n%s", p.capture(false))
}
