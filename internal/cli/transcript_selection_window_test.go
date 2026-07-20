package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

func TestTranscript_OpenSelectionLoadsGapBeforeExtending(t *testing.T) {
	history := transcriptHistory(200)
	client := aria.NewClient()
	client.Apply(readBefore(history, recentCursor, transcriptPageSize))
	client.Apply(aria.AriaRead{Live: &aria.Live{
		LT: 201, V: 0, Role: "assistant",
		Nodes: []aria.NodeDelta{{
			ID: "open", Set: map[string]any{"type": string(livedoc.NodeProse), "markdown": "open-201"},
		}},
	}})
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.key('k')
	for range 4 {
		tr.offset = 0
		tr.checkOlder = true
		req, _ := tr.pageCursor()
		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize).Committed))
	}
	tr.selectNode(-1, false)
	focus := tr.selection.focus
	tr.selectNode(-1, true)
	if tr.selection.focus != focus || !tr.checkNewer {
		t.Fatalf("selection crossed gap: focus %+v -> %+v, checkNewer=%v", focus, tr.selection.focus, tr.checkNewer)
	}
	req, ok := tr.pageCursor()
	if !ok || req.direction != pageNewer {
		t.Fatalf("gap page request = %+v, %v", req, ok)
	}
}

func TestTranscript_ClearSelectionKeepsFocusedEdge(t *testing.T) {
	history := transcriptHistory(120)
	client := aria.NewClient()
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	for i := 0; i < 4; i++ {
		messages := committedMessages(history[i*30 : (i+1)*30])
		tr.pages = append(tr.pages, transcriptPage{
			desc:     describePage(messages),
			messages: messages,
		})
	}
	tr.selection = nodeSelection{
		active: true,
		anchor: testSelectionPoint(1, 0, history[0].Nodes[0]),
		focus:  testSelectionPoint(120, 0, history[119].Nodes[0]),
	}
	tr.clearSelection()
	messages := tr.messages()
	if len(tr.pages) != transcriptPageLimit || messages[len(messages)-1].LT != 120 {
		t.Fatalf("clear retained %d pages ending at LT %d", len(tr.pages), messages[len(messages)-1].LT)
	}
}

func TestTranscript_PagedSearchMatchesRenderedMarkdown(t *testing.T) {
	history := transcriptHistory(80)
	history[0].Nodes[0].Markdown = "foo **bar**"
	client := aria.NewClient()
	client.Apply(readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, &ariaView{settings: &renderSettings{}}, client, "", time.Time{})
	tr.enter()
	tr.findQuery("foo bar")
	for tr.searchingHistory() {
		req, ok := tr.pageCursor()
		if !ok {
			break
		}
		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize).Committed))
	}
	lines := tr.lines()
	if tr.offset >= len(lines) || !strings.Contains(lines[tr.offset], "foo") {
		t.Fatalf("rendered Markdown search did not land on match")
	}
}
