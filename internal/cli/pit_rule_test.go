package cli

import (
	"strings"
	"testing"
	"time"
)

// Gluck, 11-16: the rule over a pit is double only while that pit holds the
// keyboard; when it takes it, the rule grows from single to double with the
// sticky header's dyadic expand.

func ruleCells(tr *transcript) (single, double int) {
	rule := stripANSI(tr.transcriptRule())
	return strings.Count(rule, ruleSingle), strings.Count(rule, ruleDouble)
}

func clockedQueuePager(t *testing.T, n int) (*transcript, *time.Time) {
	items := make([]queuedItem, n)
	for i := range items {
		items[i] = queuedItem{id: uint64(i + 1), text: "queued prompt"}
	}
	tr := queuePager(t, items...)
	now := time.Unix(100, 0)
	tr.now = func() time.Time { return now }
	return tr, &now
}

func TestAnUnfocusedPitIsRuledSingle(t *testing.T) {
	tr, _ := clockedQueuePager(t, 5)
	tr.showQueuedAuto(true)
	if tr.focused != focusTranscript || !tr.showing(pitQueue) {
		t.Fatalf("fixture: the queue should be open and unfocused")
	}
	if _, double := ruleCells(tr); double != 0 {
		t.Fatalf("an unfocused pit's rule has %d double cells: %q", double, stripANSI(tr.transcriptRule()))
	}
}

// Review of batch 1: a queue is usually one or two rows, and a height floor
// left T on it with no cue that the keys had moved.
func TestAOneRowPitIsRuledDoubleWhenFocused(t *testing.T) {
	tr, now := clockedQueuePager(t, 1)
	tr.key('Q')
	if tr.focused != focusPit || !tr.showing(pitQueue) {
		t.Fatalf("fixture: a one-row queue, focused; got focus %v", tr.focused)
	}
	ruleCells(tr)
	for range 200 {
		*now = now.Add(handoffTick())
	}
	if single, double := ruleCells(tr); single != 0 || double == 0 {
		t.Fatalf("a focused one-row pit is ruled %d single, %d double; want all double", single, double)
	}
}

func TestTakingFocusGrowsTheRuleToDouble(t *testing.T) {
	tr, now := clockedQueuePager(t, 5)
	tr.showQueuedAuto(true)
	ruleCells(tr)
	tr.key('T')
	single, double := ruleCells(tr)
	if single == 0 || double == 0 {
		t.Fatalf("the first frame after T is %d single, %d double; want the expand under way", single, double)
	}
	if !tr.handoffRunning() {
		t.Fatal("the expand is under way but nothing will wake the pager to finish it")
	}
	last := double
	for range 200 {
		*now = now.Add(handoffTick())
		_, d := ruleCells(tr)
		if d < last {
			t.Fatalf("the expand went backwards: %d then %d double cells", last, d)
		}
		last = d
	}
	if single, _ := ruleCells(tr); single != 0 || tr.handoffRunning() {
		t.Fatalf("the expand never finished: %d single cells left, running %v", single, tr.handoffRunning())
	}
	tr.key('T')
	if _, double := ruleCells(tr); double != 0 {
		t.Fatalf("handing focus back left %d double cells", double)
	}
}
