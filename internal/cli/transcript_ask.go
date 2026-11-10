package cli

import "strings"

type boxQuestion struct {
	prompt string
	secret bool
	title  []string
	answer func(text string)
	pit    pitID
	once   bool
}

func (t *transcript) asking() bool { return t.box == boxAsk }

func (t *transcript) pose(q *boxQuestion) {
	t.ask = q
	t.reopenAsk()
}

func (t *transcript) reopenAsk() bool {
	if t.ask == nil || t.box != boxNone || t.confirm != nil || t.inSearch {
		return false
	}
	t.askline = lineEditor{}
	t.box, t.jumpNote = boxAsk, ""
	t.menu = nil
	return true
}

func (t *transcript) answerAsk() {
	q, text := t.ask, t.askline.String()
	t.ask = nil
	t.closeAsk()
	if q != nil && q.answer != nil {
		q.answer(text)
	}
}

func (t *transcript) closeAsk() {
	if t.ask != nil && t.ask.once {
		t.ask = nil
	}
	t.askline = lineEditor{}
	if t.box == boxAsk {
		t.box = boxNone
	}
	t.clearCompletions()
}

func (t *transcript) retractAsk(q *boxQuestion) {
	if t.ask != q {
		return
	}
	t.ask = nil
	t.closeAsk()
}

func (t *transcript) askTitle() []string {
	if t.ask == nil {
		return nil
	}
	rows := make([]string, 0, len(t.ask.title))
	for _, r := range t.ask.title {
		rows = append(rows, pitGray(clipToWidth("  "+pitText(r), t.w)))
	}
	return rows
}

func (t *transcript) shownEditor() *lineEditor {
	e := t.editor()
	if t.box != boxAsk || t.ask == nil || !t.ask.secret {
		return e
	}
	return &lineEditor{runes: []rune(strings.Repeat("*", len(e.runes))), cursor: e.cursor}
}
