package cli

import (
	"io"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
)

// liveRegion paints one live unit (a turn) to the terminal as a build
// log: finalized rows are flushed to native scrollback once and never
// touched again; only the active tail below the commit watermark is
// re-rendered and line-diffed in place. It owns all cursor motion.
//
// Cursor invariant between operations: parked at column 0 of the top row
// of the live region (the first row after the flushed prefix). Not safe
// for concurrent use — the caller (notify pump + a spinner ticker) must
// serialize calls.
type liveRegion struct {
	out     io.Writer
	width   int
	bashCap int

	blob    string
	tick    uint64
	flushed int      // rows committed to scrollback this unit
	live    []string // rows currently shown in the live region
}

func newLiveRegion(out io.Writer, width, bashCap int) *liveRegion {
	return &liveRegion{out: out, width: width, bashCap: bashCap}
}

// snapshot replaces the unit's blob wholesale (unit start or resync) and
// repaints from scratch: any on-screen live rows are cleared first.
func (lr *liveRegion) snapshot(blob string) {
	if len(lr.live) > 0 || lr.flushed > 0 {
		io.WriteString(lr.out, eraseToEnd)
	}
	lr.blob = blob
	lr.flushed = 0
	lr.live = nil
	lr.repaint(true)
}

// applyDelta folds one splice into the blob and repaints.
func (lr *liveRegion) applyDelta(d livedoc.Delta) {
	lr.blob = livedoc.Apply(lr.blob, d)
	lr.repaint(true)
}

// running reports whether a spinner is animating (the blob carries a
// sentinel), so the caller can run/stop its tick timer.
func (lr *liveRegion) running() bool {
	for _, r := range lr.blob {
		if r == render.SpinnerSentinel {
			return true
		}
	}
	return false
}

// tickSpin advances the spinner one frame and repaints (no-op if no
// spinner is running).
func (lr *liveRegion) tickSpin() {
	if !lr.running() {
		return
	}
	lr.tick++
	lr.repaint(true)
}

// resize re-renders the live tail at a new width. The flushed prefix
// keeps its commit-time width (the terminal rewraps it natively);
// best-effort, a one-frame artifact at the boundary is acceptable.
func (lr *liveRegion) resize(width int) {
	if width == lr.width || width <= 0 {
		return
	}
	lr.width = width
	io.WriteString(lr.out, eraseToEnd) // clear the live tail; scrollback above is untouched
	lr.live = nil
	lr.repaint(false) // don't advance the watermark on a resize frame
}

// commit freezes the unit: drop the cursor below the live region (its
// rows are already final on screen) and reset for the next unit.
func (lr *liveRegion) commit() {
	if n := len(lr.live); n > 0 {
		io.WriteString(lr.out, term.CursorDown(n))
		io.WriteString(lr.out, "\r")
	}
	lr.blob = ""
	lr.tick = 0
	lr.flushed = 0
	lr.live = nil
}

// repaint renders the blob, flushes any newly-stable rows to scrollback,
// and line-diffs the remaining tail. With allowFlush=false the watermark
// is pinned (used by resize so already-committed rows aren't reprinted).
func (lr *liveRegion) repaint(allowFlush bool) {
	res := render.Render(lr.blob, render.Options{Width: lr.width, BashCap: lr.bashCap, Tick: lr.tick})
	rows := res.Lines

	stable := res.StableRows
	if !allowFlush && stable > lr.flushed {
		stable = lr.flushed
	}
	if stable < lr.flushed {
		stable = lr.flushed // the watermark only rises
	}

	oldFlushed := lr.flushed
	// Freeze rows[oldFlushed:stable] into scrollback. They are the top of
	// the current live region; reprinting them in place (identical) and
	// dropping below leaves the cursor at the new live-region top.
	for i := oldFlushed; i < stable && i < len(rows); i++ {
		io.WriteString(lr.out, term.EraseLine)
		io.WriteString(lr.out, rows[i])
		io.WriteString(lr.out, "\n")
	}

	consumed := stable - oldFlushed
	remOld := lr.live
	if consumed <= len(remOld) {
		remOld = remOld[consumed:]
	} else {
		remOld = nil
	}

	var newLive []string
	if stable < len(rows) {
		newLive = rows[stable:]
	}
	lr.diffPaint(newLive, remOld)

	lr.flushed = stable
	lr.live = newLive
}

// diffPaint line-diffs newLive against remOld (the rows currently on
// screen in the live region), rewriting only the rows that differ,
// clearing leftovers if it shrank, and parking the cursor back at the
// top of the region. Cursor starts and ends at the live-region top.
func (lr *liveRegion) diffPaint(newLive, remOld []string) {
	out := lr.out
	n, o := len(newLive), len(remOld)
	maxN := n
	if o > maxN {
		maxN = o
	}
	if maxN == 0 {
		return
	}
	for i := 0; i < maxN; i++ {
		if i < n {
			if i >= o || newLive[i] != remOld[i] {
				io.WriteString(out, term.EraseLine) // \r + clear
				io.WriteString(out, newLive[i])
			}
		} else {
			io.WriteString(out, term.EraseLine) // shrink: clear leftover row
		}
		if i < maxN-1 {
			if i+1 < o {
				io.WriteString(out, cursorNextExisting) // row exists on screen
			} else {
				io.WriteString(out, cursorNextNew) // create a new row (may scroll)
			}
		}
	}
	if maxN > 1 {
		io.WriteString(out, term.CursorUp(maxN-1))
	}
	io.WriteString(out, "\r")
}

const (
	// cursorNextExisting moves down one existing row, column 0, without
	// scrolling (CSI cursor-down + carriage return).
	cursorNextExisting = "\x1b[1B\r"
	// cursorNextNew creates the next row (carriage return + line feed;
	// scrolls at the viewport bottom).
	cursorNextNew = "\r\n"
	// eraseToEnd clears from the cursor to the end of the screen.
	eraseToEnd = "\x1b[J"
)
