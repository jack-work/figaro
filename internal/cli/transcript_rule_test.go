package cli

import (
	"strings"
	"testing"
	"time"
)

// Gluck: the double bar separates the header and nothing else; a message in
// the body is bordered by single rules.
func TestRule_OnlyTheHeadersEdgeIsDouble(t *testing.T) {
	tr := stickyPager(t, 1, 4, 6, 24)
	_, maxOff := tr.layoutNow()
	pinned := 0
	for off := 0; off <= maxOff; off++ {
		tr.offset = off
		tr.buildIndex()
		head := plain(headRowsOf(tr))
		body := plain(bodyRows(tr))
		for i, row := range body[min(len(head), len(body)):] {
			if strings.Contains(row, ruleDouble) {
				t.Fatalf("offset %d: body row %d is a double rule: %q", off, len(head)+i, row)
			}
		}
		if n := len(head); n > stickyText {
			pinned++
			if edge := head[n-1]; strings.Trim(edge, ruleDouble+"╧╤╪") != "" {
				t.Fatalf("offset %d: the header's edge is not double: %q", off, edge)
			}
		}
	}
	if pinned == 0 {
		t.Fatal("fixture: no offset pinned a header with its rule")
	}
}

func TestRule_JointTakesTheWeightOfTheCellItCuts(t *testing.T) {
	gutter := gutterCut{row: "  │ x", hidden: "  │ y"}
	mixed := dimRuleOf(8, func(i int) bool { return i < 4 })
	if got := stripANSI(joinRule(mixed, gutter, gutterCut{})); got != "══╧═────" {
		t.Errorf("a joint in the double half: %q", got)
	}
	cut := gutterCut{row: "      │ x", hidden: "      │ y"}
	if got := stripANSI(joinRule(mixed, cut, gutterCut{})); got != "════──┴─" {
		t.Errorf("a joint in the single half: %q", got)
	}
}

func edgeOffsets(t *testing.T, tr *transcript) (pinned, loose int) {
	t.Helper()
	_, maxOff := tr.layoutNow()
	for off := 1; off <= maxOff; off++ {
		tr.offset = off
		if turn, _ := tr.stickyEdge(); turn != 0 {
			tr.offset = off - 1
			if turn, _ := tr.stickyEdge(); turn == 0 {
				return off, off - 1
			}
		}
	}
	t.Fatal("fixture: no step where the header gains its edge")
	return 0, 0
}

// Defends the handoff Gluck asked for: the rule changes weight cell by cell from
// the dyadic seeds, both ways, and settles.
func TestRule_HandoffAnimatesFromTheDyadicSeeds(t *testing.T) {
	tr := stickyPager(t, 1, 4, 6, 24)
	clock := time.Unix(1000, 0)
	tr.now = func() time.Time { return clock }
	var woke []time.Duration
	tr.wake = func(d time.Duration) { woke = append(woke, d) }
	pinned, loose := edgeOffsets(t, tr)
	whole := time.Duration(dyadicReach(handoffLevel, max(tr.w, 3))+1) * handoffTick()

	tr.offset = loose
	tr.renderFrame()
	tr.offset = pinned
	tr.renderFrame()
	if len(woke) != 1 {
		t.Fatalf("the pin did not ask for the next frame: %v", woke)
	}
	edge := plain(headRowsOf(tr))
	mid := edge[len(edge)-1]
	if !strings.Contains(mid, ruleSingle) {
		t.Fatalf("at the first frame the edge is already whole: %q", mid)
	}
	clock = clock.Add(whole / 2)
	edge = plain(headRowsOf(tr))
	half := edge[len(edge)-1]
	if strings.Count(half, ruleDouble) <= strings.Count(mid, ruleDouble) || !strings.Contains(half, ruleSingle) {
		t.Fatalf("halfway the edge did not grow from its seeds: %q then %q", mid, half)
	}
	if !strings.HasPrefix(half, ruleDouble) || !strings.HasSuffix(half, ruleDouble) {
		t.Fatalf("the ends are seeds and turn first: %q", half)
	}
	clock = clock.Add(whole)
	edge = plain(headRowsOf(tr))
	if done := edge[len(edge)-1]; strings.Contains(done, ruleSingle) {
		t.Fatalf("the edge did not settle double: %q", done)
	}

	tr.offset = loose
	tr.renderFrame()
	clock = clock.Add(whole / 2)
	tr.renderFrame()
	if tr.handoff.releaseLine < tr.offset {
		t.Fatal("the release lost its line")
	}
	row := plain([]string{tr.prev[tr.handoff.releaseLine-tr.offset]})[0]
	if !strings.Contains(row, ruleDouble) || !strings.Contains(row, ruleSingle) {
		t.Fatalf("halfway through the release the old edge is %q", row)
	}
	clock = clock.Add(whole)
	tr.renderFrame()
	if row := plain([]string{tr.prev[tr.handoff.releaseLine-tr.offset]})[0]; strings.Contains(row, ruleDouble) {
		t.Fatalf("the released rule did not settle single: %q", row)
	}
}

// gen5 t29: on the 60 fps lockstep grid the edge's wipe grew a cell every 2
// or 3 frames (2910, 2912, 2915, 2918, 2921, 2923), a judder beside the
// spinners' even 5. Each cell of the wipe now lands on the same number of
// frames.
func TestRule_HandoffGrowsOnAnEvenFrameCadence(t *testing.T) {
	tr := stickyPager(t, 1, 4, 6, 72)
	start := time.Unix(1000, 0)
	clock := start
	tr.now = func() time.Time { return clock }
	tr.wake = func(time.Duration) {}
	pinned, loose := edgeOffsets(t, tr)
	tr.offset = loose
	tr.renderFrame()
	tr.offset = pinned
	tr.renderFrame()
	var changes []int
	last := -1
	for n := 0; n <= 60; n++ {
		clock = start.Add(time.Duration(int64(n) * int64(time.Second) / 60))
		edge := plain(headRowsOf(tr))
		doubles := strings.Count(edge[len(edge)-1], ruleDouble)
		if doubles != last {
			if last >= 0 {
				changes = append(changes, n)
			}
			last = doubles
		}
	}
	if len(changes) < 3 {
		t.Fatalf("the wipe changed on frames %v: too few to judge a cadence", changes)
	}
	for i := 2; i < len(changes); i++ {
		if changes[i]-changes[i-1] != changes[1]-changes[0] {
			t.Fatalf("the wipe advanced on frames %v: an uneven cadence", changes)
		}
	}
}
