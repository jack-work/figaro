package cli

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/mark"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

type nodeRef struct {
	turn  int
	index int
	meta  int
	reply int
}

func metaRefOf(ref nodeRef, i int) nodeRef {
	ref.meta, ref.reply = i, 0
	return ref
}

func blockOf(ref nodeRef) nodeRef {
	ref.meta, ref.reply = 0, 0
	return ref
}

func headOf(ref nodeRef) nodeRef {
	ref.reply = 0
	return ref
}

func replyRefOf(entry nodeRef, k int) nodeRef {
	entry.reply = k
	return entry
}

func (r nodeRef) pseudo() bool { return r.meta > 0 }

// m.From is the positional id of m.Nodes[0] (the wire guarantees Nodes[i].ID ==
// From+i), so a turn delivered in slices yields one ref per node.
func nodeRefAt(m aria.Message, i int) nodeRef {
	return nodeRef{turn: m.Turn, index: int(m.From) + i}
}

// The question is text on the turn and occupies no node index, so it takes the
// inquiry sentinel.
func blockRef(m aria.Message, block int) nodeRef {
	if block == ldrender.BlockInquiry {
		return nodeRef{turn: m.Turn, index: inquiryNode}
	}
	return nodeRefAt(m, block)
}

// Node indices are positional and never negative, so -1 cannot collide, and
// pointLess orders the question ahead of every node of its turn.
const inquiryNode = -1

// Only the slice with From == 0 carries the question; the hash is over a prose
// node bearing it, so the copy path verifies it like a node.
func inquiryPoint(m aria.Message) (selectionPoint, bool) {
	if m.Inquiry == "" {
		return selectionPoint{}, false
	}
	return selectionPoint{
		nodeRef: nodeRef{turn: m.Turn, index: inquiryNode},
		hash:    nodeHash(inquiryNodeOf(m.Inquiry)),
	}, true
}

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
	// held is the window's copy of what the selection covers: a yank of what
	// is on screen touches no wire.
	held []aria.Message
	// expanded is snapshotted: the copier may page history in the background
	// and must not read the live map from another goroutine.
	expanded  map[nodeRef]bool
	metaOpen  map[nodeRef]bool
	meta      metaSource
	entryOpen map[nodeRef]bool
}

type transcriptRow struct {
	text string
	ref  nodeRef
	mark string
	// gutter is the collapsed adornment marker, riding the row for the same reason.
	gutter string
	// spine is re-resolved at paint time because the snake's head follows the
	// selection.
	spine ldrender.SpineSlot
	// chrome: carries the block's ref but none of its content
	// (ldrender.Row.Chrome).
	chrome bool
	more   []ldrender.Spot
}

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

// nodeRefs is every selection stop in reading order: each block, then the rows of
// its open adornment.
func (t *transcript) nodeRefs() []selectionPoint {
	refs := make([]selectionPoint, 0)
	appendMessage := func(m aria.Message) {
		if p, ok := inquiryPoint(m); ok {
			refs = append(refs, p)
		}
		if m.Inquiry != "" {
			refs = append(refs, t.metaPointsOf(m, ldrender.BlockInquiry)...)
		}
		for i, n := range m.Nodes {
			refs = append(refs, selectionPoint{nodeRef: nodeRefAt(m, i), hash: nodeHash(n)})
			refs = append(refs, t.metaPointsOf(m, i)...)
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
		if m.Inquiry != "" {
			for _, p := range t.metaPointsOf(m, ldrender.BlockInquiry) {
				mark(p.nodeRef)
			}
		}
		for i := range m.Nodes {
			mark(nodeRefAt(m, i))
			for _, p := range t.metaPointsOf(m, i) {
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
		// Cold entry seeds from the viewport, not the window, and must not
		// move the page.
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
		// Nothing on screen to seed from: fall back to the ends of the window.
		if delta < 0 {
			index = len(refs) - 1
		} else {
			index = 0
		}
	} else if !cold {
		next := index + delta
		// Clamped at the ends of the window; the older-history fetch is
		// asked for by geometry (wantOlder).
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
		// Seeded from the screen: visible by construction, and
		// ensureSelectionVisible would drag the page to a tall block's
		// tail.
		return
	}
	t.stopFollowing()
	t.ensureSelectionVisible()
}

// clearSelection re-anchors the viewport on the line it was showing.
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
	// The window holds what is on screen: copy it now and the yank needs no wire.
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
		expanded: snapshot(t.expanded), metaOpen: snapshot(t.metaOpen),
		meta: t.metaSource(), entryOpen: snapshot(t.entryOpen),
	}, true
}

// The yank follows the eye: a folded tool yanks its output, an expanded one the
// call and result in full.
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

// anchorAbove: a backward walk at a still has ground to cover before b.
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
	// The window first: the question is text on the turn that only the slice
	// starting it carries.
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
	// Anchored on (turn, node): a turn-granular step lands on the turn before,
	// skipping the head slice that carries the inquiry.
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
			break // no progress
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
	// The hash is checked only at an endpoint; the text is taken whenever the
	// point falls inside the range.
	take := func(ref nodeRef, hash uint64, text string) error {
		if ref == plan.lo.nodeRef || ref == plan.hi.nodeRef {
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
	takeMeta := func(block int) error {
		ref := blockRef(m, block)
		inside := func(p selectionPoint) bool { return p.meta > 0 && blockOf(p.nodeRef) == ref }
		if !plan.metaOpen[ref] && !inside(plan.lo) && !inside(plan.hi) {
			return nil
		}
		for i, e := range plan.meta.entries(m, block) {
			entry := metaRefOf(ref, i+1)
			open := e.kind == metaComment && plan.entryOpen[entry]
			endpoint := plan.lo.nodeRef == entry || plan.hi.nodeRef == entry
			text := e.full()
			if open && (plan.lo.nodeRef != entry || plan.hi.nodeRef != entry) {
				text = ""
			}
			if e.kind == metaField && !endpoint {
				text = ""
			}
			if err := take(entry, e.hash(), text); err != nil {
				return err
			}
			if !open {
				continue
			}
			for k, te := range e.comment.thread {
				if err := take(replyRefOf(entry, k+1), entryHash(e.comment, te), entryText(te)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if p, ok := inquiryPoint(m); ok {
		if err := take(p.nodeRef, p.hash, m.Inquiry); err != nil {
			return nil, false, false, err
		}
		if err := takeMeta(ldrender.BlockInquiry); err != nil {
			return nil, false, false, err
		}
	}
	for i, n := range m.Nodes {
		ref := nodeRefAt(m, i)
		if err := take(ref, nodeHash(n), nodeClipboardText(n, expanded[ref])); err != nil {
			return nil, false, false, err
		}
		if err := takeMeta(i); err != nil {
			return nil, false, false, err
		}
	}
	return out, foundLo, foundHi, nil
}

// Arguments come from the decoded map when it exists, else the streamed prefix, so
// a call still being written yields what has arrived.
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
	if a.meta != b.meta {
		return a.meta < b.meta
	}
	return a.reply < b.reply
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

type foldTarget struct {
	state  map[nodeRef]bool
	ref    nodeRef
	deltas bool
}

func (t *transcript) foldSubjects() (tools, adorned, entries []nodeRef) {
	marks := t.selectionMarks()
	if len(marks) == 0 {
		return nil, nil, nil
	}
	// blockOf answers both cases in one pass.
	blocks := make(map[nodeRef]bool, len(marks))
	for ref, m := range marks {
		if !m.selected {
			continue
		}
		if ref.meta > 0 {
			if ref.reply == 0 {
				entries = append(entries, ref)
			}
			continue
		}
		blocks[blockOf(ref)] = true
	}
	sort.Slice(entries, func(i, j int) bool {
		return pointLess(selectionPoint{nodeRef: entries[i]}, selectionPoint{nodeRef: entries[j]})
	})
	touched := func(ref nodeRef) bool { return blocks[ref] }
	appendMessage := func(m aria.Message) {
		inq := nodeRef{turn: m.Turn, index: inquiryNode}
		if touched(inq) && m.Inquiry != "" {
			adorned = append(adorned, inq)
		}
		if touched(inq) && quoteFolds(m.Inquiry, t.w) {
			tools = append(tools, inq)
		}
		for i, n := range m.Nodes {
			ref := nodeRefAt(m, i)
			if !touched(ref) {
				continue
			}
			if nodeExpandable(n) {
				tools = append(tools, ref)
			}
			if len(t.metaSource().entries(m, i)) > 0 {
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
	return tools, adorned, entries
}

func (t *transcript) toggleSelectedNodes() bool {
	if focus := t.selection.focus.nodeRef; t.selection.active && focus.meta > 0 && t.selection.anchor.nodeRef == focus {
		return t.toggleEntry(headOf(focus))
	}
	tools, adorned, entries := t.foldSubjects()
	return t.toggleFolds(targets(tools, t.expanded, false), targets(adorned, t.metaOpen, true), targets(entries, t.openEntries(), false))
}

func (t *transcript) toggleSelectedTools() bool {
	tools, _, _ := t.foldSubjects()
	return t.toggleFolds(targets(tools, t.expanded, false))
}

func (t *transcript) openEntries() map[nodeRef]bool {
	if t.entryOpen == nil {
		t.entryOpen = map[nodeRef]bool{}
	}
	return t.entryOpen
}

func targets(refs []nodeRef, state map[nodeRef]bool, deltas bool) []foldTarget {
	out := make([]foldTarget, 0, len(refs))
	for _, ref := range refs {
		out = append(out, foldTarget{state: state, ref: ref, deltas: deltas})
	}
	return out
}

func (t *transcript) toggleExpansion(refs []nodeRef) bool {
	return t.toggleFolds(targets(refs, t.expanded, false))
}

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
	t.trustScreen = true
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
	// Expanding grows upward: anchorBelow pins the tail of the change so every
	// node at or after it keeps its screen row.
	if t.follow {
		toggle()
		t.ensureSelectionVisible()
		markFold(targets, open, started)
		return true
	}
	if !t.anchorBelow(targets[len(targets)-1].ref, toggle) {
		// No span on one side of the change: no honest delta to apply.
		t.ensureSelectionVisible()
	}
	markFold(targets, open, started)
	// Not ensureSelectionVisible on the anchored path: revealing the far end
	// of a 200-line expansion is the downward growth this replaced.
	return true
}

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

// A list closed under the cursor collapses the selection onto its block,
// re-derived through selectRef so the hash guard stays honest.
func (t *transcript) repairSelection() {
	if !t.selection.active {
		return
	}
	lost := func(p selectionPoint) bool {
		if p.meta == 0 {
			return false
		}
		if !t.metaOpen[blockOf(p.nodeRef)] {
			return true
		}
		if _, ok := t.entryAt(p.nodeRef); !ok {
			return true
		}
		return p.reply > 0 && !t.entryOpen[headOf(p.nodeRef)]
	}
	if !lost(t.selection.anchor) && !lost(t.selection.focus) {
		return
	}
	focus := t.selection.focus.nodeRef
	if focus.reply > 0 && t.metaOpen[blockOf(focus)] && t.selectRef(headOf(focus), false) {
		return
	}
	t.selectRef(blockOf(focus), false)
}

// The sticky header covers rows, so the floor is the offset plus what the header hides.
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
	if head := t.headRows(); span.first < t.offset+head {
		// One pass: scrolling up can only move the row further from the chrome.
		t.offset = max(span.first-head, 0)
	} else if span.last >= t.offset+body {
		t.offset = span.last - body + 1
	}
}

// Clipped to the pane width and nothing else.
func plainNodeRow(row string, width int) string {
	if width < 1 {
		width = 1
	}
	return clipToWidth(row, width)
}

// A wash and nothing else; a chrome row of a selected block is washed like any other.
func decorateNodeRow(plain string, mark selectionMark, width int) string {
	if !mark.selected && !mark.active {
		return plain
	}
	return washRow(plain, width, term.NodeWash(mark.active))
}

func washRow(plain string, width int, wash string) string {
	if wash == "" {
		return plain
	}
	return wash + washBody(plain, wash) + washFill(plain, width) + "\x1b[0m"
}

// Autowrap is off, so a row reaching the last column leaves the cursor on it and
// \x1b[K would wipe that cell: no erase when the row already reaches the edge.
func washFill(plain string, width int) string {
	if displayWidth(plain) >= width {
		return ""
	}
	return "\x1b[K"
}

// The wash is re-emitted after every reset, so inline styling cannot end it.
func washBody(plain, wash string) string {
	body := plain
	for _, r := range []string{"\x1b[0m", "\x1b[m"} {
		body = strings.ReplaceAll(body, r, r+wash)
	}
	return body
}

// below: every end sits in the prefix a fork shares with its ancestor, where a ref
// means the same node in both.
func (s nodeSelection) below(base int) bool {
	if !s.active {
		return true
	}
	return base > 0 && s.anchor.turn < base && s.focus.turn < base
}
