package cli

import (
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// THE POINTER, as a first-class way to address a node.

// clickAt is a left-button PRESS on 0-based screen row `row`. extend is the
// shift-click variant: it moves the focus and leaves the anchor, exactly as
// Shift+^N does.
func (t *transcript) clickAt(row int, extend bool) bool {
	if row < 0 || row >= len(t.frameRefs) {
		return false
	}
	ref := t.frameRefs[row]
	if !ref.valid() {
		return false
	}
	// The second click. Note that it is only the second click on the FOCUS, not
	// on any selected node: with a range selected, clicking a member re-collapses
	// the range onto it (below), and clicking the focus toggles it. That ordering
	// makes a range recoverable with one click instead of trapping the reader in
	// an expansion they did not ask for.
	if !extend && t.selection.active && t.selection.focus.nodeRef == ref {
		return t.toggleExpansionOf(ref)
	}
	// Detach from the tail, as every selection gesture does: a selection the live
	// stream scrolls out from under is not a selection. Unlike selectNode this
	// does NOT then scroll: see the file comment, property 2.
	t.stopFollowing()
	return t.selectRef(ref, extend)
}

// clickable reports whether a left click on this row would do anything. The
// input loop asks before it acts, so a click on chrome does not dismiss a
// search prompt or a panel that the user is still reading.
func (t *transcript) clickable(row int) bool {
	return row >= 0 && row < len(t.frameRefs) && t.frameRefs[row].valid()
}

// selectRef lives in transcript_jump.go: the jump lands on a node the same way
// a click does, and one authority for "put the selection here" is what keeps a
// clicked selection yankable (it carries the endpoint hash the copy path
// verifies) without this file knowing how a hash is taken.

// nodeAt is the livedoc node behind a ref, when the ref names one. The
// inquiry's sentinel ref (inquiryNode) names TEXT ON THE TURN and no node, so
// it correctly answers false: the question has no collapsed form to toggle.
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

// toggleExpansionOf toggles one node, when that node has something to reveal.
// A node with no collapsed form is left alone rather than flipped invisibly:
// flipping a flag that changes no row would make the second click look broken
// in exactly the way a no-op looks correct.
func (t *transcript) toggleExpansionOf(ref nodeRef) bool {
	n, ok := t.nodeAt(ref)
	if !ok || !nodeExpandable(n) {
		return false
	}
	return t.toggleExpansion([]nodeRef{ref})
}

// mouseHelpRows is the pointer's line of the '?' panel: ONE row, in the same
// prose style as the generated ones ("j/k · u/d · gg/G" reads better than a
// mechanical join, and so does this).
func mouseHelpRows() []string {
	const keys = "mouse"
	const text = "click select · click again expand · wheel scroll"
	pad := helpKeyColumn - runewidth.StringWidth(keys)
	if pad < 1 {
		pad = 1
	}
	return []string{"  " + keys + strings.Repeat(" ", pad) + text}
}
