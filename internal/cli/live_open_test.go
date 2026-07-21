package cli

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// Live-turn model: committed prompt + an OPEN streaming message whose rows
// also match. Search/count/extend must see the open rows.
func TestVisual_OpenMessageRowsSearchable(t *testing.T) {
	ft := ldrender.NewFakeTerminal(80, 24)
	client := aria.NewClient()
	client.Apply(aria.AriaRead{Committed: []aria.Committed{{
		LT: 1, Role: "user",
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "run LIVEALPHA please"}},
	}}})
	client.Apply(aria.AriaRead{Live: &aria.Live{
		LT: 2, V: 0, Role: "assistant",
		Nodes: []aria.NodeDelta{
			{ID: "n0", Set: map[string]any{"type": "prose", "markdown": "ok LIVEALPHA coming"}},
			{ID: "n1", Set: map[string]any{"type": "prose", "markdown": "and LIVEALPHA again"}},
		},
	}})
	tr := newTranscript(ft, 80, 24, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()

	tr.findQuery("livealpha")
	tr.vmode = visualCursor
	cur, total, ok := tr.matchStats()
	if !ok || total != 3 {
		t.Fatalf("stats = %d/%d ok=%v, want x/3 (open rows must count)", cur, total, ok)
	}
	tr.key('v')
	tr.key('n')
	if tr.vCursor.lt != 2 {
		t.Fatalf("n must extend into the OPEN message (lt 2), got lt %d", tr.vCursor.lt)
	}
	text, okY := tr.visualYankText()
	if !okY || len(text) < 10 {
		t.Fatalf("yank across committed+open rows too small: %q", text)
	}
}
