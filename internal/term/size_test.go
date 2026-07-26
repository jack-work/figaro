package term

import "testing"

// A terminal that reports 2 rows has 2 rows. The old guards (`c > 20`,
// `r > 2`) treated a small-but-real measurement as noise and substituted a
// fabricated 80x24 — so figaro painted 24 rows into a 2-row pane and every
// repaint scrolled the previous frame into scrollback, printing a streaming
// reply several times over, each copy longer than the last.
//
// Only the absence of a measurement justifies a default.
func TestSizeOr_KeepsSmallButRealMeasurements(t *testing.T) {
	for _, c := range []struct {
		name                     string
		measured, fallback, want int
	}{
		{"two rows is a real height", 2, 24, 2},
		{"one row is a real height", 1, 24, 1},
		{"three rows is a real height", 3, 24, 3},
		{"narrow but real width", 15, 80, 15},
		{"twenty columns is a real width", 20, 80, 20},
		{"zero means no measurement", 0, 24, 24},
		{"negative means no measurement", -1, 80, 80},
		{"ordinary sizes are untouched", 40, 24, 40},
	} {
		if got := sizeOr(c.measured, c.fallback); got != c.want {
			t.Errorf("%s: sizeOr(%d, %d) = %d, want %d",
				c.name, c.measured, c.fallback, got, c.want)
		}
	}
}
