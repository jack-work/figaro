package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// Ground truth for the visual overlay: capture the raw ANSI the pager writes.
func TestVisual_PaintWritesAllOverlaidRows(t *testing.T) {
	var buf bytes.Buffer
	client := aria.NewClient()
	for i := 1; i <= 6; i++ {
		client.Apply(aria.AriaRead{Committed: []aria.Committed{{
			LT: i, Role: "assistant",
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("Msg%02d body", i)}},
		}}})
	}
	tr := newTranscript(&buf, 60, 24, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Now())
	tr.enter()
	tr.key('g')
	tr.key('g')
	tr.lines()
	tr.vCursor = tr.rowToPoint(2, 1)
	tr.hasCursor = true
	tr.startVisual(visualChar)
	buf.Reset()
	tr.vCursor = tr.rowToPoint(12, 2)
	tr.render()
	got := buf.String()
	n := strings.Count(got, visualBgOn)
	if n < 5 {
		t.Fatalf("paint wrote %d bg-opens, want >=5 (rows 2..12 incl. rule+header)\npayload: %q", n, got)
	}
}
