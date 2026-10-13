package cli

import (
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/sdk/aria"
)

// extend is shift-click: it moves the focus and leaves the anchor, as Shift+^N does.
func (t *transcript) clickAt(row int, extend bool) bool {
	if row < 0 || row >= len(t.frameRefs) {
		return false
	}
	ref := t.frameRefs[row]
	if !ref.valid() {
		return false
	}
	// Only a second click on the focus toggles; a click on another member of a
	// range re-collapses the range onto it, so a range is recoverable with one
	// click.
	if !extend && t.selection.active && t.selection.focus.nodeRef == ref {
		return t.toggleExpansionOf(ref)
	}
	// Detach from the tail, as every selection gesture does; unlike selectNode
	// this does not then scroll.
	t.stopFollowing()
	return t.selectRef(ref, extend)
}

// The input loop asks before it acts, so a click on chrome does not dismiss a
// search prompt or a panel.
func (t *transcript) clickable(row int) bool {
	return row >= 0 && row < len(t.frameRefs) && t.frameRefs[row].valid()
}

// selectRef lives in transcript_jump.go: one authority for "put the selection
// here" keeps a clicked selection yankable.

// The inquiry sentinel (inquiryNode) names text on the turn and no node, so it
// answers false.
func (t *transcript) nodeAt(ref nodeRef) (node livedoc.Node, ok bool) {
	find := func(m aria.Message) {
		if ok || m.Turn != ref.turn {
			return
		}
		for i := range m.Nodes {
			if nodeRefAt(m, i) == ref {
				node, ok = m.Nodes[i], true
				return
			}
		}
	}
	for _, m := range t.messages() {
		find(m)
	}
	if open := t.openMessage(); open != nil {
		find(*open)
	}
	return node, ok
}

// A block with nothing to open is left alone rather than flipped invisibly. A
// delta row has no second form: the keys close the list it belongs to (see
// foldSubjects).
func (t *transcript) toggleExpansionOf(ref nodeRef) bool {
	if ref.delta > 0 {
		return false
	}
	var targets []foldTarget
	if d, ok := t.deltasAt(ref); ok && adornRowCount(d, adornLift(ref.index)) > 0 {
		targets = append(targets, foldTarget{state: t.adorned, ref: ref})
	}
	if n, ok := t.nodeAt(ref); ok && nodeExpandable(n) {
		targets = append(targets, foldTarget{state: t.expanded, ref: ref})
	}
	return t.toggleFolds(targets)
}

// deltasAt is a node's deltas, or the turn's when the ref sits on the inquiry.
func (t *transcript) deltasAt(ref nodeRef) (deltas map[string]livedoc.FormDelta, ok bool) {
	find := func(m aria.Message) {
		if ok || m.Turn != ref.turn {
			return
		}
		if ref.index == inquiryNode {
			deltas, ok = m.FormDeltas, len(m.FormDeltas) > 0
			return
		}
		for i := range m.Nodes {
			if nodeRefAt(m, i) == ref {
				deltas, ok = m.Nodes[i].FormDeltas, len(m.Nodes[i].FormDeltas) > 0
				return
			}
		}
	}
	for _, m := range t.messages() {
		find(m)
	}
	if open := t.openMessage(); open != nil {
		find(*open)
	}
	return deltas, ok
}

func mouseHelpRows() []string {
	const keys = "mouse"
	const text = "click select · click again expand · wheel scroll"
	pad := helpKeyColumn - runewidth.StringWidth(keys)
	if pad < 1 {
		pad = 1
	}
	return []string{"  " + keys + strings.Repeat(" ", pad) + text}
}
