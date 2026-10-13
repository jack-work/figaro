package cli

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// After every paint the terminal grid must equal t.prev. The reference is an
// unconditional repaint, not naivePaint, which diffs and shares the hole.

// vtFromRows paints every row unconditionally: the ground truth for t.prev.
func vtFromRows(w, h int, rows []string) *vtScreen {
	v := newVT(w, h)
	var b strings.Builder
	for r := 0; r < h; r++ {
		row := ""
		if r < len(rows) {
			row = rows[r]
		}
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K%s", r+1, row)
	}
	_, _ = v.Write([]byte(b.String()))
	return v
}

// resizeGrid changes only the dimensions; scribbleUnknown overwrites the cells.
func (v *vtScreen) resizeGrid(w, h int) {
	cells := make([][]vtCell, h)
	for r := 0; r < h; r++ {
		cells[r] = make([]vtCell, w)
		if r < v.h {
			copy(cells[r], v.cells[r])
		}
	}
	v.cells, v.w, v.h = cells, w, h
	v.top, v.bot = 0, h-1
	if v.row >= h {
		v.row = h - 1
	}
	if v.col >= w {
		v.col = w - 1
	}
}

// scribbleUnknown is the arbitrary prior state the painter must converge from;
// harsher than any real resize, and only valid where t.prev was discarded.
func (v *vtScreen) scribbleUnknown() {
	for r := 0; r < v.h; r++ {
		for c := 0; c < v.w; c++ {
			v.cells[r][c] = vtCell{r: '\u00a4'} // ¤ appears in no rendered row
		}
	}
}

// assertBelief reports text first; the cell grid only when characters agree.
func assertBelief(t *testing.T, vt *vtScreen, prev []string, w, h int, what string) {
	t.Helper()
	want := vtFromRows(w, h, prev)
	wt, gt := want.text(), vt.text()
	wg, gg := want.grid(), vt.grid()
	bad := 0
	for r := range wg {
		if wg[r] == gg[r] {
			continue
		}
		bad++
		if bad > 4 {
			continue
		}
		kind := "STALE TEXT"
		if wt[r] == gt[r] {
			kind = "appearance only (same characters)"
		}
		t.Errorf("%s: row %d [%s]\n  t.prev claims: %q\n  screen shows:  %q", what, r, kind, wt[r], gt[r])
	}
	if bad > 0 {
		t.Fatalf("%s: %d of %d rows disagree with t.prev at %dx%d", what, bad, h, w, h)
	}
}

// Asserted after every frame: a transient glitch self-heals on the next op.
func TestTranscriptPaint_ResizeLeavesNoStaleRows(t *testing.T) {
	for _, regions := range []bool{true, false} {
		name := "scroll-regions"
		if !regions {
			name = "full-repaint-fallback"
		}
		t.Run(name, func(t *testing.T) {
			defer func(v bool) { transcriptScrollRegions = v }(transcriptScrollRegions)
			transcriptScrollRegions = regions

			const w0, h0 = 100, 40
			client := aria.NewClient()
			client.SetClosedLimit(transcriptTailLimit)
			committed := make([]aria.TurnPart, 14)
			for i := range committed {
				committed[i] = aria.TurnPart{Turn: aria.Turn{
					ID: uint64(i + 1), Sealed: true, Nodes: heavyNodes(i+1, 14),
				}}
			}
			client.Apply(aria.Page{Parts: committed}, aria.Notify)

			vt := newVT(w0, h0)
			tr := newTranscript(vt, w0, h0, &ariaView{settings: &renderSettings{}}, client, "aria0001", time.Unix(0, 0))
			tr.enter()

			w, h := w0, h0
			step := func(what string) {
				t.Helper()
				assertBelief(t, vt, tr.prev, w, h, what)
			}
			// The bug lives in rows whose new content is "", so zero
			// blank rows means the fixture no longer exercises it.
			blankRows := func() int {
				n := 0
				for _, row := range tr.prev {
					if visibleText(row) == "" {
						n++
					}
				}
				return n
			}
			// The terminal grid changes before SIGWINCH is delivered.
			resize := func(nw, nh int) {
				vt.resizeGrid(nw, nh)
				vt.scribbleUnknown()
				w, h = nw, nh
				tr.resize(nw, nh)
			}

			step("enter")

			// Message separators put blank rows in the viewport.
			for i := range 30 {
				tr.scrollBy(-1)
				step(fmt.Sprintf("scroll up %d", i))
			}
			if got := blankRows(); got == 0 {
				t.Fatalf("fixture no longer exercises its stated reason: no blank rows in the viewport")
			} else {
				t.Logf("viewport holds %d intentionally-blank rows at %dx%d", got, w, h)
			}

			resize(72, h)
			step("after width shrink 100->72")

			resize(120, h)
			step("after width grow 72->120")

			resize(120, 24)
			step("after height shrink 40->24")

			resize(120, 56)
			step("after height grow 24->56")

			resize(64, 20)
			step("after both 120x56 -> 64x20")

			resize(140, 60)
			step("after both grow 64x20 -> 140x60")

			// Moving away and back must not be what makes the screen
			// correct.
			for i := range 8 {
				tr.scrollBy(-2)
				step(fmt.Sprintf("post-resize scroll up %d", i))
			}
			for i := range 8 {
				tr.scrollBy(2)
				step(fmt.Sprintf("post-resize scroll down %d", i))
			}
		})
	}
}

// No resize anywhere: a failure here is a second bug.
func TestTranscriptPaint_GesturesKeepBelief(t *testing.T) {
	for _, regions := range []bool{true, false} {
		name := "scroll-regions"
		if !regions {
			name = "full-repaint-fallback"
		}
		t.Run(name, func(t *testing.T) {
			defer func(v bool) { transcriptScrollRegions = v }(transcriptScrollRegions)
			transcriptScrollRegions = regions

			const w, h = 100, 40
			client := aria.NewClient()
			client.SetClosedLimit(transcriptTailLimit)
			committed := make([]aria.TurnPart, 16)
			for i := range committed {
				committed[i] = aria.TurnPart{Turn: aria.Turn{
					ID: uint64(i + 1), Sealed: true, Nodes: heavyNodes(i+1, 16),
				}}
			}
			client.Apply(aria.Page{Parts: committed}, aria.Notify)

			vt := newVT(w, h)
			tr := newTranscript(vt, w, h, &ariaView{settings: &renderSettings{}}, client, "aria0001", time.Unix(0, 0))
			tr.enter()
			frames := 0
			step := func(what string) {
				t.Helper()
				frames++
				assertBelief(t, vt, tr.prev, w, h, what)
			}
			defer func() { t.Logf("belief invariant held across %d frames, no resize anywhere", frames) }()
			step("enter")

			tr.key('g')
			tr.key('g')
			step("gg to top")
			tr.key('G')
			step("G to tail")

			// A wheel burst arrives as many reports in one read,
			// coalesced into one frame.
			tr.beginBatch()
			for range 23 {
				tr.scrollBy(-1)
			}
			tr.endBatch()
			step("wheel burst up (batched)")
			tr.beginBatch()
			for range 11 {
				tr.scrollBy(2)
			}
			tr.endBatch()
			step("wheel burst down (batched)")

			for _, n := range []navKey{navPageUp, navPageUp, navUp, navPageDown, navDown, navHome, navEnd} {
				tr.navMotion(n)
				step(fmt.Sprintf("nav %d", n))
			}

			for range 20 {
				tr.scrollBy(-1)
			}
			step("scroll before search")
			tr.find("transcript")
			step("search find")
			for i := range 6 {
				tr.findRepeat(1)
				step(fmt.Sprintf("search next %d", i))
			}
			for i := range 4 {
				tr.findRepeat(-1)
				step(fmt.Sprintf("search prev %d", i))
			}

			// Panels shrink the body without the terminal reflowing
			// underneath.
			tr.openStatusPit()
			tr.render()
			step("status panel open")
			for i := range 4 {
				tr.scrollBy(-3)
				step(fmt.Sprintf("scroll with status panel %d", i))
			}
			tr.pit.close()
			tr.render()
			step("status panel closed")

			tr.openHelpPit()
			tr.render()
			step("help panel open")
			for i := range 4 {
				tr.scrollBy(3)
				step(fmt.Sprintf("scroll with help panel %d", i))
			}
			tr.pit.close()
			tr.render()
			step("help panel closed")

			// queuedRows is the only field the queued panel reads.
			tr.queuedRows = []string{"", "↳ queued messages", "   a queued prompt", "   and another one"}
			tr.showQueuedAuto(true)
			step("queued panel open")
			tr.showQueuedAuto(false)
			step("queued panel closed")

			for i := range 6 {
				tr.key(0x0e) // C-n
				step(fmt.Sprintf("select next %d", i))
			}
			tr.key('\r')
			step("expand selected")
			for i := range 4 {
				tr.scrollBy(-2)
				step(fmt.Sprintf("scroll while expanded %d", i))
			}
			tr.key('\r')
			step("collapse selected")

			tr.key(0x0f) // C-o
			step("verbose on")
			tr.key(0x0f)
			step("verbose off")

			for i := range 80 {
				tr.scrollBy(-1)
				step(fmt.Sprintf("deep climb %d", i))
			}

			// Following and detached take different paths through a
			// live turn.
			tr.key('G') // follow the tail
			step("live: following before stream")
			for n := 1; n <= 10; n++ {
				client.Apply(aria.Page{Parts: []aria.TurnPart{{
					Turn: aria.Turn{ID: 17, Sealed: false, Inquiry: "stream me", Nodes: heavyNodes(17, n)},
				}}}, aria.Notify)
				tr.render()
				step(fmt.Sprintf("live: streaming while following, %d output lines", n))
			}
			for range 12 { // detach and sit in history
				tr.scrollBy(-1)
			}
			step("live: detached")
			for n := 11; n <= 20; n++ {
				client.Apply(aria.Page{Parts: []aria.TurnPart{{
					Turn: aria.Turn{ID: 17, Sealed: false, Inquiry: "stream me", Nodes: heavyNodes(17, n)},
				}}}, aria.Notify)
				tr.render()
				step(fmt.Sprintf("live: streaming while detached, %d output lines", n))
			}
			client.Apply(aria.Page{Parts: []aria.TurnPart{{
				Turn: aria.Turn{ID: 17, Sealed: true, Inquiry: "stream me", Nodes: heavyNodes(17, 20)},
			}}}, aria.Notify)
			tr.render()
			step("live: turn sealed")
			tr.key('G')
			step("live: follow after seal")
		})
	}
}

// An unchanged frame past the resync interval must write nothing.
func TestPeriodicResyncWritesNothingWhenNothingChanged(t *testing.T) {
	ft := ldrender.NewFakeTerminal(40, 8)
	tap := &paintTap{to: ft}
	tr := newTranscript(tap, 40, 8, ldrender.NodeText{}, aria.NewClient(), "aria1234", time.Unix(0, 0))
	clock := time.Unix(0, 0)
	tr.now = func() time.Time { return clock }
	tr.enter()

	screen := make([]string, 8)
	for i := range screen {
		screen[i] = fmt.Sprintf("row %d", i)
	}
	tr.paint(screen)
	tap.reset()
	clock = clock.Add(5 * time.Second)

	tr.paint(append([]string(nil), screen...))
	if n := tap.buf.Len(); n != 0 {
		t.Fatalf("an unchanged frame past the resync interval wrote %d bytes: %q", n, tap.buf.String())
	}

	// The next frame that differs pays the resync in full.
	changed := append([]string(nil), screen...)
	changed[3] = "row three, changed"
	tr.paint(changed)
	painted := tap.buf.String()
	if !strings.Contains(painted, "row three, changed") {
		t.Fatalf("the frame that differs did not paint: %q", painted)
	}
	// Counted by cursor addresses: the painter writes over a row and clears
	// only past its end, so ESC[2K is not the signature of a full repaint.
	rows := 0
	for r := 1; r <= len(screen); r++ {
		if strings.Contains(painted, fmt.Sprintf("\x1b[%d;1H", r)) {
			rows++
		}
	}
	if rows < len(screen)-1 {
		t.Fatalf("the deferred resync painted %d rows in full, want the whole screen: %q", rows, painted)
	}
}

type paintTap struct {
	to  io.Writer
	buf bytes.Buffer
}

func (p *paintTap) Write(b []byte) (int, error) {
	p.buf.Write(b)
	return p.to.Write(b)
}

func (p *paintTap) reset() { p.buf.Reset() }
