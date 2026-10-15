package cli

import (
	"strconv"
	"strings"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

func (t *transcript) appendNoteRows(rows []transcriptRow, m aria.Message, block int) []transcriptRow {
	idxs := t.comments.at(m, block)
	if len(idxs) == 0 {
		return rows
	}
	rows = append(rows, transcriptRow{text: ""})
	ref := blockRef(m, block)
	for i, ci := range idxs {
		note := noteRefOf(ref, i+1)
		for _, r := range t.commentRows(t.comments.list[ci], note, t.commentsOpen[note]) {
			r.text = plainNodeRow(r.text, t.w)
			rows = append(rows, r)
		}
	}
	return rows
}

func (t *transcript) commentRows(c comment, note nodeRef, open bool) []transcriptRow {
	pad := strings.Repeat(" ", commentIndent)
	head := c.head()
	fold := "▸"
	if open {
		fold = "▾"
	}
	id := term.StateDim("  " + commentAddress(c, 0))
	label := head.by
	if label == "" {
		label = "comment"
	}
	if !open {
		more := ""
		if n := len(c.thread) - 1; n > 0 {
			more = term.StateDim(" · " + pluralReplies(n))
		}
		first := strings.TrimSpace(strings.SplitN(head.text, "\n", 2)[0])
		return []transcriptRow{{ref: note, text: pad + term.CommentMark(fold+" "+commentGlyph+" "+label) + term.StateDim(": ") + first + more + id}}
	}
	rows := []transcriptRow{{ref: note, text: pad + term.CommentMark(fold+" "+commentGlyph+" "+c.token) + id}}
	body := t.w - commentBodyShift - ldrender.GutterCols
	if body < 8 {
		body = 8
	}
	shift := strings.Repeat(" ", commentBodyShift)
	for k, e := range c.thread {
		ref := replyRefOf(note, k+1)
		if k > 0 {
			rows = append(rows, transcriptRow{ref: ref, text: ""})
		}
		by := e.by
		if by == "" {
			by = "comment"
		}
		rows = append(rows, transcriptRow{ref: ref, text: strings.Repeat(" ", commentBodyShift-2) + term.CommentMark("› "+by) + term.StateDim("  "+commentAddress(c, k+1))})
		for _, line := range t.renderCommentText(e.text, body) {
			rows = append(rows, transcriptRow{ref: ref, text: shift + line})
		}
	}
	return rows
}

func pluralReplies(n int) string {
	if n == 1 {
		return "1 reply"
	}
	return strconv.Itoa(n) + " replies"
}

func (t *transcript) renderCommentText(text string, width int) []string {
	if t.view != nil {
		rows := pagerView(t.view).Render(livedoc.Node{Type: livedoc.NodeProse, Markdown: text}, width, t.tick)
		for len(rows) > 0 && strings.TrimSpace(render.StripEscapes(rows[len(rows)-1])) == "" {
			rows = rows[:len(rows)-1]
		}
		for len(rows) > 0 && strings.TrimSpace(render.StripEscapes(rows[0])) == "" {
			rows = rows[1:]
		}
		if len(rows) > 0 {
			return rows
		}
	}
	return wrapPlain(text, width)
}

type annotationRuns map[int][][2]int

func (t *transcript) annotations(a, b int) annotationRuns {
	return t.annotate(a, b, nil)
}

func (t *transcript) focusedAnnotation(a, b int) annotationRuns {
	if !t.selection.active || t.selection.focus.note == 0 {
		return nil
	}
	focused, ok := t.commentOf(t.selection.focus.nodeRef)
	if !ok {
		return nil
	}
	return t.annotate(a, b, func(c comment) bool { return c.id == focused.id })
}

func (t *transcript) annotate(a, b int, only func(comment) bool) annotationRuns {
	if t.comments == nil || len(t.comments.list) == 0 {
		return nil
	}
	out := annotationRuns{}
	seen := map[nodeRef]bool{}
	t.forEachWindowRow(a, b, func(e *lineEntry, rel int) {
		ref := e.refAt(rel)
		if !ref.valid() || ref.pseudo() || seen[ref] {
			return
		}
		seen[ref] = true
		t.annotateNode(ref, out, only)
	})
	if len(out) == 0 {
		return nil
	}
	return out
}

func (t *transcript) underlineNotes(line int, row string, runs annotationRuns) string {
	for _, r := range runs[line] {
		row = paintColumns(row, r[0], r[1], term.CommentUnderline(), "", 0)
	}
	return row
}

func (t *transcript) annotateNode(ref nodeRef, out annotationRuns, only func(comment) bool) {
	k, ok := t.blockKeyOfRef(ref)
	if !ok {
		return
	}
	type span struct{ lo, hi int }
	var spans []span
	for _, c := range t.comments.list {
		if only != nil && !only(c) {
			continue
		}
		if lo, hi, ok := c.covering(k); ok {
			spans = append(spans, span{lo, hi})
		}
	}
	if len(spans) == 0 {
		return
	}
	text, _, base, why := t.nodeQuoteSource(ref)
	if why != "" {
		return
	}
	lines, rows := t.nodeLines(ref)
	if len(rows) == 0 {
		return
	}
	for i, row := range alignRows(text, rows) {
		if !row.aligned {
			continue
		}
		var runs [][2]int
		open, last := -1, -1
		for col, off := range row.cols {
			if off < 0 {
				continue
			}
			abs := base + off
			hit := false
			for _, s := range spans {
				if abs >= s.lo && (s.hi < 0 || abs < s.hi) {
					hit = true
					break
				}
			}
			switch {
			case hit && open < 0:
				open, last = col, col
			case hit:
				last = col
			case open >= 0:
				runs = append(runs, [2]int{open, last + 1})
				open = -1
			}
		}
		if open >= 0 {
			runs = append(runs, [2]int{open, last + 1})
		}
		if len(runs) > 0 {
			out[lines[i]] = append(out[lines[i]], runs...)
		}
	}
}

func (t *transcript) nodeLines(ref nodeRef) (lines []int, rows []string) {
	for k := t.entriesOfTurn(ref.turn); k >= 0 && k < len(t.index.entries); k++ {
		e := &t.index.entries[k]
		if e.turn != ref.turn {
			break
		}
		if e.isGap() {
			continue
		}
		base := entryRowsStart(e)
		for i := range e.rows {
			if e.rows[i].ref == ref {
				lines = append(lines, base+i)
				rows = append(rows, e.rows[i].text)
			}
		}
	}
	return lines, rows
}

func (t *transcript) washNotes(line int, row string, runs annotationRuns) string {
	for _, r := range runs[line] {
		row = paintColumns(row, r[0], r[1], term.CommentWash(), "", 0)
	}
	return row
}
