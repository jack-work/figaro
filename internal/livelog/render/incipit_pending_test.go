package render

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// withEcho gives the renderer a pending provider backed by a slice the test
// can mutate — the shape the CLI wires up (pendingChrome over the client).
func withEcho(in *Incipit, texts *[]string) {
	in.Pending = func(width int) []string {
		var rows []string
		for _, t := range *texts {
			if len(rows) > 0 {
				rows = append(rows, "")
			}
			rows = append(rows, "↳ queued", t)
		}
		return rows
	}
}

// A prompt submitted to a BUSY figaro is classified by the DRAIN, not by us,
// and the daemon broadcasts NOTHING at submit when the answer is "steer" — so
// the text is accepted, in the inbox, and on no screen until the next round
// boundary. The echo is what covers that window, and it must be visible in the
// LIVE REGION, above the pinned footer.
func TestIncipit_PendingEchoIsVisibleWhileTheAgentWorks(t *testing.T) {
	ft := NewFakeTerminal(70, 24)
	in := NewIncipit(ft, NodeText{})
	withChrome(in)
	texts := []string{"SECONDMESSAGE please acknowledge"}
	withEcho(in, &texts)

	// A turn we did not open is streaming a long tool.
	in.Open(aria.Message{Turn: 4, From: 0, Role: livedoc.RoleOutput, Nodes: []livedoc.Node{
		{ID: "n0", Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusRunning},
	}})

	screen := strings.Join(ft.Screen(), "\n")
	if !strings.Contains(screen, "SECONDMESSAGE") {
		t.Fatalf("the submitted prompt is nowhere on screen — this is the bug:\n%s", screen)
	}
	if !strings.Contains(screen, "↳ queued") {
		t.Errorf("the echo is unmarked; a reader cannot tell it from placed content:\n%s", screen)
	}
	// Above the footer, not below it: the footer is the bottom of the view.
	rows := ft.Screen()
	echo, foot := rowOf(rows, "SECONDMESSAGE"), rowOf(rows, "FOOTER thinking")
	if echo < 0 || foot < 0 || echo > foot {
		t.Errorf("echo at row %d, footer at row %d — the echo must sit above the footer:\n%s",
			echo, foot, screen)
	}
}

// The echo RESOLVES when the ack lands: the steering node arrives inside the
// open turn, the pending list empties, and the text is on screen ONCE — as the
// node, at its coordinate. Not twice, and not zero times.
func TestIncipit_EchoResolvesIntoTheNodeWithoutDoubleDrawing(t *testing.T) {
	ft := NewFakeTerminal(70, 24)
	in := NewIncipit(ft, NodeText{})
	withChrome(in)
	texts := []string{"MYSTEER: also set your mantra"}
	withEcho(in, &texts)

	in.Open(aria.Message{Turn: 4, From: 0, Role: livedoc.RoleOutput, Nodes: []livedoc.Node{
		{ID: "n0", Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusRunning},
	}})
	if n := countRows(ft.Screen(), "MYSTEER"); n != 1 {
		t.Fatalf("the echo appears %d times before the ack, want 1", n)
	}

	// The round boundary: the drain's steer becomes a node, and the client has
	// already dropped the echo (ackPending runs inside Apply).
	texts = nil
	in.Open(aria.Message{Turn: 4, From: 0, Role: livedoc.RoleOutput, Nodes: []livedoc.Node{
		{ID: "n0", Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK},
		{ID: "n1", Type: livedoc.NodeSteering, Role: livedoc.RoleInput,
			Markdown: "MYSTEER: also set your mantra"},
	}})

	rows := ft.Screen()
	if n := countRows(rows, "MYSTEER"); n != 1 {
		t.Fatalf("the steer appears %d times after the ack, want exactly 1:\n%s",
			n, strings.Join(rows, "\n"))
	}
	if n := countRows(rows, "↳ queued"); n != 0 {
		t.Errorf("the \"queued\" marker survived the ack %d times — the prompt has a "+
			"coordinate now:\n%s", n, strings.Join(rows, "\n"))
	}
}

// AN ECHO IS CHROME AND MUST NEVER BE FROZEN. Freeze commits the live region
// to native scrollback for good; a prompt with no coordinate committed there
// would be a permanent row claiming to be part of the message above it — and
// the real node, when it arrives, would print the same text a second time.
func TestIncipit_FreezeDoesNotCommitAnEcho(t *testing.T) {
	ft := NewFakeTerminal(70, 24)
	in := NewIncipit(ft, NodeText{})
	withChrome(in)
	texts := []string{"UNPLACED PROMPT"}
	withEcho(in, &texts)

	m := aria.Message{Turn: 4, From: 0, Role: livedoc.RoleOutput, Nodes: []livedoc.Node{
		{ID: "n0", Type: livedoc.NodeProse, Markdown: "APRICOT"},
	}}
	in.Open(m)
	in.Freeze(m)

	// Everything above the (now released) region is scrollback: the frozen
	// record. The echo may not be in it.
	all := strings.Join(ft.Screen(), "\n")
	if strings.Count(all, "UNPLACED PROMPT") != 0 {
		t.Fatalf("the echo was frozen into scrollback — it has no coordinate and cannot be "+
			"part of the record:\n%s", all)
	}
	if !strings.Contains(all, "APRICOT") {
		t.Fatalf("the message itself did not reach scrollback:\n%s", all)
	}
}

// rowOf is the first screen row containing s, or -1.
func rowOf(rows []string, s string) int {
	for i, r := range rows {
		if strings.Contains(r, s) {
			return i
		}
	}
	return -1
}

// countRows is how many screen rows contain s.
func countRows(rows []string, s string) int {
	n := 0
	for _, r := range rows {
		if strings.Contains(r, s) {
			n++
		}
	}
	return n
}
