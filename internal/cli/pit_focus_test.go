package cli

import (
	"testing"
	"time"

	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// The hook is called from dispatch under the render lock; this pins the
// keymap, the pty script pins the hand-off.
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
