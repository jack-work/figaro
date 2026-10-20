package cli

import (
	"bytes"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/sdk/aria"
)

type paneWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (p *paneWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buf.Write(b)
}

func (p *paneWriter) take() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.buf.String()
	p.buf.Reset()
	return s
}

var cupRow = regexp.MustCompile(`\x1b\[(\d+);\d+H`)

func rowsWritten(frame string) map[string]bool {
	rows := map[string]bool{}
	for _, m := range cupRow.FindAllStringSubmatch(frame, -1) {
		rows[m[1]] = true
	}
	return rows
}

// The painter repaints the whole screen when its model is two seconds old, to
// heal what another writer may have done. A fold the reader asked for is not
// that moment: opening one tool after an idle pause writes its own rows, not
// the screen.
func TestAFoldTheReaderAskedForIsNotAResync(t *testing.T) {
	out := &paneWriter{}
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Sealed: true, Inquiry: "run it",
		Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "above the tool"},
			{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK,
				Args: map[string]any{"command": "seq 3"}, Output: "1\n2\n3"},
		},
	}}}}, aria.Notify)
	tr := newTranscript(out, 80, 30, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
	tr.enter()
	tr.follow = false
	tr.render()
	tool := nodeRef{turn: 1, index: 1}
	tr.selectRef(tool, false)
	tr.render()

	clock := time.Unix(100, 0)
	tr.now = func() time.Time { return clock }
	tr.lastFull = clock
	clock = clock.Add(time.Minute)
	out.take()

	if !tr.toggleSelectedNodes() {
		t.Fatal("fixture: the tool did not open")
	}
	tr.render()
	written := rowsWritten(out.take())
	if len(written) == 0 || len(written) >= 30 {
		t.Fatalf("opening one tool wrote %d rows", len(written))
	}
	if !strings.Contains(strings.Join(stripANSIAll(tr.lines()), "\n"), "✓ bash") {
		t.Fatal("fixture: the open tool shows no detail")
	}
}
