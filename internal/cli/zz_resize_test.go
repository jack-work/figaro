package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// In-process resize probe: no tmux, no daemon, no model. Paint at one width,
// SetSize + Resize to another, and measure every row the painter EMITS after
// the resize.
func TestProbeResizeEmitsNarrowRows(t *testing.T) {
	md := "I'll need to read through the plan document and review the diffs to give a comprehensive response about the repository, the worktree setup, console issues on Windows, and my broader context around skills and approach."
	for _, tc := range []struct{ big, small int }{{120, 80}, {100, 61}, {80, 40}} {
		var buf bytes.Buffer
		set := renderSettings{}
		view := &ariaView{settings: &set}
		term := ldrender.NewANSITerminal(&buf, tc.big, 40)
		in := ldrender.NewIncipit(term, view)
		in.Header = messageHeader
		nodes := []livedoc.Node{{Type: livedoc.NodeThinking, Markdown: md, Status: livedoc.StatusOK}}
		in.Open(aria.Message{Turn: 1, Role: livedoc.RoleOutput, Nodes: nodes})

		buf.Reset() // measure ONLY what the resize repaint emits
		term.SetSize(tc.small, 40)
		in.Resize(nodes)

		worst, worstRow := 0, ""
		for _, line := range strings.Split(buf.String(), "\r\n") {
			if w := displayWidth(line); w > worst {
				worst, worstRow = w, line
			}
		}
		verdict := "ok"
		if worst > tc.small {
			verdict = "OVER"
		}
		t.Logf("%d -> %d: widest emitted row %d cells (%s) %q", tc.big, tc.small, worst, verdict, stripSGRForTest(worstRow))
	}
}
