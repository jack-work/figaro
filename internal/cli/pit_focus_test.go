package cli

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// S OPENS THE FORM AND T MOVES THE FOCUS. The hook itself is the interesting
// part: it is called from dispatch, which holds the render lock, so an
// implementation that takes that lock freezes the pager -- measured in a pty,
// where `S` looked like a dead key and every key after it was dead too. The
// hook here is a counter, so what this test pins is the KEYMAP; the pty script
// pins the hand-off (S opens the pit, and the pager still answers afterwards).
func TestSKeyOpensForm(t *testing.T) {
	ft := ldrender.NewFakeTerminal(60, 20)
	tr := newTranscript(ft, 60, 20, ldrender.NodeText{}, aria.NewClient(), "aria1234", time.Unix(0, 0))
	tr.enter()
	called := 0
	tr.openForm = func() { called++ }
	tr.key('S')
	if called != 1 {
		t.Fatalf("S called openForm %d times (mode=%v)", called, tr.mode())
	}
	// and T with a pit open moves the focus
	tr.pit.showList(pitHelp, "", []pitRow{staticRow("a"), staticRow("b")})
	tr.focused = focusPit
	tr.full = true
	tr.dispatch(keyEvent{b: 'T', mode: modeTranscript})
	if tr.focused != focusTranscript {
		t.Fatalf("T did not hand the focus to the transcript")
	}
	if tr.fullPit() {
		t.Fatalf("a receded pit is still claiming the pane")
	}
	tr.dispatch(keyEvent{b: 'T', mode: modeTranscript})
	if tr.focused != focusPit || !tr.fullPit() {
		t.Fatalf("T did not give the pit its screen back")
	}
}
