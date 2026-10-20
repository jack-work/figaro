package cli

import (
	"encoding/json"
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

type metaKind uint8

const (
	metaField metaKind = iota
	metaDelta
	metaComment
)

type metaEntry struct {
	kind    metaKind
	key     string
	value   string
	other   string
	delta   deltaRow
	comment comment
}

func (e metaEntry) full() string {
	switch e.kind {
	case metaDelta:
		return deltaRowFull(e.delta)
	case metaComment:
		return commentText(e.comment)
	}
	return e.key + "  " + e.value
}

func (e metaEntry) hash() uint64 {
	text := e.full()
	if e.kind == metaComment {
		text = e.comment.id + "\x00" + text
	}
	return nodeHash(livedoc.Node{Type: livedoc.NodeProse, Markdown: text})
}

type metaSource struct {
	comments *commentIndex
	layout   string
}

func (s metaSource) entries(m aria.Message, block int) []metaEntry {
	var out []metaEntry
	ref := blockRef(m, block)
	if block == ldrender.BlockInquiry {
		out = append(out, metaEntry{key: "address", value: coordLabel(m.Turn, inquiryNode, 0, s.layout)})
		for _, seg := range m.InquirySegments {
			if seg.Sender != "" {
				out = append(out, senderEntry(seg.Sender, seg.Name))
			}
		}
		out = append(out, deltaEntries(m.FormDeltas, true)...)
	} else if block >= 0 && block < len(m.Nodes) {
		n := m.Nodes[block]
		out = append(out, nodeFields(n, ref, s.layout)...)
		out = append(out, deltaEntries(n.FormDeltas, false)...)
	}
	if s.comments != nil {
		for _, ci := range s.comments.at(m, block) {
			out = append(out, metaEntry{kind: metaComment, comment: s.comments.list[ci]})
		}
	}
	return out
}

func nodeFields(n livedoc.Node, ref nodeRef, layout string) []metaEntry {
	out := []metaEntry{{key: "address", value: coordLabel(ref.turn, ref.index, nodeCoordAt(n), layout)}}
	switch n.Type {
	case livedoc.NodeSteering:
		if n.Sender != "" {
			out = append(out, senderEntry(n.Sender, n.SenderName))
		}
	case livedoc.NodeTool:
		out = append(out, metaEntry{key: "tool", value: n.Name})
		for _, f := range toolArgFields(n) {
			out = append(out, metaEntry{key: f.Name, value: f.Value})
		}
		if n.Status != "" {
			out = append(out, metaEntry{key: "status", value: n.Status})
		}
		if n.StartedAt != 0 {
			out = append(out, metaEntry{key: "took", value: toolElapsed(n)})
			out = append(out, metaEntry{key: "started", value: formatToolTime(n.StartedAt)})
		}
		if n.FinishedAt != 0 {
			out = append(out, metaEntry{key: "finished", value: formatToolTime(n.FinishedAt)})
		}
		if n.ToolCallID != "" {
			out = append(out, metaEntry{key: "call", value: n.ToolCallID})
		}
	}
	return out
}

func senderEntry(sender, name string) metaEntry {
	return metaEntry{key: "from", value: senderLabel(sender, name), other: senderOther(sender, name)}
}

func deltaEntries(deltas map[string]livedoc.FormDelta, lift bool) []metaEntry {
	rows := deltaRows(deltas, lift)
	out := make([]metaEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, metaEntry{kind: metaDelta, delta: r})
	}
	return out
}

func metaPoints(ref nodeRef, entries []metaEntry, entryOpen map[nodeRef]bool) []selectionPoint {
	out := make([]selectionPoint, 0, len(entries))
	for i, e := range entries {
		entry := metaRefOf(ref, i+1)
		out = append(out, selectionPoint{nodeRef: entry, hash: e.hash()})
		if e.kind != metaComment || !entryOpen[entry] {
			continue
		}
		for k, te := range e.comment.thread {
			out = append(out, selectionPoint{nodeRef: replyRefOf(entry, k+1), hash: entryHash(e.comment, te)})
		}
	}
	return out
}

const (
	metaHead    = headGlyph
	deltaHead   = deltaGlyph
	commentHead = commentGlyph
)

func (e metaEntry) glyph() string {
	switch e.kind {
	case metaDelta:
		return deltaHead
	case metaComment:
		return commentHead
	}
	return metaHead
}

func (e metaEntry) paint(s string) string {
	switch e.kind {
	case metaDelta:
		return term.StateDim(s)
	case metaComment:
		return s
	}
	return term.Label(s)
}

func (e metaEntry) kindName() string {
	switch e.kind {
	case metaDelta:
		return "delta"
	case metaComment:
		return "comment"
	}
	return "meta"
}

func (e metaEntry) otherSpelling() string {
	if e.kind == metaDelta && e.other == "" {
		return cellOther(e.delta.to)
	}
	return e.other
}

func entryLabel(e metaEntry, first bool) string {
	var parts []string
	if first {
		parts = append(parts, e.kindName())
	}
	if other := e.otherSpelling(); other != "" {
		parts = append(parts, other)
	}
	return strings.Join(parts, " · ")
}

func cellOther(c deltaCell) string {
	if !c.present {
		return ""
	}
	return otherName(c.text)
}

func namedCell(c deltaCell) deltaCell {
	if c.present && rpc.IsHexID(c.text) && len(strings.TrimPrefix(c.text, "@")) >= rpc.OldIDLen {
		c.text = ariaName(c.text)
	}
	return c
}

func metaKeyWidth(entries []metaEntry) int {
	w := 0
	for _, e := range entries {
		var k string
		switch e.kind {
		case metaField:
			k = e.key
		case metaDelta:
			if e.delta.banner {
				continue
			}
			k = e.delta.key
		default:
			continue
		}
		if n := runewidth.StringWidth(k); n > w && n <= formDeltaKeyCap {
			w = n
		}
	}
	return w
}

func entryRows(e metaEntry, keyw, width int, open bool, thread func(string, int) []string) []entryRow {
	if e.kind == metaComment {
		return commentEntryRows(e.comment, open, width, thread)
	}
	head := padTo(truncCols(e.key, formDeltaKeyCap), keyw) + "  "
	var rows []entryRow
	switch e.kind {
	case metaDelta:
		shown := e.delta
		shown.from, shown.to = namedCell(shown.from), namedCell(shown.to)
		if !open || shown.banner {
			return []entryRow{{text: deltaRowText(shown, keyw, width)}}
		}
		head = padTo(truncCols(shown.key, formDeltaKeyCap), keyw) + "  "
		pad := strings.Repeat(" ", runewidth.StringWidth(head))
		room := max(width-len(pad)-2, formDeltaValueFloor)
		for _, side := range []struct {
			sign string
			raw  []byte
		}{{"- ", e.delta.fromRaw}, {"+ ", e.delta.toRaw}} {
			lines := []string{absentValue}
			if len(side.raw) > 0 {
				lines = formValueLines(side.raw, room)
			}
			for k, l := range lines {
				lead := pad + "  "
				switch {
				case len(rows) == 0:
					lead = head + side.sign
				case k == 0:
					lead = pad + side.sign
				}
				rows = append(rows, entryRow{text: lead + l})
			}
		}
		return rows
	}
	room := width - runewidth.StringWidth(head)
	if !open {
		return []entryRow{{text: truncCols(head+elide(headLine(e.value), room), width)}}
	}
	raw, _ := json.Marshal(e.value)
	pad := strings.Repeat(" ", runewidth.StringWidth(head))
	for k, l := range formValueLines(raw, max(room, formDeltaValueFloor)) {
		lead := pad
		if k == 0 {
			lead = head
		}
		rows = append(rows, entryRow{text: lead + l})
	}
	return rows
}

func (t *transcript) metaSource() metaSource {
	return metaSource{comments: t.comments, layout: t.coordFormat()}
}

func (t *transcript) metaPointsOf(m aria.Message, block int) []selectionPoint {
	ref := blockRef(m, block)
	if !t.metaOpen[ref] {
		return nil
	}
	return metaPoints(ref, t.metaSource().entries(m, block), t.entryOpen)
}

func (t *transcript) section(m aria.Message, block int, n livedoc.Node, deltas map[string]livedoc.FormDelta) metaSection {
	ref := blockRef(m, block)
	sec := metaSection{
		ref:       ref,
		block:     block,
		deltas:    deltas,
		notes:     len(t.comments.at(m, block)),
		open:      t.metaOpen[ref],
		entryOpen: t.entryOpen,
	}
	if sec.open {
		held := m
		sec.src, sec.m, sec.thread = t.metaSource(), &held, t.renderCommentText
	}
	return sec
}

func (t *transcript) blockMessage(ref nodeRef) (aria.Message, int, bool) {
	var found aria.Message
	block, ok := 0, false
	visit := func(m aria.Message) {
		if ok || m.Turn != ref.turn {
			return
		}
		if ref.index == inquiryNode {
			if m.Inquiry != "" {
				found, block, ok = m, ldrender.BlockInquiry, true
			}
			return
		}
		if i := ref.index - int(m.From); i >= 0 && i < len(m.Nodes) {
			found, block, ok = m, i, true
		}
	}
	t.forEachMessage(visit)
	if open := t.openMessage(); open != nil {
		visit(*open)
	}
	return found, block, ok
}

func (t *transcript) entriesAt(ref nodeRef) []metaEntry {
	m, block, ok := t.blockMessage(blockOf(ref))
	if !ok {
		return nil
	}
	return t.metaSource().entries(m, block)
}

func (t *transcript) entryAt(ref nodeRef) (metaEntry, bool) {
	if ref.meta <= 0 {
		return metaEntry{}, false
	}
	entries := t.entriesAt(ref)
	if ref.meta > len(entries) {
		return metaEntry{}, false
	}
	return entries[ref.meta-1], true
}

func (t *transcript) commentOf(ref nodeRef) (comment, bool) {
	if ref.meta <= 0 || t.comments == nil || len(t.comments.list) == 0 {
		return comment{}, false
	}
	m, block, ok := t.blockMessage(blockOf(ref))
	if !ok || len(t.comments.at(m, block)) == 0 {
		return comment{}, false
	}
	entries := t.metaSource().entries(m, block)
	if ref.meta > len(entries) {
		return comment{}, false
	}
	e := entries[ref.meta-1]
	if e.kind != metaComment || ref.reply > len(e.comment.thread) {
		return comment{}, false
	}
	return e.comment, true
}

func (t *transcript) entryRefFor(c comment) (nodeRef, bool) {
	end := endBlockOf(c.at)
	var found nodeRef
	ok := false
	visit := func(m aria.Message) {
		try := func(block int) {
			if ok {
				return
			}
			if k, has := blockKeyOf(m, block); !has || k != end {
				return
			}
			for i, e := range t.metaSource().entries(m, block) {
				if e.kind == metaComment && e.comment.id == c.id {
					found, ok = metaRefOf(blockRef(m, block), i+1), true
					return
				}
			}
		}
		try(ldrender.BlockInquiry)
		for i := range m.Nodes {
			try(i)
		}
	}
	t.forEachMessage(visit)
	if open := t.openMessage(); open != nil && !ok {
		visit(*open)
	}
	return found, ok
}

func (t *transcript) hasMeta(ref nodeRef) bool {
	return len(t.entriesAt(ref)) > 0
}

func (t *transcript) sectionOpenIn(m aria.Message) bool {
	if m.Inquiry != "" && t.metaOpen[blockRef(m, ldrender.BlockInquiry)] {
		return true
	}
	for i := range m.Nodes {
		if t.metaOpen[nodeRefAt(m, i)] {
			return true
		}
	}
	return false
}

func (t *transcript) toggleEntry(entry nodeRef) bool {
	if _, ok := t.entryAt(entry); !ok {
		return false
	}
	return t.toggleFolds([]foldTarget{{state: t.openEntries(), ref: entry}})
}
