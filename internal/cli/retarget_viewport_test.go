package cli

import (
	"github.com/jack-work/figaro/internal/livelog/aria"
	"testing"
)

func TestRetentionUsesViewportNotRetainedFloor(t *testing.T) {
	tr := forkPager(t, 40, 21, 12)
	tr.follow = false
	tr.from = aria.Anchor{Turn: 1}
	tr.invalidateWindow()
	tr.buildIndex()
	span, ok := tr.nodeSpanOf(nodeRef{turn: 30, index: 0})
	if !ok {
		t.Fatal("turn 30 missing from fixture")
	}
	tr.offset = span.first
	if tr.keepsScroll(21) {
		t.Fatal("viewport shows divergent turn 30; retained turn 1 must not keep its offset")
	}
	span, ok = tr.nodeSpanOf(nodeRef{turn: 20, index: 0})
	if !ok {
		t.Fatal("turn 20 missing from fixture")
	}
	tr.offset = span.first
	if !tr.keepsScroll(21) {
		t.Fatal("viewport on shared turn 20 should stay")
	}
}
