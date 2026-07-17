package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

func TestTranscriptNodeSelectionRangeAndCopy(t *testing.T) {
	ft := ldrender.NewFakeTerminal(80, 20)
	client := aria.NewClient()
	client.Apply(aria.AriaRead{Committed: []aria.Committed{{
		LT: 1, Role: "assistant", Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "first node"},
			{Type: livedoc.NodeProse, Markdown: "second node"},
			{Type: livedoc.NodeTool, Name: "bash", Output: "tool output"},
		},
	}}})
	tr := newTranscript(ft, 80, 20, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()

	tr.key(0x0e) // Ctrl-N
	if !tr.selection.active || tr.selection.focus != (nodeRef{lt: 1, index: 0}) {
		t.Fatalf("first selection = %+v", tr.selection)
	}
	tr.selectNode(1, true)
	tr.render()
	if tr.selection.focus != (nodeRef{lt: 1, index: 1}) {
		t.Fatalf("range focus = %+v", tr.selection)
	}
	text, ok := tr.selectedText()
	if !ok || text != "first node\n\nsecond node" {
		t.Fatalf("selected text = %q, %v", text, ok)
	}
	if screen := strings.Join(ft.Screen(), "\n"); !strings.Contains(screen, "▸") || !strings.Contains(screen, "│") {
		t.Fatalf("selection gutters missing:\n%s", screen)
	}
	tr.clearSelection()
	if tr.hasSelection() {
		t.Fatal("copying a selection must leave Ctrl-C available for interrupt")
	}
}

func TestTranscriptEnterExpandsSelectedToolOutput(t *testing.T) {
	ft := ldrender.NewFakeTerminal(80, 30)
	client := aria.NewClient()
	lines := make([]string, 12)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%02d", i)
	}
	client.Apply(aria.AriaRead{Committed: []aria.Committed{{
		LT: 1, Role: "assistant", Nodes: []livedoc.Node{{
			Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK, Output: strings.Join(lines, "\n"),
		}},
	}}})
	tr := newTranscript(ft, 80, 30, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Now())
	tr.enter()
	tr.key(0x0e) // Ctrl-N
	if got := stripANSI(strings.Join(tr.lines(), "\n")); strings.Contains(got, "line-00") {
		t.Fatalf("collapsed tool leaked first output line:\n%s", got)
	}

	tr.key(0x0d) // Enter
	if got := stripANSI(strings.Join(tr.lines(), "\n")); !strings.Contains(got, "line-00") {
		t.Fatalf("expanded tool omitted full output:\n%s", got)
	}
	text, ok := tr.selectedText()
	if !ok || !strings.Contains(text, "line-00") || !strings.Contains(text, "line-11") {
		t.Fatalf("copied tool output = %q, %v", text, ok)
	}
}
