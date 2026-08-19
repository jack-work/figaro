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
// whole TURN. m.From is the positional id of m.Nodes[0]: the wire guarantees
// Nodes[i].ID == From+i: so a turn that reaches the renderer as several slices
// still yields one distinct ref per node.
func nodeRefAt(m aria.Message, i int) nodeRef {
	return nodeRef{turn: m.Turn, index: int(m.From) + i}
}

// inquiryNode is the index a turn's opening question takes. The question is
// TEXT ON THE TURN and occupies no node slot, but selection, copy and the
// Ctrl-O expansion state all key on nodeRef: so it needs one, and it must not
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
	// expanded is the fold state at the moment the yank was asked for. The
	// copy follows the EYE: a folded tool yanks its output, an expanded one
	// yanks the call and the result in full. Snapshotted into the plan so the
	// copier: which may page history in the background: never reads the
	// live map from another goroutine.
	expanded map[nodeRef]bool
}

type transcriptRow struct {
	text string
	ref  nodeRef
}

// searchText is the row's text as the reader sees it. Node rows carry no
// prefix of their own: the selection bar is painted over glamour's margin at
// decoration time, not baked into the stored row: so this is the row.
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
	t.wantTop = false // selecting is a deliberate move; see transcript.wantTop
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
		// last node of everything HELD, not the last one VISIBLE, and
		// ensureSelectionVisible then yanked the page to it. Entering a
		// selection must not move the page at all.
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
// selection had been dragged: the pages are gone, and the store's own
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
	expanded := make(map[nodeRef]bool, len(t.expanded))
	for ref, on := range t.expanded {
		if on {
			expanded[ref] = true
		}
	}
	return selectionCopyPlan{lo: lo, hi: hi, open: open, expanded: expanded}, true
}

// nodeClipboardText is what `y` puts on the clipboard for one node. For a
// tool it follows the EYE: what is on screen folded is the output, so that is
// what a fold-state yank gives; an expanded node shows the call and its
// result, so an expanded yank gives both, in full and untruncated. Copying
// something the reader cannot see is how a yank ends up in a commit message
// nobody meant to write.
func nodeClipboardText(n livedoc.Node, expanded bool) string {
	switch n.Type {
	case livedoc.NodeTool:
		if expanded {
			return toolClipboardFull(n)
		}
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
// (turn, node) reading order: i.e. whether a backward walk sitting at a still
// has ground to cover before it reaches b.
func anchorAbove(a, b aria.Anchor) bool {
	return a.Turn > b.Turn || a.Turn == b.Turn && a.Node > b.Node
}

func selectionText(plan selectionCopyPlan, pageSize int, read func(aria.Anchor, int) (aria.Page, error)) (string, error) {
	var newest []string
	foundLo, foundHi := false, false
	if plan.open != nil {
		text, lo, hi, err := selectedMessageText(*plan.open, plan, plan.expanded)
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
	// the turn BEFORE it: skipping every slice below the first, the head slice
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
			text, lo, hi, err := selectedMessageText(m, plan, plan.expanded)
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

func selectedMessageText(m aria.Message, plan selectionCopyPlan, expanded map[nodeRef]bool) ([]string, bool, bool, error) {
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
		ref := nodeRefAt(m, i)
		if err := take(ref, n, nodeClipboardText(n, expanded[ref])); err != nil {
			return nil, false, false, err
		}
	}
	return out, foundLo, foundHi, nil
}

// toolClipboardFull is an expanded tool node's yank: the call, then its
// result, both whole. The arguments come from the decoded map when it exists
// and from the streamed prefix when it does not, so yanking a call that is
// still being written gives what has arrived rather than nothing.
func toolClipboardFull(n livedoc.Node) string {
	var b strings.Builder
	b.WriteString(n.Name)
	for _, f := range toolArgFields(n) {
		fmt.Fprintf(&b, "\n%s: %s", f.Name, f.Value)
	}
	if n.Output != "" {
		b.WriteString("\n\n")
		b.WriteString(n.Output)
	}
	return b.String()
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
// rename is the point: expandability is a property a node reports through
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
			if marks[ref].selected && nodeExpandable(n) {
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
	// after the expansion down and off the bottom: the half of the screen the
	// reader is actually anchored on. anchorBelow pins the tail of the change
	// instead, so every node at or after it keeps its screen row and the earlier
	// content is what scrolls away. See anchorBelow for why that is the right way
	// round.
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
func plainNodeRow(row string, width int) string {
	if width < 1 {
		width = 1
	}
	return clipToWidth(row, width)
}

// barOverMargin puts the one-column selection bar at the head of a row.
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
func decorateNodeRow(plain string, mark selectionMark, width int) string {
	if !mark.selected && !mark.active {
		return plain
	}
	const (
		reset = "\x1b[0m"
		// ONE STEP off the background, not a hue. Kanagawa sumiInk2 #2A2A37
		// (xterm 236): dark enough to disappear into a dark theme and light
		// enough to read as a lift, with the foreground untouched.
		bgSelect   = "\x1b[48;5;236m"
		gutterSel  = "\x1b[36m▎"   // cyan slim bar for range members
		gutterFocs = "\x1b[1;96m▎" // bright bold cyan bar for focused node
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
	for _, r := range []string{reset, "\x1b[m"} {
		body = strings.ReplaceAll(body, r, r+bgSelect)
	}
	// The bar goes in AFTER that substitution, so its own reset (which ends the
	// bar's colour before the text resumes) is not itself re-washed.
	return bgSelect + barOverMargin(body, gutter+reset+bgSelect, width) + "\x1b[K" + reset
}
