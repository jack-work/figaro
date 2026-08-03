package cli

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"strings"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/term"
)

type nodeRef struct {
	turn  int
	index int
}

// nodeRefAt identifies the i'th node OF THE SLICE m by its position within the
// whole TURN. m.From is the positional id of m.Nodes[0] — the wire guarantees
// Nodes[i].ID == From+i — so a turn that reaches the renderer as several slices
// still yields one distinct ref per node.
//
// Using the slice-local index instead COLLIDES. A steered turn arrives as
// {From:0,n=1} {From:1,n=1} {From:2,n=3}, so the inquiry, the steer and the
// first output node would all take the ref {turn,0} — sharing expansion and
// selection state between unrelated nodes. Always mint refs through here.
func nodeRefAt(m aria.Message, i int) nodeRef {
	return nodeRef{turn: m.Turn, index: int(m.From) + i}
}

// inquiryNode is the index a turn's opening question takes. The question is
// TEXT ON THE TURN and occupies no node slot, but selection, copy and the
// Ctrl-O expansion state all key on nodeRef — so it needs one, and it must not
// collide with any node's. Node indices are positional and therefore never
// negative (Nodes[i].ID == From+i), which makes a negative sentinel free of
// collisions by construction rather than by convention; and pointLess then
// orders the question ahead of every node of its own turn, which is exactly
// where every renderer draws it.
const inquiryNode = -1

// inquiryPoint is the selection point of a slice's opening question, or false
// when the slice carries none (only the slice with From == 0 does). The hash is
// taken over a node bearing the question as prose, so the copy path can verify
// the endpoint the same way it verifies a node.
func inquiryPoint(m aria.Message) (selectionPoint, bool) {
	if m.Inquiry == "" {
		return selectionPoint{}, false
	}
	return selectionPoint{
		nodeRef: nodeRef{turn: m.Turn, index: inquiryNode},
		hash:    nodeHash(inquiryNodeOf(m.Inquiry)),
	}, true
}

// inquiryNodeOf is the question as the one livedoc.Node shape everything else
// speaks: what it hashes as, and what it copies as.
func inquiryNodeOf(inquiry string) livedoc.Node {
	return livedoc.Node{Type: livedoc.NodeProse, Markdown: inquiry}
}

func (r nodeRef) valid() bool { return r.turn != 0 }

type nodeSelection struct {
	active bool
	anchor selectionPoint
	focus  selectionPoint
}

type selectionPoint struct {
	nodeRef
	hash uint64
}

type selectionCopyPlan struct {
	lo   selectionPoint
	hi   selectionPoint
	open *aria.Message
}

type transcriptRow struct {
	text string
	ref  nodeRef
}

// searchText is the row's text as the reader sees it. Node rows carry no
// prefix of their own — the selection bar is painted over glamour's margin at
// decoration time, not baked into the stored row — so this is the row.
func (r transcriptRow) searchText() string {
	return r.text
}

type cachedMessage struct {
	rows []transcriptRow
}

type nodeSpan struct {
	first int
	last  int
}

type expandableNodeView interface {
	RenderExpanded(n livedoc.Node, width, tick int, fullOutput bool) []string
}

type selectionMark struct {
	selected bool
	active   bool
}

func (t *transcript) nodeRefs() []selectionPoint {
	refs := make([]selectionPoint, 0)
	appendMessage := func(m aria.Message) {
		if p, ok := inquiryPoint(m); ok {
			refs = append(refs, p)
		}
		for i, n := range m.Nodes {
			refs = append(refs, selectionPoint{
				nodeRef: nodeRefAt(m, i),
				hash:    nodeHash(n),
			})
		}
	}
	for _, m := range t.messages() {
		appendMessage(m)
	}
	if open := t.openMessage(); open != nil {
		appendMessage(*open)
	}
	return refs
}

func (t *transcript) selectionMarks() map[nodeRef]selectionMark {
	if !t.selection.active {
		return nil
	}
	lo, hi := t.selection.anchor, t.selection.focus
	if pointLess(hi, lo) {
		lo, hi = hi, lo
	}
	marks := make(map[nodeRef]selectionMark)
	mark := func(ref nodeRef) {
		point := selectionPoint{nodeRef: ref}
		if !pointLess(point, lo) && !pointLess(hi, point) {
			marks[ref] = selectionMark{
				selected: true,
				active:   ref == t.selection.focus.nodeRef,
			}
		}
	}
	appendMessage := func(m aria.Message) {
		if p, ok := inquiryPoint(m); ok {
			mark(p.nodeRef)
		}
		for i := range m.Nodes {
			mark(nodeRefAt(m, i))
		}
	}
	for _, m := range t.messages() {
		appendMessage(m)
	}
	if open := t.openMessage(); open != nil {
		appendMessage(*open)
	}
	return marks
}

func (t *transcript) selectNode(delta int, extend bool) {
	refs := t.nodeRefs()
	if len(refs) == 0 {
		return
	}
	index := -1
	if t.selection.active {
		for i, ref := range refs {
			if ref.nodeRef == t.selection.focus.nodeRef {
				index = i
				break
			}
		}
	}
	cold := index < 0
	if cold {
		// COLD ENTRY SEEDS FROM THE VIEWPORT, NOT FROM THE WINDOW. The retained
		// window holds far more than the screen shows, so len(refs)-1 was the
		// last node of everything HELD, not the last one VISIBLE — and
		// ensureSelectionVisible then yanked the page to it. Entering a
		// selection must not move the page at all.
		//
		// DETACH FIRST, THEN PICK. stopFollowing settles the tail window (which
		// can re-tune it, moving line space) and re-derives t.offset for the
		// detached geometry, where the live padding row becomes content. A ref
		// chosen against the FOLLOWING geometry can therefore stop being the
		// bottommost visible one a frame later — the same staleness
		// stopFollowing's own comment records for the promoted-by-'k' pager.
		// Picking after the detach means picking against the geometry that will
		// actually be painted.
		t.stopFollowing()
		t.buildIndex()
		if ref, ok := t.viewportSeedRef(delta); ok {
			for i := range refs {
				if refs[i].nodeRef == ref {
					index = i
					break
				}
			}
		}
	}
	if index < 0 {
		// Nothing on screen to seed from (an empty or unbuilt index). Fall back
		// to the ends of the retained window, where this always began.
		if delta < 0 {
			index = len(refs) - 1
		} else {
			index = 0
		}
	} else if !cold {
		next := index + delta
		// Clamped at the ends of the retained window. Moving past the top used to
		// ARM the older-history fetch; the fetch is asked for by geometry now
		// (see wantOlder), and the scroll-into-view below is what brings the
		// viewport close enough to the floor to trigger one.
		if next < 0 {
			next = 0
		} else if next >= len(refs) {
			next = len(refs) - 1
		}
		index = next
	}
	if !extend || !t.selection.active {
		t.selection.anchor = refs[index]
	}
	t.selection.focus = refs[index]
	t.selection.active = true
	if cold {
		// Seeded from what is on screen, so it is visible by construction and
		// already detached. Calling ensureSelectionVisible here is precisely the
		// scroll this exists to remove: a tall block whose head is on screen but
		// whose tail runs off the bottom would drag the page down to it.
		return
	}
	t.stopFollowing()
	t.ensureSelectionVisible()
}

// clearSelection drops the selection and re-anchors the viewport on the line
// it was showing. It used to trim the retained page set in the direction the
// selection had been dragged — the pages are gone, and the store's own
// retention (evictStale) is what bounds memory now.
func (t *transcript) clearSelection() {
	anchor, within := t.viewportAnchor()
	t.selection = nodeSelection{}
	t.pruneCaches()
	t.buildIndex()
	t.restoreViewportAnchor(anchor, within)
}

func (t *transcript) selectionPlan() (selectionCopyPlan, bool) {
	if !t.selection.active {
		return selectionCopyPlan{}, false
	}
	lo, hi := t.selection.anchor, t.selection.focus
	if pointLess(hi, lo) {
		lo, hi = hi, lo
	}
	var open *aria.Message
	if m := t.openMessage(); m != nil && m.Turn >= lo.turn && m.Turn <= hi.turn {
		copy := *m
		copy.Nodes = append([]livedoc.Node(nil), m.Nodes...)
		open = &copy
	}
	return selectionCopyPlan{lo: lo, hi: hi, open: open}, true
}

func nodeClipboardText(n livedoc.Node) string {
	switch n.Type {
	case livedoc.NodeTool:
		if n.Output != "" {
			return n.Output
		}
		if len(n.Args) > 0 {
			if b, err := json.Marshal(n.Args); err == nil {
				return n.Name + " " + string(b)
			}
		}
		if n.Summary != "" {
			return n.Summary
		}
		return n.Name
	default:
		return n.Markdown
	}
}

// anchorAbove reports whether a addresses a position strictly later than b in
// (turn, node) reading order — i.e. whether a backward walk sitting at a still
// has ground to cover before it reaches b.
func anchorAbove(a, b aria.Anchor) bool {
	return a.Turn > b.Turn || a.Turn == b.Turn && a.Node > b.Node
}

func selectionText(plan selectionCopyPlan, pageSize int, read func(aria.Anchor, int) (aria.Page, error)) (string, error) {
	var newest []string
	foundLo, foundHi := false, false
	if plan.open != nil {
		text, lo, hi, err := selectedMessageText(*plan.open, plan)
		if err != nil {
			return "", err
		}
		newest = text
		foundLo, foundHi = lo, hi
	}
	if foundLo && foundHi {
		return strings.Join(newest, "\n\n"), nil
	}
	// The walk is anchored on (turn, NODE), not on the turn alone. A turn too
	// big for one page comes back in slices, and a turn-granular step lands on
	// the turn BEFORE it — skipping every slice below the first, the head slice
	// among them, which is the only one that carries the inquiry.
	at := aria.Anchor{Turn: uint64(plan.hi.turn + 1)}
	if plan.open != nil && plan.open.Turn == plan.hi.turn {
		at = aria.Anchor{Turn: uint64(plan.open.Turn), Node: plan.open.From}
	}
	stop := aria.Anchor{Turn: uint64(plan.lo.turn)}
	if plan.lo.index > 0 {
		stop.Node = uint64(plan.lo.index)
	}
	var pages [][]string
	for anchorAbove(at, stop) {
		r, err := read(at, pageSize)
		if err != nil {
			return "", err
		}
		messages := committedMessages(r)
		if len(messages) == 0 {
			return "", fmt.Errorf("selection history unavailable before turn %d", at.Turn)
		}
		var page []string
		for _, m := range messages {
			text, lo, hi, err := selectedMessageText(m, plan)
			if err != nil {
				return "", err
			}
			page = append(page, text...)
			foundLo = foundLo || lo
			foundHi = foundHi || hi
		}
		pages = append(pages, page)
		next := aria.Anchor{Turn: uint64(messages[0].Turn), Node: messages[0].From}
		if !anchorAbove(at, next) {
			break // the read made no progress; nothing older is coming
		}
		at = next
	}
	if !foundLo || !foundHi {
		return "", fmt.Errorf("selection endpoints unavailable")
	}
	var out []string
	for i := len(pages) - 1; i >= 0; i-- {
		out = append(out, pages[i]...)
	}
	out = append(out, newest...)
	return strings.Join(out, "\n\n"), nil
}

func selectedMessageText(m aria.Message, plan selectionCopyPlan) ([]string, bool, bool, error) {
	var out []string
	foundLo, foundHi := false, false
	// One rule for the question and for every node: the hash is taken only at an
	// endpoint (it is the guard against the selection having moved under us),
	// and the text is taken whenever the point falls inside the range.
	take := func(ref nodeRef, n livedoc.Node, text string) error {
		if ref == plan.lo.nodeRef || ref == plan.hi.nodeRef {
			hash := nodeHash(n)
			if ref == plan.lo.nodeRef {
				if hash != plan.lo.hash {
					return fmt.Errorf("selection start changed")
				}
				foundLo = true
			}
			if ref == plan.hi.nodeRef {
				if hash != plan.hi.hash {
					return fmt.Errorf("selection end changed")
				}
				foundHi = true
			}
		}
		point := selectionPoint{nodeRef: ref}
		if !pointLess(point, plan.lo) && !pointLess(plan.hi, point) && text != "" {
			out = append(out, text)
		}
		return nil
	}
	if p, ok := inquiryPoint(m); ok {
		if err := take(p.nodeRef, inquiryNodeOf(m.Inquiry), m.Inquiry); err != nil {
			return nil, false, false, err
		}
	}
	for i, n := range m.Nodes {
		if err := take(nodeRefAt(m, i), n, nodeClipboardText(n)); err != nil {
			return nil, false, false, err
		}
	}
	return out, foundLo, foundHi, nil
}

func pointLess(a, b selectionPoint) bool {
	return a.turn < b.turn || a.turn == b.turn && a.index < b.index
}

func nodeHash(n livedoc.Node) uint64 {
	h := fnv.New64a()
	var size [8]byte
	write := func(s string) {
		binary.LittleEndian.PutUint64(size[:], uint64(len(s)))
		_, _ = h.Write(size[:])
		_, _ = io.WriteString(h, s)
	}
	write(string(n.Type))
	write(n.Name)
	write(n.Summary)
	write(n.Status)
	write(n.Markdown)
	write(n.Output)
	binary.LittleEndian.PutUint64(size[:], uint64(n.StartedAt))
	_, _ = h.Write(size[:])
	binary.LittleEndian.PutUint64(size[:], uint64(n.FinishedAt))
	_, _ = h.Write(size[:])
	if len(n.Args) > 0 {
		if args, err := json.Marshal(n.Args); err == nil {
			write(string(args))
		}
	}
	return h.Sum64()
}

// toggleSelectedNodes is Enter in the pager: expand (or re-collapse) every
// expandable node inside the selection. It was toggleSelectedTools, and the
// rename is the point — expandability is a property a node reports through
// nodeExpandable, not a synonym for "is a tool", so the gesture widens for free
// as more node kinds grow a collapsed form.
func (t *transcript) toggleSelectedNodes() bool {
	marks := t.selectionMarks()
	if len(marks) == 0 {
		return false
	}
	var refs []nodeRef
	appendMessage := func(m aria.Message) {
		for i, n := range m.Nodes {
			ref := nodeRefAt(m, i)
			if marks[ref].selected && nodeExpandable(n, t.w-2) {
				refs = append(refs, ref)
			}
		}
	}
	for _, m := range t.messages() {
		appendMessage(m)
	}
	if open := t.openMessage(); open != nil {
		appendMessage(*open)
	}
	return t.toggleExpansion(refs)
}

// toggleExpansion flips a set of nodes between their collapsed and expanded
// renders. Shared by Enter (a whole selection) and by a second click (one
// node), so the viewport discipline below is written once.
//
// The set flips as a UNIT: if any member is collapsed, all of them expand;
// otherwise all of them collapse. A per-node flip would make Enter over a mixed
// selection swap which half is open, which reads as noise.
func (t *transcript) toggleExpansion(refs []nodeRef) bool {
	if len(refs) == 0 {
		return false
	}
	expand := false
	for _, ref := range refs {
		if !t.expanded[ref] {
			expand = true
			break
		}
	}
	dirty := make(map[int]struct{}, len(refs))
	toggle := func() {
		for _, ref := range refs {
			if expand {
				t.expanded[ref] = true
			} else {
				delete(t.expanded, ref)
			}
			dirty[ref.turn] = struct{}{}
		}
		t.dropTurnsRows(dirty)
	}
	// EXPANDING GROWS UPWARD. Leaving t.offset alone pins the viewport TOP, and
	// because the offset is an ABSOLUTE line index the new rows shove everything
	// after the expansion down and off the bottom — the half of the screen the
	// reader is actually anchored on. anchorBelow pins the tail of the change
	// instead, so every node at or after it keeps its screen row and the earlier
	// content is what scrolls away. See anchorBelow for why that is the right way
	// round.
	//
	// The anchor is the LAST toggled node: a selection can cover several, and only
	// the content after all of them is guaranteed to hold still.
	//
	// Following is left alone — the viewport is pinned to the bottom there and
	// renderFrame re-derives the offset every frame, so a shift would be
	// overwritten and the bottom is already the correct anchor.
	if t.follow {
		toggle()
		t.ensureSelectionVisible()
		return true
	}
	if !t.anchorBelow(refs[len(refs)-1], toggle) {
		// No span on one side of the change, so there is no honest delta to
		// apply. Keep the old behaviour rather than guess.
		t.ensureSelectionVisible()
	}
	// Deliberately NOT ensureSelectionVisible on the anchored path. The focus is
	// the block that just grew; scrolling to reveal its far end is exactly the
	// downward growth this replaced, and on a 200-line expansion it throws the
	// reader into the middle of the output with the anchor pushed off-screen.
	return true
}

// ensureSelectionVisible scrolls the focused node into the body, if it is not
// already there.
//
// THE BODY HEIGHT COMES FROM layout(). It used to be recomputed here as
// t.h - 1, which is not the same quantity: layout reserves two rows for the
// footer rule and status, another for an open panel's every line, and one more
// for the live padding row while following. So this believed the body was one
// row taller than it is — two while following, more with a panel up — and
// "scroll until span.last is visible" stopped short by exactly that much.
//
// The symptom was a selection that scrolled the page and then wasn't on it.
// Observed in a pty: cold Ctrl-P selected the last node of the window, moved
// the viewport from 1-38/70 to 32-69/70, and painted no cue at all, because
// the node it had just selected sat on line 70. One further keypress revealed
// it. Two expressions for one quantity is what caused this, so there is now
// one.
func (t *transcript) ensureSelectionVisible() {
	if !t.selection.active {
		return
	}
	t.buildIndex()
	span, ok := t.nodeSpanOf(t.selection.focus.nodeRef)
	if !ok {
		return
	}
	body, _ := t.layout(len(t.footLines()))
	if span.first < t.offset {
		t.offset = span.first
	} else if span.last >= t.offset+body {
		t.offset = span.last - body + 1
	}
}

// plainNodeRow is a node row in its undecorated resting state: clipped to the
// pane width, and NOTHING ELSE.
//
// It used to prepend one blank column for the selection bar to stand in, which
// cost the pager a column of text and shifted every node one column right of
// where the inline renderer draws the same node. The bar now stands in
// glamour's own left margin (decorateNodeRow), so the resting row is exactly
// the row the incipit paints — which is the invariant
// TestPagerRowsMatchIncipitRows pins.
//
// This is computed once, when the message's rows are rendered into the row
// cache, rather than per frame: the transcript re-materializes every retained
// row on every keypress. decorateNodeRow then only has to do work for the
// handful of rows that are actually selected.
func plainNodeRow(row string, width int) string {
	if width < 1 {
		width = 1
	}
	return clipToWidth(row, width)
}

// barOverMargin puts the one-column selection bar at the head of a row.
//
// Rows a renderer wraps through glamour arrive with a two-column left margin,
// so the bar REPLACES a blank the row was already spending and the text does
// not move. Rows with no margin to stand in — a tool header ("✓ bash"), the
// steering marker — get the bar INSERTED and the row clipped back to the cells
// it had, so a selected row can never be wider than an unselected one. Either
// way the bar is drawn: selection is never silent.
//
// Leading escapes are copied verbatim, uncounted: glamour opens a row with its
// style, and a bar written before that style would be recoloured by it.
//
// width is the PANE, not the row: a row shorter than the pane has room for the
// bar without giving anything up, and clipping to the row's own width instead
// threw the bar away on an empty row and on one whose first glyph is wide.
func barOverMargin(row, bar string, width int) string {
	if width < 1 {
		width = 1
	}
	i := 0
	for i < len(row) && row[i] == 0x1b {
		i, _ = escapeEnd(row, i)
	}
	if i < len(row) && row[i] == ' ' {
		return row[:i] + bar + row[i+1:] // stand in the margin: same width
	}
	return clipToWidth(row[:i]+bar+row[i:], width)
}

// decorateNodeRow paints a single transcript row with its selection cue. The
// left indicator is one column (down from two): a slim vertical bar for
// selected rows (bright cyan on the focused row, plain cyan on the rest of
// the range) and a single space otherwise. Selected rows also get a subtle
// background wash so the extent of a multi-block selection is visible without
// relying on a wide gutter.
//
// It takes the row in its plainNodeRow form (clipped, and otherwise exactly
// the row the inline renderer paints), so the unselected case — the
// overwhelming majority of rows in any frame — returns it untouched with zero
// allocation. The bar is written INTO the row's own left margin
// (barOverMargin), which is why a selected row is the same width, and starts
// at the same column, as an unselected one.
//
// Background painting has two subtleties. First, any `\x1b[0m` inside the
// row resets ALL SGR — including our background — so every reset in the
// content is re-emitted with the background restored. Second, the paint loop
// erases the line with the terminal's default background BEFORE writing the
// row, so we trail with `\x1b[K` (erase-to-end at the current, i.e. selected,
// background) to extend the wash to the right edge. A final `\x1b[0m` clears
// everything before the next row begins.
func decorateNodeRow(plain string, mark selectionMark, width int) string {
	if !mark.selected && !mark.active {
		return plain
	}
	const (
		reset      = "\x1b[0m"
		bgSelect   = "\x1b[48;5;238m" // subtle dark gray wash (xterm 256-color)
		gutterSel  = "\x1b[36m▎"      // cyan slim bar for range members
		gutterFocs = "\x1b[1;96m▎"    // bright bold cyan bar for focused node
	)
	if !term.Enabled() {
		return barOverMargin(plain, "▎", width)
	}
	gutter := gutterSel
	if mark.active {
		gutter = gutterFocs
	}
	body := plain
	// Re-emit the background after every reset in the body so highlighting
	// survives inline styling (dim, cyan, etc. inside a rendered node).
	//
	// BOTH RESET FORMS. `\x1b[m` is `\x1b[0m` with the parameter omitted, and it
	// clears the wash exactly as thoroughly. glamour v1 only ever emitted the
	// long form, so matching one spelling was enough by accident; v2 emits the
	// short one, and a selected row whose content ended in `\x1b[m` lost its
	// highlight from there to the right edge — the `\x1b[K` then erased with the
	// DEFAULT background. TestGoldenFramesMatchPreSGRAtTheCellLevel caught it at
	// cell 0,45, which is the only reason anybody knows.
	for _, r := range []string{reset, "\x1b[m"} {
		body = strings.ReplaceAll(body, r, r+bgSelect)
	}
	// The bar goes in AFTER that substitution, so its own reset (which ends the
	// bar's colour before the text resumes) is not itself re-washed.
	return bgSelect + barOverMargin(body, gutter+reset+bgSelect, width) + "\x1b[K" + reset
}
