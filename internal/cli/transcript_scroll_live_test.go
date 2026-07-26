package cli

import (
	"io"
	"strings"
	"testing"
	"time"
)

// The pager is either FOLLOWING the live tail or PINNED where you left it, and
// the screen has to say which. Every rule below was a bug a human found in his
// own terminal while the suite was green, so each one names the screen it
// stands in for. See the tmux-testing skill (~/.config/figaro/skills/tmux-testing.md): this file is the cheap half of
// that proof, not the whole of it.
func TestTranscriptScrollLiveContract(t *testing.T) {
	tr := scrollTranscript(t, io.Discard, 100, 40, 12)

	// LIVE: one blank row of padding above the rule, and the marker says live.
	if got := tr.prev[tr.h-3]; got != "" {
		t.Errorf("live frame has no padding above the rule: %q", got)
	}
	if !strings.Contains(tr.prev[tr.h-2], "live") {
		t.Errorf("live frame does not say live: %q", tr.prev[tr.h-2])
	}

	// SCROLLED: the padding row is given back to content — the last line sits
	// flush against the rule — and the marker is gone. Counted rather than read
	// off the screen because a content row is allowed to be blank itself.
	//
	// And that reclaimed row IS the notch: the first k out of live spends its
	// travel on the padding, so nothing scrolls. The window still ENDS on the
	// same line it did while live; it merely starts one line earlier. A press
	// that both detached and scrolled read as a two-row jump.
	live, end := len(tr.rowBuf), tr.offset+len(tr.rowBuf)
	tr.scrollBy(-1)
	if len(tr.rowBuf) != live+1 {
		t.Errorf("scrolled frame paints %d rows; live paints %d, and the padding row must become content",
			len(tr.rowBuf), live)
	}
	if got := tr.offset + len(tr.rowBuf); got != end {
		t.Errorf("the first notch out of live ended the window on line %d, was %d: that notch is spent on the padding, not on the content", got, end)
	}
	if strings.Contains(tr.prev[tr.h-2], "live") {
		t.Errorf("scrolled frame still says live: %q", tr.prev[tr.h-2])
	}

	// The SECOND notch is an ordinary one-line scroll.
	tr.scrollBy(-1)
	if got := tr.offset + len(tr.rowBuf); got != end-1 {
		t.Errorf("the second notch ended the window on line %d, want %d (exactly one row)", got, end-1)
	}

	// RE-ATTACH: reaching the last row is not enough; one row PAST it is.
	tr.scrollBy(1)
	if tr.follow {
		t.Error("reaching the last row must not re-attach to live")
	}
	tr.scrollBy(1)
	if !tr.follow {
		t.Error("scrolling past the last row must re-attach to live")
	}

	// PROMOTED BY THE SCROLL KEY: enter() and the keystroke arrive in one input
	// chunk and the frame-rate gate defers the frame between them, so follow
	// drops before anything has painted. The window must still be the settled
	// tail — this is 'k' from the inline view, which used to open on the cold
	// window the pager was constructed with: a screen of history erased.
	fresh := newTranscript(io.Discard, tr.w, tr.h, tr.view, tr.client, "aria0001", time.Unix(0, 0))
	fresh.gate = func() bool { return false }
	fresh.enter()
	fresh.key('k')
	if fresh.index.total != tr.index.total {
		t.Errorf("promotion by 'k' rendered %d lines; a following frame renders %d",
			fresh.index.total, tr.index.total)
	}
	if fresh.offset < fresh.index.total-fresh.h {
		t.Errorf("promotion by 'k' landed at line %d of %d, not at the tail",
			fresh.offset, fresh.index.total)
	}
}
