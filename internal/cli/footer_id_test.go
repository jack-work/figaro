package cli

import (
	"testing"
	"time"
)

// The tmux smoke cases find the attended aria by reading the status bar. They
// only run with FIGARO_TMUX_SMOKE set, so a regex that stops matching what the
// bar paints would go unnoticed until someone runs them. This pins the two
// readers to the real renderer, through the session's own view, for every id
// length a store can hold.
func TestSmokeReadersFindTheAriaTheStatusBarPaints(t *testing.T) {
	for _, full := range []string{"a5647bfb", "a5647bfbc216b8a09ffea9586393c289"} {
		s := &sessionStatus{figaroID: full}
		s.metrics.Mantra = "a mantra"
		id := s.viewOf(pitNothing, false, time.Now()).Aria
		if id != ariaName(full) {
			t.Fatalf("the bar paints %q for %s, want its name %q", id, full, ariaName(full))
		}
		row := statusView{Aria: id, Mantra: "a mantra", Ctx: "12%"}.render(160)
		capture := ""
		for _, r := range row {
			capture += r + "\n"
		}
		if got := ariaIDOnScreen(capture); got != id {
			t.Errorf("ariaIDOnScreen read %q off %q, want %q", got, capture, id)
		}
		if m := footerAriaID.FindStringSubmatch(capture); m == nil || m[1] != id {
			t.Errorf("footerAriaID read %v off %q, want %q", m, capture, id)
		}
	}
}
