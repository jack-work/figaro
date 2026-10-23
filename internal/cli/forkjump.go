package cli

import (
	"strings"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk/aria"
)

// forkAt refs the block the fork rides on (the inquiry for a fork on the
// question), not a row of its state.
type forkAt struct {
	ref    nodeRef
	parent string
}

// A fork older than the retained floor is not found; the note says so.
func (t *transcript) forkPoints() []forkAt {
	var out []forkAt
	collect := func(m aria.Message) {
		if p := forkParentOf(m.FormDeltas); p != "" {
			out = append(out, forkAt{ref: nodeRef{turn: m.Turn, index: inquiryNode}, parent: p})
		}
		for i := range m.Nodes {
			if p := forkParentOf(m.Nodes[i].FormDeltas); p != "" {
				out = append(out, forkAt{ref: nodeRefAt(m, i), parent: p})
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

// A lookup, not a survey: this runs per block per frame under the render lock, and
// a bound form has one forked_from.
func forkParentOf(deltas map[string]livedoc.FormDelta) string {
	for key, d := range deltas {
		if d.Kind != livedoc.FormBound || d.Event != livedoc.FormSet {
			continue
		}
		if strings.TrimPrefix(key, d.Form+".") != forkedFromKey {
			continue
		}
		return unquote(d.Value).text
	}
	return ""
}

func pagerForkNext(t *transcript) { t.jumpFork(1) }
func pagerForkPrev(t *transcript) { t.jumpFork(-1) }

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

func pagerAttendFork(t *transcript) {
	if t.attendAria == nil {
		// An unarmed hook must say so, or the key is indistinguishable
		// from an unbound one.
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
			if p.ref == blockOf(focus) {
				t.attendAria(p.parent)
				return
			}
		}
		t.note("no fork point on the selection")
		return
	}
	// No selection: the fork on screen wins, else the nearest.
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

func pagerPitActivate(t *transcript) {
	if t.pit.id == pitQueue {
		if !t.toggleQueuedMessage() {
			t.note("nothing to open on this row")
		}
		return
	}
	if iv, ok := t.pit.live.(itemView); ok {
		if row, has := t.pit.selected(); has && row.id != "" {
			iv.Activate(row.id)
		}
		return
	}
	if row, ok := t.pit.selected(); ok && rpc.ValidateAriaID(row.id) == nil {
		pagerAttendRow(t)
		return
	}
	t.note("nothing to open on this row")
}

func pagerAttendRow(t *transcript) {
	row, ok := t.pit.selected()
	if !ok || row.id == "" {
		return
	}
	if t.attendAria == nil {
		t.note("this session cannot attend (no shell to bind)")
		return
	}
	// A pit row's id is whatever that pit counts by; a view about arias (a
	// notification) says which one.
	id := row.id
	if av, ok := t.pit.live.(interface{ AriaOf(string) string }); ok {
		id = av.AriaOf(row.id)
	}
	if rpc.ValidateAriaID(id) != nil {
		t.note("that row is not an aria")
		return
	}
	t.attendAria(id)
}

func pagerAriaBack(t *transcript)    { t.hopAria(-1) }
func pagerAriaForward(t *transcript) { t.hopAria(1) }

func pagerAttendParent(t *transcript) {
	if t.attendParent == nil {
		t.note("this session cannot attend (no shell to bind)")
		return
	}
	t.attendParent()
}

func (t *transcript) hopAria(dir int) {
	if t.ariaHop == nil {
		t.note("this session has no jumplist (no shell to bind)")
		return
	}
	t.ariaHop(dir)
}

// Browser model, not vim's: visiting from the middle of the list drops what was ahead.
type ariaJumplist struct {
	ids []string
	pos int
}

// Arriving where you already are is not a jump; a hop has already moved pos.
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

// The cursor moves when the switch succeeds (stepTo), not when it is asked for.
func (j *ariaJumplist) peek(dir int) (string, bool) {
	next := j.pos + dir
	if next < 0 || next >= len(j.ids) {
		return "", false
	}
	return j.ids[next], true
}

// Always a new arrival, even next to the cursor: folding it would make the move
// irreversible.
func (j *ariaJumplist) arrive(from, to string) {
	j.visit(from)
	if to == "" {
		return
	}
	j.visit(to)
}

// A hop that lands somewhere not adjacent on the path is recorded as an arrival.
func (j *ariaJumplist) stepTo(to string) {
	if j.pos > 0 && j.ids[j.pos-1] == to {
		j.pos--
		return
	}
	if j.pos+1 < len(j.ids) && j.ids[j.pos+1] == to {
		j.pos++
		return
	}
	j.visit(to)
}

func (j *ariaJumplist) trail() []string {
	if j.pos < 0 || len(j.ids) == 0 {
		return nil
	}
	return append([]string(nil), j.ids[:min(j.pos+1, len(j.ids))]...)
}

// A killed aria must not be somewhere ^O can land.
func (j *ariaJumplist) forget(id string) {
	kept := j.ids[:0]
	moved := 0
	for i, v := range j.ids {
		if v == id {
			if i <= j.pos {
				moved++
			}
			continue
		}
		kept = append(kept, v)
	}
	j.ids = kept
	j.pos = clampInt(j.pos-moved, -1, len(j.ids)-1)
	if len(j.ids) == 0 {
		j.pos = 0
	}
}

func (j *ariaJumplist) where() (int, int) { return j.pos + 1, len(j.ids) }

// Swallows the key: Esc must not clear a selection the reader did not mean to touch.
