package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

func forkDeltas(form, parent string) map[string]livedoc.FormDelta {
	return map[string]livedoc.FormDelta{
		form + ".system.forked_from": {
			Value: []byte(`"` + parent + `"`),
			Kind:  livedoc.FormBound, Event: livedoc.FormSet, Form: form,
		},
	}
}

// A transcript of `turns` turns, with fork banners on the turns named.
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

// f k walks back through the fork points, f j forward, and each landing
// selects the table it landed on.
func TestForkJumpTravelsBetweenForkPoints(t *testing.T) {
	tr := forkedTranscript(t, 8, map[int]string{2: "aaaa1111", 6: "bbbb2222"})
	if got := len(tr.forkPoints()); got != 2 {
		t.Fatalf("want 2 fork points, got %d", got)
	}
	tr.key('f')
	if tr.mode() != modeFork {
		t.Fatalf("f must arm the fork mode, got %v", tr.mode())
	}
	tr.key('k') // backwards from the tail: the later fork first
	first := tr.selection.focus.nodeRef
	if !first.delta || first.turn != 6 {
		t.Fatalf("f k should land on turn 6's fork table, got %+v", first)
	}
	tr.key('f')
	tr.key('k')
	if ref := tr.selection.focus.nodeRef; !ref.delta || ref.turn != 2 {
		t.Fatalf("a second f k should land on turn 2's, got %+v", ref)
	}
	tr.key('f')
	tr.key('j')
	if ref := tr.selection.focus.nodeRef; !ref.delta || ref.turn != 6 {
		t.Fatalf("f j should come back down to turn 6's, got %+v", ref)
	}
}

// An 'f' followed by anything else is that key, not a swallowed one.
func TestForkModeReleasesAnUnclaimedKey(t *testing.T) {
	tr := forkedTranscript(t, 8, map[int]string{2: "aaaa1111"})
	tr.key('f')
	tr.key('?')
	if tr.mode() == modeFork {
		t.Fatal("the gesture must end with the second key")
	}
	if !tr.pit.open() {
		t.Fatal("f then ? must open the help panel")
	}
}

// 'a' on a fork point hands the parent id to the session's attend.
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

// The jumplist is the browser's: back, forward, and a new visit drops
// whatever was ahead of the cursor.
func TestAriaJumplist(t *testing.T) {
	var j ariaJumplist
	j.visit("a")
	j.visit("b")
	j.visit("c")
	if id, ok := j.hop(-1); !ok || id != "b" {
		t.Fatalf("back = %q %v", id, ok)
	}
	if id, ok := j.hop(-1); !ok || id != "a" {
		t.Fatalf("back again = %q %v", id, ok)
	}
	if _, ok := j.hop(-1); ok {
		t.Fatal("there is nothing older than the first aria")
	}
	if id, ok := j.hop(1); !ok || id != "b" {
		t.Fatalf("forward = %q %v", id, ok)
	}
	// Arriving where the cursor already stands is not a jump: that is what
	// makes a hop's own attend idempotent.
	j.visit("b")
	if pos, total := j.where(); pos != 2 || total != 3 {
		t.Fatalf("a hop's arrival must not grow the list: %d/%d", pos, total)
	}
	j.visit("d")
	if pos, total := j.where(); pos != 3 || total != 3 {
		t.Fatalf("a visit from the middle drops what was ahead: %d/%d", pos, total)
	}
	if id, ok := j.hop(1); ok {
		t.Fatalf("nothing is newer than the aria just visited, got %q", id)
	}
}

func stripANSIAll(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = stripANSI(l)
	}
	return out
}

// `a` with no selection takes the fork point on screen, which is where the
// reader is looking after a fork retargets the pager.
func TestAttendFromTheForkPointOnScreen(t *testing.T) {
	tr := forkedTranscript(t, 3, map[int]string{2: "aaaa1111"})
	var attended string
	tr.attendAria = func(id string) { attended = id }
	tr.key('a')
	if attended != "aaaa1111" {
		t.Fatalf("a with no selection attends the visible fork point, got %q", attended)
	}
}

// A report from a pager row must reach the reader. It goes to the bar's
// notice, not to jumpNote, which the dispatcher's epilogue wipes on the
// very key that wrote it: that is why `a` on a fork point looked like a
// dead key in a pty.
func TestAPagerRowsNoteReachesTheBar(t *testing.T) {
	tr, ft := forkedTranscriptOn(t, 3, nil) // no fork points: `a` has something to say
	tr.attendAria = func(string) {}
	tr.key('a')
	tr.render()
	screen := strings.Join(stripANSIAll(ft.Screen()), "\n")
	if !strings.Contains(screen, "no fork point") {
		t.Fatalf("the note never reached the screen:\n%s", screen)
	}
}
