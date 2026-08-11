package cli

import (
	"bytes"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// A resize must reach the pager even while the pager is HIDDEN.
//
// The pager enters itself: the frame path opens it the moment the live region
// grows taller than the viewport, which can be minutes after the terminal was
// resized. Until this was fixed it entered with the width it had been
// CONSTRUCTED with, and then painted every frame at that width forever.
//
// MEASURED before the fix, from inside the process (FIGARO_WIDTH_AUDIT), one
// resize 100 -> 40 with six seconds of settling first: 68 rows written past the
// edge, up to 100 cells into a 40-column pane, still going fourteen seconds
// later, every one of them from (*transcript).paint. After: 0.
//
// This is the "text beyond the right side" report. It needed a width CHANGE to
// appear, which is why fixed-width sweeps at 20..200 were clean and honest.
//
// CANARY (watched): delete the `t.tr.setSize(w, h)` call in livelogTurn.resize
// and this fails with `hidden pager kept width 100 after a resize to 40`.
func TestResizeReachesTheHiddenPager(t *testing.T) {
	var buf bytes.Buffer
	set := renderSettings{}
	status := newSessionStatus("test1234", time.Now())
	lt := newLivelogTurn(&buf, 100, 40, &set, "test1234", time.Now(), status, nil, func() string { return "" })

	if lt.tr.active {
		t.Fatal("fixture: the pager must start hidden, or this proves nothing")
	}
	if lt.tr.w != 100 {
		t.Fatalf("fixture: transcript should start at 100, got %d", lt.tr.w)
	}

	lt.resize(40, 40)

	if lt.tr.w != 40 {
		t.Fatalf("hidden pager kept width %d after a resize to 40", lt.tr.w)
	}
	if lt.tr.h != 40 {
		t.Fatalf("hidden pager kept height %d after a resize to 40", lt.tr.h)
	}
}

// setSize must drop the row cache: it is keyed by (turn, from) and NOT by
// width, and buildIndex reads it rather than re-rendering, so rows composed for
// the old width would be re-served to the new viewport.
//
// NOTE ON WHAT THIS DOES NOT PROVE. The same assertion against resize() passes
// with the invalidation REMOVED: buildIndex evicts every entry whose turn is
// outside the window, and with an empty client that is all of them. A test that
// passes for the wrong reason is worse than no test, so it is not here. resize's
// invalidation is held by the end-to-end measurement instead: one resize
// 100 -> 40, six seconds of settling, 68 rows past the edge before and 0 after.
//
// CANARY (watched): delete invalidateRows() from setSize and this fails with
// `setSize kept 1 cached row set(s)`.
func TestSetSizeDropsTheWidthDependentRowCache(t *testing.T) {
	var buf bytes.Buffer
	set := renderSettings{}
	tr := newTranscript(&buf, 100, 40, &ariaView{settings: &set}, aria.NewClient(), "test1234", time.Now())

	tr.rowCache[sliceKey(1)] = cachedMessage{}
	tr.setSize(40, 40)
	if n := len(tr.rowCache); n != 0 {
		t.Fatalf("setSize kept %d cached row set(s) rendered at the old width", n)
	}
	if tr.prev != nil {
		t.Fatal("setSize must also void the painter's model of the screen")
	}
}
