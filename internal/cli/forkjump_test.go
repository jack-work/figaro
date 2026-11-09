package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

func forkDeltas(form, parent string) map[string]livedoc.FormDelta {
	return map[string]livedoc.FormDelta{
		form + ".system.forked_from": {
			Value: []byte(`"` + parent + `"`),
			Kind:  livedoc.FormBound, Event: livedoc.FormSet, Form: form,
		},
	}
}

func forkedTranscript(t *testing.T, turns int, forks map[int]string) *transcript {
	t.Helper()
	tr, _ := forkedTranscriptOn(t, turns, forks)
	return tr
}

func forkedTranscriptOn(t *testing.T, turns int, forks map[int]string) (*transcript, *ldrender.FakeTerminal) {
	t.Helper()
	ft := ldrender.NewFakeTerminal(60, 12)
	client := aria.NewClient()
	for i := 1; i <= turns; i++ {
		turn := aria.Turn{
			ID: uint64(i), Sealed: true, Inquiry: "q",
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: strings.Repeat("body ", 6)}},
		}
		if parent, ok := forks[i]; ok {
			turn.FormDeltas = forkDeltas("a1", parent)
		}
		client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: turn}}}, aria.Notify)
	}
	tr := newTranscript(ft, 60, 12, ldrender.NodeText{}, client, "aria1234", time.Unix(0, 0))
	tr.enter()
	return tr, ft
}

func TestForkJumpTravelsBetweenForkPoints(t *testing.T) {
	tr := forkedTranscript(t, 8, map[int]string{2: "aaaa1111", 6: "bbbb2222"})
	if got := len(tr.forkPoints()); got != 2 {
		t.Fatalf("want 2 fork points, got %d", got)
	}
	tr.key('f')
	if tr.pending != "fork" {
		t.Fatalf("f must arm the fork jump, pending %q", tr.pending)
	}
	tr.key('k')
	first := tr.selection.focus.nodeRef
	if first.meta != 0 || first.index != inquiryNode || first.turn != 6 {
		t.Fatalf("f k should land on turn 6's question, got %+v", first)
	}
	tr.key('f')
	tr.key('k')
	if ref := tr.selection.focus.nodeRef; ref.meta != 0 || ref.turn != 2 {
		t.Fatalf("a second f k should land on turn 2's, got %+v", ref)
	}
	tr.key('f')
	tr.key('j')
	if ref := tr.selection.focus.nodeRef; ref.meta != 0 || ref.turn != 6 {
		t.Fatalf("f j should come back down to turn 6's, got %+v", ref)
	}
}

func TestForkModeReleasesAnUnclaimedKey(t *testing.T) {
	tr := forkedTranscript(t, 8, map[int]string{2: "aaaa1111"})
	tr.key('f')
	tr.key('h')
	if tr.pending == "fork" {
		t.Fatal("the gesture must end with the second key")
	}
	if !tr.pit.open() {
		t.Fatal("f then h must open the help panel")
	}
}

func TestAttendFromAForkPoint(t *testing.T) {
	tr := forkedTranscript(t, 4, map[int]string{3: "aaaa1111"})
	var attended string
	tr.attendAria = func(id string) { attended = id }
	tr.key('f')
	tr.key('k')
	tr.key('a')
	if attended != "aaaa1111" {
		t.Fatalf("a on a fork point attends its parent, got %q", attended)
	}
}

func stripANSIAll(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = stripANSI(l)
	}
	return out
}

func TestAttendFromTheForkPointOnScreen(t *testing.T) {
	tr := forkedTranscript(t, 3, map[int]string{2: "aaaa1111"})
	var attended string
	tr.attendAria = func(id string) { attended = id }
	tr.key('a')
	if attended != "aaaa1111" {
		t.Fatalf("a with no selection attends the visible fork point, got %q", attended)
	}
}

// A row's report goes to the bar's notice, not jumpNote, which the dispatcher
// wipes on the key that wrote it.
func TestAPagerRowsNoteReachesTheBar(t *testing.T) {
	tr, ft := forkedTranscriptOn(t, 3, nil)
	tr.attendAria = func(string) {}
	tr.key('a')
	tr.render()
	screen := strings.Join(stripANSIAll(ft.Screen()), "\n")
	if !strings.Contains(screen, "no fork point") {
		t.Fatalf("the note never reached the screen:\n%s", screen)
	}
}
