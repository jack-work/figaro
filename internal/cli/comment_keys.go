package cli

import (
	"strconv"
	"strings"

	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
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
		if ref, ok := t.noteRefFor(c); ok {
			return ref, true
		}
	}
	return nodeRef{}, false
}

func (t *transcript) noteRefFor(c comment) (nodeRef, bool) {
	end := endBlockOf(c.at)
	var found nodeRef
	ok := false
	visit := func(m aria.Message) {
		if ok {
			return
		}
		try := func(block int) {
			if k, has := blockKeyOf(m, block); has && k == end {
				for i, ci := range t.comments.byBlock[end] {
					if t.comments.list[ci].id == c.id {
						found, ok = noteRefOf(blockRef(m, block), i+1), true
					}
				}
			}
		}
		try(ldrender.BlockInquiry)
		for i := range m.Nodes {
			try(i)
		}
	}
	t.forEachMessage(visit)
	if open := t.openMessage(); open != nil {
		visit(*open)
	}
	return found, ok
}

func (t *transcript) stepThread(focus nodeRef) bool {
	c, ok := t.commentOf(focus)
	if !ok {
		return false
	}
	head := headOf(focus)
	switch {
	case !t.commentsOpen[head]:
		t.toggleFolds([]foldTarget{{state: t.openComments(), ref: head}})
		t.selectRef(head, false)
	case focus.reply < len(c.thread):
		t.selectRef(replyRefOf(head, focus.reply+1), false)
	default:
		t.selectRef(head, false)
		t.toggleFolds([]foldTarget{{state: t.openComments(), ref: head}})
	}
	t.ensureSelectionVisible()
	return true
}

func (t *transcript) selectedCommentAddr() (string, bool) {
	ref := nodeRef{}
	switch {
	case t.visual.active():
		ref = t.visual.cursor.ref
	case t.selection.active:
		ref = t.selection.focus.nodeRef
	}
	if ref.note == 0 {
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
	p, ok := t.stepTo(func(p selectionPoint) bool { return p.note > 0 }, dir)
	if ok {
		t.land(p.nodeRef)
		return ""
	}
	if turn, ok := t.commentBeyondWindow(dir); ok {
		t.startJump(jumpTarget{turn: turn})
		return "the next comment is in turn " + strconv.Itoa(turn) + ": paging it in"
	}
	return "no comment " + towards(dir)
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
		if _, drawn := t.noteRefFor(c); drawn {
			continue
		}
		if dir < 0 && c.turn < lowest && c.turn > best {
			best = c.turn
		}
	}
	return best, best > 0
}
