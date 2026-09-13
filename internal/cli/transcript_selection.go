package cli

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/mark"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
)

type nodeRef struct {
	turn  int
	index int
	// delta addresses ONE ROW of the form-delta adornment drawn beside the
	// block at (turn, index) rather than the block itself: 0 is the block,
	// n the n'th delta. It is a separate axis instead of another sentinel
	// index because a delta row must sort immediately AFTER the block it
	// explains, which a negative sentinel (see inquiryNode) cannot do, and
	// because the axis cannot collide with a node index by construction.
	delta int
}

// deltaRefOf is the i'th delta row of a block, 1-based. Every surface that
// addresses a pseudonode goes through this, so the axis has one name.
func deltaRefOf(ref nodeRef, i int) nodeRef {
	ref.delta = i
	return ref
}

// blockOf is the block a ref belongs to: itself, or the block a delta row
// adorns. d, t and Enter all act on the block, wherever the cursor stands
// inside its list.
func blockOf(ref nodeRef) nodeRef {
	ref.delta = 0
	return ref
}

// nodeRefAt identifies the i'th node OF THE SLICE m by its position within the
// whole TURN. m.From is the positional id of m.Nodes[0]: the wire guarantees
// Nodes[i].ID == From+i: so a turn that reaches the renderer as several slices
// still yields one distinct ref per node.
func nodeRefAt(m aria.Message, i int) nodeRef {
	return nodeRef{turn: m.Turn, index: int(m.From) + i}
}

// blockRef is the ref of a composed row's block coordinate: a node's, or
// the sentinel the turn's opening question takes. The question is TEXT ON
// THE TURN and occupies no node index, so it needs a ref of its own: that
// is what makes it select, copy and highlight exactly as a node does,
// which is how it behaved when it WAS one.
func blockRef(m aria.Message, block int) nodeRef {
	if block == ldrender.BlockInquiry {
		return nodeRef{turn: m.Turn, index: inquiryNode}
	}
	return nodeRefAt(m, block)
}

// inquiryNode is the index a turn's opening question takes. The question is
// TEXT ON THE TURN and occupies no node slot, but selection, copy and the
// M-m expansion state all key on nodeRef: so it needs one, and it must not
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

// deltaPoints are the selection points of a block's form-delta adornment:
// one per row, in the order the rows draw. Each is a PSEUDONODE: it occupies
// no node slot, selects and yanks like one, and hashes over its own row so
// an endpoint notices when the state under it changed. A closed adornment
// draws no rows and so offers no points: there is nothing on screen to
// select.
func deltaPoints(ref nodeRef, deltas map[string]livedoc.FormDelta, open bool) []selectionPoint {
	if len(deltas) == 0 || !open {
		return nil
	}
	rows := deltaRows(deltas, adornLift(ref.index))
	out := make([]selectionPoint, 0, len(rows))
	for i, r := range rows {
		out = append(out, selectionPoint{
			nodeRef: deltaRefOf(ref, i+1),
			hash:    nodeHash(deltaNodeOf(deltaRowFull(r))),
		})
	}
	return out
}

// deltaNodeOf is one delta row as the one livedoc.Node shape everything else
// speaks. WHOLE: the identity of a row is what it says, not how much of it
// today's terminal had room for, and a row clipped to some nominal width
// hashed and copied the same as one that differed past that column.
func deltaNodeOf(row string) livedoc.Node {
	return livedoc.Node{Type: livedoc.NodeProse, Markdown: row}
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
	// held is the retained window's copy of everything the selection covers.
	// A yank of what is on screen is answered from it, so the common case
	// touches no wire and cannot fail on a page that comes back clipped.
	held []aria.Message
	// expanded is the fold state at the moment the yank was asked for. The
	// copy follows the EYE: a folded tool yanks its output, an expanded one
	// yanks the call and the result in full. Snapshotted into the plan so the
	// copier: which may page history in the background: never reads the
	// live map from another goroutine.
	expanded map[nodeRef]bool
	// adorned is the same snapshot for the delta lists: a closed list is not
	// on screen, so a selection that spans it copies the blocks and not the
	// state behind them.
	adorned map[nodeRef]bool
}

type transcriptRow struct {
	text string
	ref  nodeRef
	// mark is the block's address, carried by its first row and drawn against
	// the right edge only while ^O is on. It rides the row rather than taking
	// one of its own, so toggling it moves nothing and invalidates no cache.
	mark string
	// gutter is the glyph the row wears in the right gutter: a block's
	// collapsed adornment marker. It rides the row for the same reason the
	// mark does.
	gutter string
	// spine is the row's place in its block's adornment snake, re-resolved at
	// paint time because the snake's head follows the selection.
	spine ldrender.SpineSlot
	// chrome marks a row that carries its block's ref but none of its
	// content, so the selection cue skips it. See ldrender.Row.Chrome.
	chrome bool
}

// barColumnFree reports whether the selection bar may take this row's first
// column. A delta row says no: the snake is its cue, and its head says what
// the bar could not, which of the rows in range is the focused one. So does
// any row whose snake already stands in that column.
func (r transcriptRow) barColumnFree() bool {
	switch r.spine.Kind {
	case ldrender.SpineNone:
		return true
	case ldrender.SpineRow:
		return false
	default:
		return r.spine.Col > 0
	}
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

// nodeRefs is every selection stop in the retained window, in reading
// order: each block, and immediately after it the rows of its adornment
// while the adornment is open. Walking with ^N/^P therefore enters an open
// list from above at its first row and from below at its last, and steps out
// of it onto the block, where d, t and Enter act.
func (t *transcript) nodeRefs() []selectionPoint {
	refs := make([]selectionPoint, 0)
	appendMessage := func(m aria.Message) {
		if p, ok := inquiryPoint(m); ok {
			refs = append(refs, p)
		}
		inq := nodeRef{turn: m.Turn, index: inquiryNode}
		refs = append(refs, deltaPoints(inq, m.FormDeltas, t.adorned[inq])...)
		for i, n := range m.Nodes {
			ref := nodeRefAt(m, i)
			refs = append(refs, selectionPoint{nodeRef: ref, hash: nodeHash(n)})
			refs = append(refs, deltaPoints(ref, n.FormDeltas, t.adorned[ref])...)
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
		inq := nodeRef{turn: m.Turn, index: inquiryNode}
		for _, p := range deltaPoints(inq, m.FormDeltas, t.adorned[inq]) {
			mark(p.nodeRef)
		}
		for i := range m.Nodes {
			ref := nodeRefAt(m, i)
			mark(ref)
			for _, p := range deltaPoints(ref, m.Nodes[i].FormDeltas, t.adorned[ref]) {
				mark(p.nodeRef)
			}
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
	// What the reader selected is what the reader can see, so the window holds
	// it: take a copy now and the copy needs no wire at all. Only a selection
	// dragged past what is retained falls back to reading pages.
	var held []aria.Message
	t.forEachMessage(func(m aria.Message) {
		if m.Turn < lo.turn || m.Turn > hi.turn {
			return
		}
		m.Nodes = append([]livedoc.Node(nil), m.Nodes...)
		held = append(held, m)
	})
	snapshot := func(src map[nodeRef]bool) map[nodeRef]bool {
		out := make(map[nodeRef]bool, len(src))
		for ref, on := range src {
			if on {
				out[ref] = true
			}
		}
		return out
	}
	return selectionCopyPlan{
		lo: lo, hi: hi, open: open, held: held,
		expanded: snapshot(t.expanded), adorned: snapshot(t.adorned),
	}, true
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
	// The window first: a selection the reader is looking at is already here,
	// and a question is the case that proves it, being text on the turn that
	// only the slice starting it carries.
	var fromWindow []string
	for _, m := range plan.held {
		text, lo, hi, err := selectedMessageText(m, plan, plan.expanded)
		if err != nil {
			return "", err
		}
		fromWindow = append(fromWindow, text...)
		foundLo, foundHi = foundLo || lo, foundHi || hi
	}
	if foundLo && foundHi {
		return strings.Join(append(fromWindow, newest...), "\n\n"), nil
	}
	newest = append(fromWindow, newest...)
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
		messages := pageMessages(r)
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
	takeDeltas := func(ref nodeRef, deltas map[string]livedoc.FormDelta) error {
		if len(deltas) == 0 || !plan.adorned[ref] {
			return nil
		}
		for i, r := range deltaRows(deltas, adornLift(ref.index)) {
			full := deltaRowFull(r)
			if err := take(deltaRefOf(ref, i+1), deltaNodeOf(full), full); err != nil {
				return err
			}
		}
		return nil
	}
	if err := takeDeltas(nodeRef{turn: m.Turn, index: inquiryNode}, m.FormDeltas); err != nil {
		return nil, false, false, err
	}
	for i, n := range m.Nodes {
		ref := nodeRefAt(m, i)
		if err := take(ref, n, nodeClipboardText(n, expanded[ref])); err != nil {
			return nil, false, false, err
		}
		if err := takeDeltas(ref, n.FormDeltas); err != nil {
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
	if a.turn != b.turn {
		return a.turn < b.turn
	}
	if a.index != b.index {
		return a.index < b.index
	}
	// A block's delta rows come after the block they explain, in order.
	return a.delta < b.delta
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

// FOLD GESTURES. Three keys, one subject: the BLOCK. `t` opens a tool's
// body, `d` opens the form-delta list beside a block, Enter opens both, and
// each is inert where it has nothing to show. A cursor standing on a delta
// row is a cursor on that block's state, so the gestures reach the block
// from inside its own list, which is what makes the list escapable with the
// keys that opened it.

// foldTarget is one thing a gesture flips: a ref, the map that holds its fold
// state, and which of the two it is, so a gesture can say what it did.
type foldTarget struct {
	state  map[nodeRef]bool
	ref    nodeRef
	deltas bool
}

// foldSubjects are the blocks under the selection: every block inside the
// range, plus the parent of any selected delta row. tools are the ones with
// a body to open, adorned the ones with state to show.
func (t *transcript) foldSubjects() (tools, adorned []nodeRef) {
	marks := t.selectionMarks()
	if len(marks) == 0 {
		return nil, nil
	}
	// A block counts as selected when it is selected itself or when a delta
	// row of its own is: blockOf answers both in one pass, where a scan per
	// candidate was the selection size times the window.
	blocks := make(map[nodeRef]bool, len(marks))
	for ref, m := range marks {
		if m.selected {
			blocks[blockOf(ref)] = true
		}
	}
	touched := func(ref nodeRef) bool { return blocks[ref] }
	appendMessage := func(m aria.Message) {
		inq := nodeRef{turn: m.Turn, index: inquiryNode}
		if len(m.FormDeltas) > 0 && touched(inq) && adornRowCount(m.FormDeltas, adornLift(inq.index)) > 0 {
			adorned = append(adorned, inq)
		}
		for i, n := range m.Nodes {
			ref := nodeRefAt(m, i)
			if !touched(ref) {
				continue
			}
			if nodeExpandable(n) {
				tools = append(tools, ref)
			}
			if len(n.FormDeltas) > 0 && adornRowCount(n.FormDeltas, adornLift(ref.index)) > 0 {
				adorned = append(adorned, ref)
			}
		}
	}
	for _, m := range t.messages() {
		appendMessage(m)
	}
	if open := t.openMessage(); open != nil {
		appendMessage(*open)
	}
	return tools, adorned
}

// toggleSelectedNodes is Enter: open (or re-close) everything the selection
// covers, body and state together. It was toggleSelectedTools, and the
// rename is the point: what a block can reveal is a property it reports, not
// a synonym for "is a tool".
func (t *transcript) toggleSelectedNodes() bool {
	tools, adorned := t.foldSubjects()
	return t.toggleFolds(targets(tools, t.expanded, false), targets(adorned, t.adorned, true))
}

// toggleSelectedTools is `t`: bodies only, and only where there is a body.
func (t *transcript) toggleSelectedTools() bool {
	tools, _ := t.foldSubjects()
	return t.toggleFolds(targets(tools, t.expanded, false))
}

// toggleSelectedAdornments is `d`: the form-delta lists only, and only where
// there is state to list.
func (t *transcript) toggleSelectedAdornments() bool {
	_, adorned := t.foldSubjects()
	return t.toggleFolds(targets(adorned, t.adorned, true))
}

func targets(refs []nodeRef, state map[nodeRef]bool, deltas bool) []foldTarget {
	out := make([]foldTarget, 0, len(refs))
	for _, ref := range refs {
		out = append(out, foldTarget{state: state, ref: ref, deltas: deltas})
	}
	return out
}

// toggleExpansion is one node's body, for the mouse: a second click on a
// block opens it.
func (t *transcript) toggleExpansion(refs []nodeRef) bool {
	return t.toggleFolds(targets(refs, t.expanded, false))
}

// toggleFolds flips sets of blocks between their collapsed and open renders.
// Shared by Enter, d, t and a second click, so the viewport discipline below
// is written once.
func (t *transcript) toggleFolds(sets ...[]foldTarget) bool {
	var targets []foldTarget
	for _, set := range sets {
		targets = append(targets, set...)
	}
	if len(targets) == 0 {
		return false
	}
	started := mark.Now()
	open := false
	for _, g := range targets {
		if !g.state[g.ref] {
			open = true
			break
		}
	}
	dirty := make(map[int]struct{}, len(targets))
	toggle := func() {
		for _, g := range targets {
			if open {
				g.state[g.ref] = true
			} else {
				delete(g.state, g.ref)
			}
			dirty[g.ref.turn] = struct{}{}
		}
		t.dropTurnsRows(dirty)
		t.repairSelection()
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
		markFold(targets, open, started)
		return true
	}
	if !t.anchorBelow(targets[len(targets)-1].ref, toggle) {
		// No span on one side of the change, so there is no honest delta to
		// apply. Keep the old behaviour rather than guess.
		t.ensureSelectionVisible()
	}
	markFold(targets, open, started)
	// Deliberately NOT ensureSelectionVisible on the anchored path. The focus is
	// the block that just grew; scrolling to reveal its far end is exactly the
	// downward growth this replaced, and on a 200-line expansion it throws the
	// reader into the middle of the output with the anchor pushed off-screen.
	return true
}

// markFold records what a fold gesture did and what it cost: the rows it
// invalidated are recomposed on the next frame, so this is the keystroke half
// of the span the frame mark closes.
func markFold(targets []foldTarget, open bool, started time.Time) {
	if !mark.Enabled() {
		return
	}
	deltas, bodies := 0, 0
	for _, g := range targets {
		if g.deltas {
			deltas++
		} else {
			bodies++
		}
	}
	mark.Mark("fold", "deltas", deltas, "bodies", bodies, "open", open,
		"ms", float64(time.Since(started).Microseconds())/1000)
}

// repairSelection brings a selection back onto a row that still exists. A
// list closed under the cursor leaves the cursor addressing a pseudonode
// that draws nothing: the block it adorned is where the reader is, and is
// where d pressed again re-opens the list. The selection collapses onto that
// block rather than keeping an endpoint nobody can see, and it re-derives the
// point through selectRef so the copier's hash guard stays honest.
func (t *transcript) repairSelection() {
	if !t.selection.active {
		return
	}
	lost := func(p selectionPoint) bool {
		return p.delta > 0 && !t.adorned[blockOf(p.nodeRef)]
	}
	if !lost(t.selection.anchor) && !lost(t.selection.focus) {
		return
	}
	t.selectRef(blockOf(t.selection.focus.nodeRef), false)
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

// selBg is the selection wash: ONE STEP off the background, not a hue.
// Kanagawa sumiInk2 #2A2A37 (xterm 236) is dark enough to disappear into a
// dark theme and light enough to read as a lift, with the foreground
// untouched.
const selBg = "\x1b[48;5;236m"

// decorateNodeRow paints a single transcript row with its selection cue. The
// left indicator is one column (down from two): a slim vertical bar for
// selected rows (bright cyan on the focused row, plain cyan on the rest of
// the range) and a single space otherwise. Selected rows also get a subtle
// background wash so the extent of a multi-block selection is visible without
// relying on a wide gutter.
//
// bar is false for a row whose left margin is already spoken for: a delta
// row's snake stands in that column, and the snake IS the cue there, its head
// marking the focus the bar could only say was somewhere in range. The wash
// still runs, so the extent of a selection reads the same.
func decorateNodeRow(plain string, mark selectionMark, width int, bar bool) string {
	if !mark.selected && !mark.active {
		return plain
	}
	if !bar {
		return washRow(plain, width)
	}
	const (
		reset      = "\x1b[0m"
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
	// The bar goes in AFTER the wash substitution, so its own reset (which
	// ends the bar's colour before the text resumes) is not itself re-washed.
	body := washBody(plain)
	return selBg + barOverMargin(body, gutter+reset+selBg, width) + washFill(plain, width) + reset
}

// washRow is the wash with no left cue: the lift alone, carried to the right
// edge like a decorated row.
func washRow(plain string, width int) string {
	if !term.Enabled() {
		return plain
	}
	return selBg + washBody(plain) + washFill(plain, width) + "\x1b[0m"
}

// washFill carries the wash from the end of the row to the right edge, and
// draws NOTHING when the row already reaches it.
//
// THE ERASE WOULD WIPE THE LAST COLUMN. The pager runs with autowrap off, so
// writing the pane's last column leaves the cursor standing ON it, and an
// erase-to-end-of-line then clears the cell just written: the adornment's
// gutter glyph, or the last character of a row that happens to fill the pane.
// appendRowUpdate learned this from a tmux replay and erases before it writes;
// a wash cannot do that, so it asks first.
func washFill(plain string, width int) string {
	if displayWidth(plain) >= width {
		return ""
	}
	return "\x1b[K"
}

// washBody re-emits the wash after every reset the row carries, so
// highlighting survives inline styling (dim, cyan, a glyph in the margin)
// instead of ending at the first reset inside the row.
func washBody(plain string) string {
	body := plain
	for _, r := range []string{"\x1b[0m", "\x1b[m"} {
		body = strings.ReplaceAll(body, r, r+selBg)
	}
	return body
}

// below reports whether every end of the selection sits in the turns below
// base: the prefix a fork shares with its ancestor, where a node reference
// means the same node in both arias. An inactive selection is below
// everything, because there is nothing to point anywhere.
func (s nodeSelection) below(base int) bool {
	if !s.active {
		return true
	}
	return base > 0 && s.anchor.turn < base && s.focus.turn < base
}
