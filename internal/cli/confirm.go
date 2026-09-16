package cli

import "strings"

// A DESTRUCTIVE VERB ASKS FIRST. `x` on a row and `:kill` put a question to
// the reader and hold the keyboard until it is answered; `X` on a row does
// the same work with no question at all.
//
// THE QUESTION COSTS NO HEIGHT. It is not a dialog: the sentence rides the
// bar's alert slot as a pinned notice (the one alert that does not retire on
// its own), the answers ride the bar's command slot as the pit token, and
// whatever was on screen stays there. A modal that shoved the conversation
// down to say six words was two copies of one question.

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
	t.status.pinNotice(prompt)
	t.render()
}

// clearConfirm takes the question down and the notice with it, in that order:
// the bar must not be left holding a sentence about a question nobody can see.
func (t *transcript) clearConfirm() {
	t.confirm = nil
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

// killPrompt is the sentence the two kill doors ask.
func killPrompt(id string) string { return "kill " + strings.TrimSpace(id) + "?" }
