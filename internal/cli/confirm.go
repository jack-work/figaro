package cli

import "strings"

// A DESTRUCTIVE VERB ASKS FIRST. `x` on a row and `:kill` put a question on
// the screen and hold the keyboard until it is answered; `X` on a row does
// the same work with no question at all.
//
// The question is a pit (so the bar names it and every key that is not an
// answer is swallowed) plus a pinned notice, which is the one alert that does
// not retire on its own: it stands for exactly as long as the question does.

// confirmAsk is the pending question: the sentence, and what a yes runs.
type confirmAsk struct {
	prompt string
	yes    func()
}

// askConfirm puts the question up. The caller's action runs on the input
// goroutine, so it must hand off anything that dials, exactly as every other
// hook here does.
func (t *transcript) askConfirm(prompt string, yes func()) {
	t.confirm = &confirmAsk{prompt: prompt, yes: yes}
	t.pit.showList(pitConfirm, "", []pitRow{staticRow("  " + prompt + "  [y/N]")})
	t.status.pinNotice(prompt)
	t.focused = focusPit
	t.render()
}

// clearConfirm takes the question down and the notice with it, in that order:
// the bar must not be left holding a sentence about a question nobody can see.
func (t *transcript) clearConfirm() {
	t.confirm = nil
	if t.showing(pitConfirm) {
		t.pit.close()
	}
	t.status.setNotice("")
}

func confirmYes(t *transcript) {
	ask := t.confirm
	t.clearConfirm()
	if ask != nil && ask.yes != nil {
		ask.yes()
	}
	t.render()
}

func confirmNo(t *transcript) {
	t.clearConfirm()
	t.note("")
	t.render()
}

// confirmLines is the question as the drawer draws it.
func (t *transcript) confirmLines() []string {
	if t.confirm == nil {
		return nil
	}
	return []string{pitGray(clipToWidth(t.confirm.prompt+"  [y/N]", t.w))}
}

// killPrompt is the sentence the two kill doors ask.
func killPrompt(id string) string { return "kill " + strings.TrimSpace(id) + "?" }
