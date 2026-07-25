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
	lt    int
	index int
}

func (r nodeRef) valid() bool { return r.lt != 0 }

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
		for i, n := range m.Nodes {
			refs = append(refs, selectionPoint{
				nodeRef: nodeRef{lt: m.LT, index: i},
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
	appendMessage := func(m aria.Message) {
		for i := range m.Nodes {
			point := selectionPoint{nodeRef: nodeRef{lt: m.LT, index: i}}
			if !pointLess(point, lo) && !pointLess(hi, point) {
				marks[point.nodeRef] = selectionMark{
					selected: true,
					active:   point.nodeRef == t.selection.focus.nodeRef,
				}
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
			(refs[index].lt == t.heldOpen.LT || refs[next].lt == t.heldOpen.LT) {
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
	if len(messages) > 0 && t.selection.focus.lt >= messages[len(messages)/2].LT {
		direction = pageNewer
	}
	anchorLT, within := t.viewportAnchor()
	t.selection = nodeSelection{}
	t.trimPages(direction)
	t.pruneCaches()
	t.lines()
	t.restoreViewportAnchor(anchorLT, within)
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
	if m := t.openMessage(); m != nil && m.LT >= lo.lt && m.LT <= hi.lt {
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

func selectionText(plan selectionCopyPlan, pageSize int, read func(int, int) (aria.AriaRead, error)) (string, error) {
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
	before := plan.hi.lt + 1
	if plan.open != nil && plan.open.LT == plan.hi.lt {
		before = plan.open.LT
	}
	var pages [][]string
	for before > plan.lo.lt {
		r, err := read(before, pageSize)
		if err != nil {
			return "", err
		}
		messages := committedMessages(r.Committed)
		if len(messages) == 0 {
			return "", fmt.Errorf("selection history unavailable before LT %d", before)
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
		before = messages[0].LT
		if before <= plan.lo.lt {
			break
		}
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
	for i, n := range m.Nodes {
		ref := nodeRef{lt: m.LT, index: i}
		var hash uint64
		if ref == plan.lo.nodeRef || ref == plan.hi.nodeRef {
			hash = nodeHash(n)
		}
		if ref == plan.lo.nodeRef {
			if hash != plan.lo.hash {
				return nil, false, false, fmt.Errorf("selection start changed")
			}
			foundLo = true
		}
		if ref == plan.hi.nodeRef {
			if hash != plan.hi.hash {
				return nil, false, false, fmt.Errorf("selection end changed")
			}
			foundHi = true
		}
		point := selectionPoint{nodeRef: ref}
		if !pointLess(point, plan.lo) && !pointLess(plan.hi, point) {
			if text := nodeClipboardText(n); text != "" {
				out = append(out, text)
			}
		}
	}
	return out, foundLo, foundHi, nil
}

func pointLess(a, b selectionPoint) bool {
	return a.lt < b.lt || a.lt == b.lt && a.index < b.index
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
			ref := nodeRef{lt: m.LT, index: i}
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
	for _, ref := range tools {
		if expand {
			t.expanded[ref] = true
		} else {
			delete(t.expanded, ref)
		}
		delete(t.rowCache, ref.lt)
	}
	t.ensureSelectionVisible()
	return true
}

func (t *transcript) ensureSelectionVisible() {
	if !t.selection.active {
		return
	}
	t.lines()
	span, ok := t.nodeRows[t.selection.focus.nodeRef]
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
