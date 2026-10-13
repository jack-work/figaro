package cli

import (
	"strings"

	"github.com/jack-work/figaro/api/decide"
	"github.com/jack-work/figaro/api/rpc"
)

const optionsPrompt = decide.Sigil + " "

const pitDecision pitID = "decision"

type decision struct {
	question string
	resp     rpc.DecideResponse
}

func (t *transcript) openOptions() {
	t.askOptions = true
	t.optline.reset()
	t.clearCompletions()
}

func (t *transcript) closeOptions() {
	t.askOptions = false
	t.optline.reset()
	t.clearCompletions()
}

func (t *transcript) optionsTitle() []string {
	ask, _ := decide.Bare(strings.TrimRight(t.draft.String(), "\n"))
	ask = composerFirstLine(strings.TrimSpace(strings.TrimPrefix(ask, visualRangePlaceholder)))
	row := decide.Sigil + " options · comma separated · empty for yes/no · ^S decide · Esc back"
	if ask != "" {
		row = decide.Sigil + " " + ask + " · comma separated · empty for yes/no · ^S decide · Esc back"
	}
	return []string{pitGray(clipToWidth("  "+row, t.w))}
}

func (t *transcript) routeDraft(text string) bool {
	if _, options, ok := decide.Parse(text); ok && len(options) > 0 {
		t.decideDraft(text, nil)
		return true
	}
	if _, bare := decide.Bare(text); bare {
		t.openOptions()
		return true
	}
	return false
}

func (t *transcript) decideDraft(text string, choices []string) {
	expanded, note, err := t.expandComposerRange(text)
	if err != "" {
		t.setCommandNoteAt(err, alertError)
		return
	}
	if t.decideComposer == nil {
		t.setCommandNoteAt("deciding needs a live session", alertError)
		return
	}
	t.draft.remember(composerFirstLine(text))
	t.closeOptions()
	t.composerClose(true)
	if note != "" {
		t.setCommandNoteAt(note, alertInfo)
	}
	t.setProgressNote("…deciding")
	t.decideComposer(expanded, choices)
}

func (t *transcript) showDecision(question string, resp rpc.DecideResponse) {
	t.decision = &decision{question: question, resp: resp}
	t.openDecisionPit()
}

func (t *transcript) openDecisionPit() {
	if t.decision == nil {
		t.setCommandNoteAt("no decision yet: end a draft with a ?? line, or :decide -- <question>", alertInfo)
		return
	}
	d := t.decision
	rows := []pitRow{staticRow(pitGray("  " + decide.Sigil + " " + composerFirstLine(decisionAsk(d))))}
	lines := decide.Lines(d.resp)
	for i, l := range lines {
		row := staticRow("    " + l)
		if i < len(d.resp.Rows) {
			row.yank, row.id = d.resp.Rows[i].Option, d.resp.Rows[i].Option
		}
		rows = append(rows, row)
	}
	rows = append(rows, staticRow(""), staticRow(pitGray("  "+decide.Footer(d.resp))))
	t.closePanels()
	t.pit.showList(pitDecision, "", rows)
	t.focused = focusPit
}

func decisionAsk(d *decision) string {
	if d.resp.Question != "" {
		return d.resp.Question
	}
	return d.question
}
