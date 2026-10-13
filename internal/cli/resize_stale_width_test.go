package cli

import (
	"bytes"
	"testing"
	"time"

	"github.com/jack-work/figaro/sdk/aria"
)

// The pager enters itself from the frame path, possibly minutes after a
// resize, so a resize must reach it while hidden.
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

// The row cache is keyed by (turn, from), not width, so setSize must drop it.
// The same assertion against resize() passes with the invalidation removed
// (buildIndex evicts everything over an empty client), so it is not made there.
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
