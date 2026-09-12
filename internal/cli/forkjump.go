package cli

import (
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// Fork points in the pager: finding them, jumping between them (f j / f k),
// attending the aria on the other side of one (a), and the jumplist that
// makes attending reversible (^O / ^I).

// forkAt is one fork point in the retained window: the delta table that
// carries the fork banner, and the aria it names.
type forkAt struct {
	ref    nodeRef
	parent string
}

// forkPoints are every fork banner in the retained window, in reading
// order. The window is what the reader has paged in: a fork older than the
// floor is not found, and the note says so rather than pretending.
func (t *transcript) forkPoints() []forkAt {
	var out []forkAt
	collect := func(m aria.Message) {
		if p := forkParentOf(m.FormDeltas); p != "" {
			out = append(out, forkAt{ref: deltaRefOf(nodeRef{turn: m.Turn, index: inquiryNode}), parent: p})
		}
		for i := range m.Nodes {
			if p := forkParentOf(m.Nodes[i].FormDeltas); p != "" {
				out = append(out, forkAt{ref: deltaRefOf(nodeRefAt(m, i)), parent: p})
			}
		}
	}
	for _, m := range t.messages() {
		collect(m)
	}
	if open := t.openMessage(); open != nil {
		collect(*open)
	}
	return out
}

// forkParentOf is the aria a delta set says this one was forked from, or
// "". One derivation, shared by the renderer and the jump.
func forkParentOf(deltas map[string]livedoc.FormDelta) string {
	if len(deltas) == 0 {
		return ""
	}
	groups, order := groupDeltas(deltas)
	for _, id := range order {
		if p := forkParent(groups[id]); p != "" {
			return p
		}
	}
	return ""
}

// pagerForkPending is the first key of `f j` / `f k`: it arms modeFork,
// exactly as the first 'g' of gg arms pendG. The arming itself happens in
// the dispatcher's epilogue, so this row only has to exist.
func pagerForkPending(t *transcript) {}

func pagerForkNext(t *transcript) { t.jumpFork(1) }
func pagerForkPrev(t *transcript) { t.jumpFork(-1) }

// jumpFork moves the viewport to the next (dir > 0) or previous fork point
// and selects it, so the landing is visibly identified: the same discipline
// as a coordinate jump.
func (t *transcript) jumpFork(dir int) {
	t.settle()
	t.buildIndex()
	points := t.forkPoints()
	if len(points) == 0 {
		t.note("no fork point in view")
		return
	}
	type landing struct {
		line int
		ref  nodeRef
	}
	var lands []landing
	for _, p := range points {
		if span, ok := t.nodeSpanOf(p.ref); ok {
			lands = append(lands, landing{line: span.first, ref: p.ref})
		}
	}
	if len(lands) == 0 {
		t.note("no fork point in view")
		return
	}
	from := t.offset
	if dir > 0 {
		for _, l := range lands {
			if l.line > from {
				t.landJump(l.line, l.ref)
				return
			}
		}
		t.note("no fork point below")
		return
	}
	for i := len(lands) - 1; i >= 0; i-- {
		if lands[i].line < from {
			t.landJump(lands[i].line, lands[i].ref)
			return
		}
	}
	t.note("no fork point above")
}

// pagerAttendFork is 'a' in the transcript: attend the aria the selected
// fork point came from. With no selection it takes the fork point the
// viewport is sitting on, which is where `f j` just left it.
func pagerAttendFork(t *transcript) {
	if t.attendAria == nil {
		// A HOOK THAT IS NOT ARMED SAYS SO. A key that does nothing and
		// reports nothing is indistinguishable from a key that is not
		// bound, which is an hour of a pty session to tell apart.
		t.note("this session cannot attend (no shell to bind)")
		return
	}
	points := t.forkPoints()
	if len(points) == 0 {
		t.note("no fork point in view")
		return
	}
	if t.selection.active {
		focus := t.selection.focus.nodeRef
		for _, p := range points {
			// The table and the block it hangs under are one gesture here:
			// `a` on a node that carries a fork means the fork.
			if p.ref == focus || p.ref == deltaRefOf(focus) {
				t.attendAria(p.parent)
				return
			}
		}
		t.note("no fork point on the selection")
		return
	}
	// NO SELECTION MEANS THE NEAREST FORK. A key that says "attend the
	// fork" and answers "not on screen" is a key that makes the reader
	// scroll for a fact the pager already knows; the one on screen wins,
	// and otherwise the closest one does.
	t.buildIndex()
	top, bottom := t.viewportLines()
	best, bestDist := "", -1
	for _, p := range points {
		span, ok := t.nodeSpanOf(p.ref)
		if !ok {
			continue
		}
		if span.last >= top && span.first < bottom {
			t.attendAria(p.parent)
			return
		}
		d := top - span.last
		if d < 0 {
			d = span.first - bottom
		}
		if bestDist < 0 || d < bestDist {
			best, bestDist = p.parent, d
		}
	}
	if best == "" {
		t.note("no fork point in view")
		return
	}
	t.attendAria(best)
}

// pagerAttendRow is 'a' in a pit: attend the aria the selected row names.
// SELECTABLE MEANS HAS AN ID, and in a list of arias that id is one.
func pagerAttendRow(t *transcript) {
	row, ok := t.pit.selected()
	if !ok || row.id == "" {
		return
	}
	if t.attendAria == nil {
		t.note("this session cannot attend (no shell to bind)")
		return
	}
	// A pit row's id is whatever that pit counts by: a queue row's is a
	// queue number, and attending it would be an error message where a
	// no-op belongs.
	if rpc.ValidateAriaID(row.id) != nil {
		t.note("that row is not an aria")
		return
	}
	t.attendAria(row.id)
}

func pagerAriaBack(t *transcript)    { t.hopAria(-1) }
func pagerAriaForward(t *transcript) { t.hopAria(1) }

func (t *transcript) hopAria(dir int) {
	if t.ariaHop == nil {
		t.note("this session has no jumplist (no shell to bind)")
		return
	}
	t.ariaHop(dir)
}

// ---------------------------------------------------------------------------
// The jumplist.

// ariaJumplist is the reader's path through arias, on the browser's model
// rather than vim's: the list holds every aria visited in order, pos is
// where the reader stands, ^O steps back and ^I steps forward. Visiting a
// new aria from the middle of the list drops what was ahead of it, which is
// what makes "back" mean the same thing twice in a row.
type ariaJumplist struct {
	ids []string
	pos int
}

// visit records arriving at an aria. Arriving where you already are is not
// a jump: that is what makes a hop's own arrival idempotent, since the hop
// has already moved pos.
func (j *ariaJumplist) visit(id string) {
	if id == "" {
		return
	}
	if len(j.ids) > 0 && j.pos < len(j.ids) && j.ids[j.pos] == id {
		return
	}
	j.ids = append(j.ids[:min(j.pos+1, len(j.ids))], id)
	j.pos = len(j.ids) - 1
}

// hop moves the cursor and answers where to go, or false at either end.
func (j *ariaJumplist) hop(dir int) (string, bool) {
	next := j.pos + dir
	if next < 0 || next >= len(j.ids) {
		return "", false
	}
	j.pos = next
	return j.ids[next], true
}

// where is the list as the footer says it: "2/5".
func (j *ariaJumplist) where() (int, int) { return j.pos + 1, len(j.ids) }

// pagerForkCancel is Esc with 'f' armed: the mode is already cleared by the
// dispatcher, so this row exists to swallow the key rather than let Esc
// clear a selection the reader did not mean to touch.
func pagerForkCancel(t *transcript) { t.note("") }
