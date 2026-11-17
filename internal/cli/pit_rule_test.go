package cli

import (
	"strings"
	"testing"
	"time"
)

// Gluck, 11-16: the rule over a pit is double only while that pit holds the
// keyboard and is more than two rows tall; when it becomes so, it grows from
// single to double with the sticky header's dyadic expand.

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

func TestACollapsedPitIsRuledSingleEvenFocused(t *testing.T) {
	tr, _ := clockedQueuePager(t, 1)
	tr.key('Q')
	if tr.focused != focusPit || tr.pitHeight() > pitCollapsedRows {
		t.Fatalf("fixture: a one-row queue, focused; got focus %v height %d", tr.focused, tr.pitHeight())
	}
	if _, double := ruleCells(tr); double != 0 {
		t.Fatalf("a collapsed pit's rule has %d double cells", double)
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

func TestAPitGrowingPastCollapsedPlaysTheExpand(t *testing.T) {
	tr, _ := clockedQueuePager(t, 1)
	tr.key('Q')
	ruleCells(tr)
	tr.queued = append(tr.queued, queuedItem{id: 2, text: "two"}, queuedItem{id: 3, text: "three"})
	tr.showQueuedAuto(true)
	if tr.pitHeight() <= pitCollapsedRows {
		t.Fatalf("fixture: the queue did not grow, height %d", tr.pitHeight())
	}
	if single, double := ruleCells(tr); single == 0 || double == 0 {
		t.Fatalf("growing out of collapsed drew %d single, %d double; want the expand under way", single, double)
	}
}
