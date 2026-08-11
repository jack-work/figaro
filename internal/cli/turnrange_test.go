package cli

import (
	"testing"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// Every `figaro show` selector speaks TURN IDS. Before this there were three
// coordinate systems in one command: --from/--to were slice indices, --before
// was an LT, and the printed label was a third thing. This pins the collapse.
func TestSelectTurnRangeSpeaksTurnIDs(t *testing.T) {
	// Turn ids need not start at 1 or be contiguous: a forked trunk inherits
	// its parent's numbering, so the resolver must scan, not index-arithmetic.
	turns := []aria.Turn{{ID: 5}, {ID: 6}, {ID: 7}, {ID: 8}, {ID: 9}}
	base := showOpts{last: 10, from: -1, to: -1, before: -1}

	with := func(f func(*showOpts)) showOpts { o := base; f(&o); return o }

	cases := []struct {
		name   string
		opts   showOpts
		lo, hi int
	}{
		{"all", with(func(o *showOpts) { o.all = true }), 0, 5},
		{"last 2 paginates backwards from the end", with(func(o *showOpts) { o.last = 2 }), 3, 5},
		{"from turn 7", with(func(o *showOpts) { o.from = 7 }), 2, 5},
		{"from 6 to 7 inclusive", with(func(o *showOpts) { o.from = 6; o.to = 7 }), 1, 3},
		{"before turn 8, last 2", with(func(o *showOpts) { o.before = 8; o.last = 2 }), 1, 3},
		{"before the first turn is empty", with(func(o *showOpts) { o.before = 5; o.last = 3 }), 0, 0},
		{"unknown high turn clamps to the end", with(func(o *showOpts) { o.from = 99 }), 5, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lo, hi := selectTurnRange(turns, c.opts)
			if lo != c.lo || hi != c.hi {
				t.Fatalf("got [%d,%d), want [%d,%d)", lo, hi, c.lo, c.hi)
			}
		})
	}
}
