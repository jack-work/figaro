package cli

import (
	"strconv"
	"strings"
)

func (t *transcript) commentAtCursor() (nodeRef, bool) {
	if !t.visual.active() || t.comments == nil {
		return nodeRef{}, false
	}
	p := t.visual.cursor
	block := blockOf(p.ref)
	k, ok := t.blockKeyOfRef(block)
	if !ok {
		return nodeRef{}, false
	}
	text, _, base, why := t.nodeQuoteSource(block)
	if why != "" {
		return nodeRef{}, false
	}
	_, rows := t.nodeLines(block)
	if p.row >= len(rows) {
		return nodeRef{}, false
	}
	o, _, ok := colToRune(alignRows(text, rows)[p.row], p.col, true)
	if !ok {
		return nodeRef{}, false
	}
	off := base + o
	for _, c := range t.comments.list {
		lo, hi, ok := c.covering(k)
		if !ok || off < lo || (hi >= 0 && off >= hi) {
			continue
		}
		if ref, ok := t.entryRefFor(c); ok {
			return ref, true
		}
	}
	return nodeRef{}, false
}

func (t *transcript) selectedCommentAddr() (string, bool) {
	ref := nodeRef{}
	switch {
	case t.visual.active():
		ref = t.visual.cursor.ref
	case t.selection.active:
		ref = t.selection.focus.nodeRef
	}
	if ref.meta == 0 {
		return "", false
	}
	c, ok := t.commentOf(ref)
	if !ok {
		return "", false
	}
	return commentAddress(c, ref.reply), true
}

func pagerCommentNext(t *transcript) { t.noteStep(t.stepComment(1)) }

func pagerCommentPrev(t *transcript) { t.noteStep(t.stepComment(-1)) }

func pagerDeleteComment(t *transcript) {
	addr, ok := t.selectedCommentAddr()
	if !ok {
		t.note("x deletes the selected comment; select one with c j")
		return
	}
	if t.command == nil {
		t.note("deleting a comment needs a live session")
		return
	}
	id, _, _ := strings.Cut(addr, ".")
	t.command("comment -X " + id)
}

func (t *transcript) stepComment(dir int) string {
	if t.comments == nil || len(t.comments.list) == 0 {
		return "no comments on this aria"
	}
	note := t.stepEntry(dir, func(e metaEntry) bool { return e.kind == metaComment }, "no comment")
	if note == "" {
		return ""
	}
	if turn, ok := t.commentBeyondWindow(dir); ok {
		t.startJump(jumpTarget{turn: turn})
		return "the next comment is in turn " + strconv.Itoa(turn) + ": paging it in"
	}
	return note
}

func (t *transcript) commentBeyondWindow(dir int) (int, bool) {
	if t.comments == nil {
		return 0, false
	}
	msgs := t.messages()
	if len(msgs) == 0 {
		return 0, false
	}
	lowest := msgs[0].Turn
	best := 0
	for _, c := range t.comments.list {
		if c.turn <= 0 {
			continue
		}
		if _, drawn := t.entryRefFor(c); drawn {
			continue
		}
		if dir < 0 && c.turn < lowest && c.turn > best {
			best = c.turn
		}
	}
	return best, best > 0
}
