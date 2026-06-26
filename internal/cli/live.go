package cli

import (
	"io"
	"strings"

	"github.com/jack-work/figaro/internal/livedoc"
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
	height  int // viewport rows; 0 = unbounded (no overflow flushing)
	bashCap int

	settings renderSettings // display toggles (tool expand, thinking)
	bookend  func() string  // optional status line pinned below the content
	nodes    []livedoc.Node
	tick     uint64

	// Flush watermark. Native scrollback is immutable: once a node's rows are
	// committed they are never re-rendered. The watermark is a NODE index, not
	// a row count, so a verbosity toggle (which changes the rendered height of
	// already-flushed nodes) can never reach back into scrollback. The live
	// region is the render of nodes[flushedNodes:], minus flushedRows already
	// flushed off its top.
	flushedNodes int      // leading nodes fully committed to scrollback
	flushedRows  int      // rows flushed off the top of the first not-yet-final node (viewport overflow)
	live         []string // rows currently shown in the live region
}

func newLiveRegion(out io.Writer, width, bashCap int) *liveRegion {
	return &liveRegion{out: out, width: width, bashCap: bashCap}
}

// setSettings replaces the display toggles and repaints the live tail so a
// live toggle (Ctrl-O / Ctrl-T) takes effect immediately.
func (lr *liveRegion) setSettings(s renderSettings) {
	lr.settings = s
	if lr.nodes != nil || lr.flushedNodes > 0 {
		lr.repaint(true)
	}
}

// snapshot replaces the unit's node list wholesale (unit start or resync)
// and repaints from scratch: any on-screen live rows are cleared first.
func (lr *liveRegion) snapshot(nodes []livedoc.Node) {
	if len(lr.live) > 0 || lr.flushedNodes > 0 {
		io.WriteString(lr.out, eraseToEnd)
	}
	lr.nodes = nodes
	lr.flushedNodes = 0
	lr.flushedRows = 0
	lr.live = nil
	lr.repaint(true)
}

// applyOp folds one node op (open/patch/set) into the unit and repaints.
func (lr *liveRegion) applyOp(op livedoc.Op) {
	lr.nodes = livedoc.ApplyOp(lr.nodes, op)
	lr.repaint(true)
}

// running reports whether any tool node is still running, so the caller
// can run/stop its spinner tick timer.
func (lr *liveRegion) running() bool {
	return nodesRunning(lr.nodes)
}

// tickSpin advances the spinner one frame and repaints (no-op if no tool
// is running).
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
// rows are already final on screen) and reset for the next unit. The
// descent uses real newlines, not CursorDown — at the viewport bottom CUD
// clamps instead of scrolling, which would land the next unit on top of
// the last live row when the conversation has scrolled.
func (lr *liveRegion) commit() {
	if n := len(lr.live); n > 0 {
		io.WriteString(lr.out, strings.Repeat("\n", n))
	}
	lr.nodes = nil
	lr.tick = 0
	lr.flushedNodes = 0
	lr.flushedRows = 0
	lr.live = nil
}

// repaint renders the unflushed node tail, flushes any newly-final nodes (and
// any viewport overflow) to scrollback, and line-diffs the remaining live
// rows. With allowFlush=false the watermark is pinned (used by resize so
// already-committed rows aren't reprinted).
//
// rows is the render of nodes[flushedNodes:]; its first flushedRows are
// already in scrollback (viewport overflow within the first not-yet-final
// node), so the on-screen live region is rows[flushedRows:].
func (lr *liveRegion) repaint(allowFlush bool) {
	rows, stableRows, stableNodes := renderNodesFrom(
		lr.nodes, lr.flushedNodes, lr.flushedNodes > 0,
		lr.width, lr.bashCap, lr.tick, lr.settings)
	// A content-relative bookend: a blank line + status rule pinned just
	// below the rendered content. Always part of the live tail, so it's
	// redrawn as content grows but never flushed to scrollback. It persists
	// as the final line after commit.
	if lr.bookend != nil {
		rows = append(rows, "", clipToWidth(lr.bookend(), lr.width))
	}

	// flush is how many of rows[flushedRows:] to commit to scrollback this
	// frame. Whole newly-final nodes (rows[flushedRows:stableRows]) flush
	// first; that part is reflow-stable by construction.
	flush := 0
	flushedNodes := lr.flushedNodes
	if allowFlush && stableRows > lr.flushedRows {
		flush = stableRows - lr.flushedRows
		flushedNodes += stableNodes
	}

	// Viewport overflow: diffPaint navigates with relative cursor moves that
	// clamp at the viewport edges, so a live region taller than the viewport
	// desyncs the cursor. Flush extra rows off the top — but only rows already
	// on screen identically, so glamour's reflow of the still-streaming tail
	// never re-flushes a changed row. These rows live inside the first
	// not-yet-final node, so they advance flushedRows, not flushedNodes.
	extraRows := 0
	if allowFlush && lr.height > 0 {
		for len(rows)-(lr.flushedRows+flush) > lr.height {
			i := flush // index into the current on-screen live rows
			if i < 0 || i >= len(lr.live) || lr.live[i] != rows[lr.flushedRows+flush] {
				break
			}
			flush++
			extraRows++
		}
	}

	// Freeze rows[flushedRows : flushedRows+flush] into scrollback. They are
	// the top of the current live region; reprinting them in place (identical)
	// and dropping below leaves the cursor at the new live-region top.
	for i := lr.flushedRows; i < lr.flushedRows+flush && i < len(rows); i++ {
		io.WriteString(lr.out, term.EraseLine)
		io.WriteString(lr.out, stabilizeForScrollback(rows[i]))
		io.WriteString(lr.out, "\n")
	}

	remOld := lr.live
	if flush <= len(remOld) {
		remOld = remOld[flush:]
	} else {
		remOld = nil
	}

	// The live region is everything past what we just froze, indexed in THIS
	// frame's rows (old base). Using the post-advance flushedRows here would
	// re-paint a just-flushed node back into the live region for one frame
	// (a transient duplicate the next op then clears).
	consumed := lr.flushedRows + flush
	newLive := rows[consumed:]

	// Advance the watermark. When whole nodes flushed, their render is now in
	// scrollback and the NEXT frame renders from flushedNodes onward, so any
	// rows flushed off the top of the OLD first node are subsumed — reset
	// flushedRows to just the overflow rows still inside the new first node.
	// This is the next-frame base, not an index into this frame's rows.
	newFlushedRows := consumed
	if flushedNodes > lr.flushedNodes {
		newFlushedRows = extraRows
	}
	lr.flushedNodes = flushedNodes
	lr.flushedRows = newFlushedRows

	lr.diffPaint(newLive, remOld)
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
	// autowrap toggles the terminal's auto-margin (DECAWM). The painter
	// drives the cursor explicitly and assumes one logical row per physical
	// line, so it disables auto-wrap while live: a row at/over the viewport
	// width must not wrap onto a second line and desync the cursor math.
	autowrapOff = "\x1b[?7l"
	autowrapOn  = "\x1b[?7h"
	// cursorHide/cursorShow toggle the text cursor so it doesn't sit
	// highlighted on the actively-rendered line during streaming.
	cursorHide = "\x1b[?25l"
	cursorShow = "\x1b[?25h"
)
