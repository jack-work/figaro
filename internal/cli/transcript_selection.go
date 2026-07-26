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

// searchText is the row's content as history search sees it: node rows are
// stored in their plainNodeRow form, so the blank gutter column has to come
// back off before matching.
func (r transcriptRow) searchText() string {
	if r.ref.valid() {
		return strings.TrimPrefix(r.text, " ")
	}
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
	if index < 0 {
		if delta < 0 {
			index = len(refs) - 1
		} else {
			index = 0
		}
	} else {
		next := index + delta
		if t.hasNewerHistory() && t.heldOpen != nil && next >= 0 && next < len(refs) &&
			(refs[index].turn == t.heldOpen.Turn || refs[next].turn == t.heldOpen.Turn) {
			t.checkNewer = true
			return
		}
		switch {
		case next < 0:
			next = 0
			t.checkOlder = true
		case next >= len(refs):
			next = len(refs) - 1
			t.checkNewer = true
		}
		index = next
	}
	if !extend || !t.selection.active {
		t.selection.anchor = refs[index]
	}
	t.selection.focus = refs[index]
	t.selection.active = true
	t.stopFollowing()
	t.ensureSelectionVisible()
}

func (t *transcript) clearSelection() {
	direction := pageOlder
	messages := t.messages()
	if len(messages) > 0 && t.selection.focus.turn >= messages[len(messages)/2].Turn {
		direction = pageNewer
	}
	anchor, within := t.viewportAnchor()
	t.selection = nodeSelection{}
	t.trimPages(direction)
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

func (t *transcript) toggleSelectedTools() bool {
	marks := t.selectionMarks()
	if len(marks) == 0 {
		return false
	}
	var tools []nodeRef
	appendMessage := func(m aria.Message) {
		for i, n := range m.Nodes {
			ref := nodeRefAt(m, i)
			if marks[ref].selected && n.Type == livedoc.NodeTool && n.Output != "" {
				tools = append(tools, ref)
			}
		}
	}
	for _, m := range t.messages() {
		appendMessage(m)
	}
	if open := t.openMessage(); open != nil {
		appendMessage(*open)
	}
	if len(tools) == 0 {
		return false
	}
	expand := false
	for _, ref := range tools {
		if !t.expanded[ref] {
			expand = true
			break
		}
	}
	dirty := make(map[int]struct{}, len(tools))
	for _, ref := range tools {
		if expand {
			t.expanded[ref] = true
		} else {
			delete(t.expanded, ref)
		}
		dirty[ref.turn] = struct{}{}
	}
	t.dropTurnsRows(dirty)
	t.ensureSelectionVisible()
	return true
}

func (t *transcript) ensureSelectionVisible() {
	if !t.selection.active {
		return
	}
	t.buildIndex()
	span, ok := t.nodeSpanOf(t.selection.focus.nodeRef)
	if !ok {
		return
	}
	body := t.h - 1
	if body < 1 {
		body = 1
	}
	if span.first < t.offset {
		t.offset = span.first
	} else if span.last >= t.offset+body {
		t.offset = span.last - body + 1
	}
}

// plainNodeRow is a node row in its undecorated resting state: clipped to
// the gutter width and prefixed with the one blank column that an unselected
// row shows where a selected row shows its bar.
//
// This is computed once, when the message's rows are rendered into the row
// cache, rather than per frame: the transcript re-materializes every retained
// row on every keypress, and " " + clip(row) was the single largest allocator
// left in the frame path. decorateNodeRow then only has to do work for the
// handful of rows that are actually selected.
func plainNodeRow(row string, width int) string {
	if width < 2 {
		width = 2
	}
	return " " + clipToWidth(row, width-1)
}

// decorateNodeRow paints a single transcript row with its selection cue. The
// left indicator is one column (down from two): a slim vertical bar for
// selected rows (bright cyan on the focused row, plain cyan on the rest of
// the range) and a single space otherwise. Selected rows also get a subtle
// background wash so the extent of a multi-block selection is visible without
// relying on a wide gutter.
//
// It takes the row in its plainNodeRow form (already clipped, already carrying
// the blank gutter column), so the unselected case — the overwhelming majority
// of rows in any frame — returns it untouched with zero allocation.
//
// Background painting has two subtleties. First, any `\x1b[0m` inside the
// row resets ALL SGR — including our background — so every reset in the
// content is re-emitted with the background restored. Second, the paint loop
// erases the line with the terminal's default background BEFORE writing the
// row, so we trail with `\x1b[K` (erase-to-end at the current, i.e. selected,
// background) to extend the wash to the right edge. A final `\x1b[0m` clears
// everything before the next row begins.
func decorateNodeRow(plain string, mark selectionMark) string {
	if !mark.selected && !mark.active {
		return plain
	}
	body := strings.TrimPrefix(plain, " ") // drop the blank gutter column
	const (
		reset      = "\x1b[0m"
		bgSelect   = "\x1b[48;5;238m" // subtle dark gray wash (xterm 256-color)
		gutterSel  = "\x1b[36m▎"      // cyan slim bar for range members
		gutterFocs = "\x1b[1;96m▎"    // bright bold cyan bar for focused node
	)
	if !term.Enabled() {
		if mark.active {
			return "▎" + body
		}
		return "▎" + body
	}
	gutter := gutterSel
	if mark.active {
		gutter = gutterFocs
	}
	// Re-emit the background after every reset in the body so highlighting
	// survives inline styling (dim, cyan, etc. inside a rendered node).
	body = strings.ReplaceAll(body, reset, reset+bgSelect)
	return bgSelect + gutter + reset + bgSelect + body + "\x1b[K" + reset
}
