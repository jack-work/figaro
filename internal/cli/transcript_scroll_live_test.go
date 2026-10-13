package cli

import (
	"io"
	"strings"
	"testing"
	"time"
)

// The screen must say whether the pager is following the live tail or pinned.
func TestTranscriptScrollLiveContract(t *testing.T) {
	tr := scrollTranscript(t, io.Discard, 100, 40, 12)

	// Live: one padding row above the rule.
	if got := tr.prev[tr.h-3]; got != "" {
		t.Errorf("live frame has no padding above the rule: %q", got)
	}
	if !strings.Contains(tr.prev[tr.h-2], "live") {
		t.Errorf("live frame does not say live: %q", tr.prev[tr.h-2])
	}

	// The first notch out of live spends its travel on the padding row: the
	// window still ends on the same line, it starts one earlier.
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

	tr.scrollBy(-1)
	if got := tr.offset + len(tr.rowBuf); got != end-1 {
		t.Errorf("the second notch ended the window on line %d, want %d (exactly one row)", got, end-1)
	}

	tr.scrollBy(1)
	if tr.follow {
		t.Error("reaching the last row must not re-attach to live")
	}
	tr.scrollBy(1)
	if !tr.follow {
		t.Error("scrolling past the last row must re-attach to live")
	}

	// enter() and the keystroke arrive in one chunk with the frame deferred,
	// so follow drops before anything painted; the window must still be the tail.
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
