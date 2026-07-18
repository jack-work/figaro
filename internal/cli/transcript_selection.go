package cli

import (
	"encoding/json"
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
	anchor nodeRef
	focus  nodeRef
}

type transcriptRow struct {
	text string
	ref  nodeRef
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

func (t *transcript) nodeRefs() []nodeRef {
	refs := make([]nodeRef, 0)
	appendMessage := func(m aria.Message) {
		for i := range m.Nodes {
			refs = append(refs, nodeRef{lt: m.LT, index: i})
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
	refs := t.nodeRefs()
	anchor, focus := -1, -1
	for i, ref := range refs {
		if ref == t.selection.anchor {
			anchor = i
		}
		if ref == t.selection.focus {
			focus = i
		}
	}
	if anchor < 0 || focus < 0 {
		t.selection = nodeSelection{}
		return nil
	}
	if anchor > focus {
		anchor, focus = focus, anchor
	}
	marks := make(map[nodeRef]selectionMark, focus-anchor+1)
	for i := anchor; i <= focus; i++ {
		marks[refs[i]] = selectionMark{selected: true, active: refs[i] == t.selection.focus}
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
			if ref == t.selection.focus {
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
		if len(t.newer) > 0 && t.heldOpen != nil && next >= 0 && next < len(refs) &&
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

func (t *transcript) hasSelection() bool {
	return len(t.selectionMarks()) > 0
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

func (t *transcript) selectedText() (string, bool) {
	marks := t.selectionMarks()
	if len(marks) == 0 {
		return "", false
	}
	var out []string
	appendMessage := func(m aria.Message) {
		for i, n := range m.Nodes {
			if !marks[nodeRef{lt: m.LT, index: i}].selected {
				continue
			}
			if text := nodeClipboardText(n); text != "" {
				out = append(out, text)
			}
		}
	}
	for _, m := range t.messages() {
		appendMessage(m)
	}
	if open := t.openMessage(); open != nil {
		appendMessage(*open)
	}
	return strings.Join(out, "\n\n"), true
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
	span, ok := t.nodeRows[t.selection.focus]
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

func decorateNodeRow(row string, mark selectionMark, width int) string {
	gutter := "  "
	switch {
	case mark.active:
		gutter = term.Cyan("▸ ")
	case mark.selected:
		gutter = term.Cyan("│ ")
	}
	return gutter + clipToWidth(row, width-2)
}
