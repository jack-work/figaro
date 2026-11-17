package cli

import (
	"strings"
	"testing"
	"time"
)

// Gluck, 11-17: the floor rule (the one over the status bar) rests single and
// animates to double, the dyadic expand, only when the bottom chrome shows
// something beyond the minimized status bar: a pit, the composer, search, and
// so on. Keyboard focus does not decide the weight (ebe77f74d read that wrong).

func floorChromeRow(t testing.TB, tr *transcript) string {
	t.Helper()
	tr.renderFrame()
	foot := tr.footLines()
	row := tr.h - 1 - tr.barRows() - len(foot)
	if row < 0 || row >= len(tr.prev) {
		t.Fatalf("floor row %d out of range for h=%d, foot=%d", row, tr.h, len(foot))
	}
	return stripANSI(tr.prev[row])
}

func TestFloorRule_RestIsSingle(t *testing.T) {
	tr := queuePager(t)
	row := floorChromeRow(t, tr)
	if strings.Contains(row, ruleDouble) {
		t.Fatalf("a resting floor rule is double: %q", row)
	}
	if !strings.Contains(row, ruleSingle) {
		t.Fatalf("a resting floor rule has no rule at all: %q", row)
	}
}

func TestFloorRule_EachNonDefaultSurfaceGoesDoubleThenSingleAgain(t *testing.T) {
	whole := time.Duration(dyadicReach(handoffLevel, max(72, 3))+1) * handoffTick()
	cases := []struct {
		name  string
		open  func(tr *transcript)
		close func(tr *transcript)
	}{
		{"a queue pit", func(tr *transcript) {
			tr.queued = []queuedItem{{id: 1, text: "queued"}}
			tr.key('Q')
		}, func(tr *transcript) { tr.key(0x1b) }},
		{"the composer", func(tr *transcript) { tr.key('>') }, func(tr *transcript) {
			tr.key(0x1b)
			tr.key(0x1b)
		}},
		{"search", func(tr *transcript) { tr.key('/') }, func(tr *transcript) { tr.key(0x1b) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := queuePager(t)
			clock := time.Unix(1000, 0)
			tr.now = func() time.Time { return clock }

			tc.open(tr)
			if !tr.chromeExpanded() {
				t.Fatalf("fixture: %s did not open the chrome", tc.name)
			}
			clock = clock.Add(whole)
			if row := floorChromeRow(t, tr); strings.Contains(row, ruleSingle) {
				t.Fatalf("%s settled with single cells left: %q", tc.name, row)
			} else if !strings.Contains(row, ruleDouble) {
				t.Fatalf("%s never went double: %q", tc.name, row)
			}

			tc.close(tr)
			if tr.chromeExpanded() {
				t.Fatalf("fixture: %s did not close", tc.name)
			}
			clock = clock.Add(whole)
			if row := floorChromeRow(t, tr); strings.Contains(row, ruleDouble) {
				t.Fatalf("%s left double cells after minimizing: %q", tc.name, row)
			} else if !strings.Contains(row, ruleSingle) {
				t.Fatalf("%s minimized to nothing: %q", tc.name, row)
			}
		})
	}
}

func TestFloorRule_AnimatesOneCellPerTickBothWays(t *testing.T) {
	activeLockstep = &lockstep{fps: lockstepFPS}
	defer func() { activeLockstep = nil }()

	tr := queuePager(t, queuedItem{id: 1, text: "queued"})
	start := time.Unix(1000, 0)
	clock := start
	tr.now = func() time.Time { return clock }
	tr.wake = func(time.Duration) {}

	tr.key('Q')
	if !tr.showing(pitQueue) {
		t.Fatalf("fixture: the queue did not open")
	}

	cadence := func(dir string, grow bool) {
		var changes []int
		last := -1
		for n := 0; n <= 90; n++ {
			clock = start.Add(time.Duration(n) * handoffTick())
			doubles := strings.Count(floorChromeRow(t, tr), ruleDouble)
			if doubles != last {
				if last >= 0 {
					changes = append(changes, n)
					if grow && doubles < last {
						t.Fatalf("%s: double cells fell from %d to %d at frame %d", dir, last, doubles, n)
					}
					if !grow && doubles > last {
						t.Fatalf("%s: double cells rose from %d to %d at frame %d", dir, last, doubles, n)
					}
				}
				last = doubles
			}
		}
		if len(changes) < 3 {
			t.Fatalf("%s: too few changes to judge a cadence: %v", dir, changes)
		}
		step := changes[1] - changes[0]
		for i := 2; i < len(changes); i++ {
			if changes[i]-changes[i-1] != step {
				t.Fatalf("%s advanced on frames %v: an uneven cadence", dir, changes)
			}
		}
	}

	cadence("grow", true)
	if row := floorChromeRow(t, tr); strings.Contains(row, ruleSingle) {
		t.Fatalf("the grow never settled all double: %q", row)
	}

	tr.key(0x1b)
	if tr.pit.open() {
		t.Fatalf("fixture: the queue did not close")
	}
	start = clock
	cadence("shrink", false)
	if row := floorChromeRow(t, tr); strings.Contains(row, ruleDouble) {
		t.Fatalf("the shrink never settled all single: %q", row)
	}
}
