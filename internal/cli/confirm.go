package cli

import "strings"

type confirmAsk struct {
	prompt string
	yes    func()
}

// yes runs on the input goroutine: it must hand off anything that dials.
func (t *transcript) askConfirm(prompt string, yes func()) {
	t.confirm = &confirmAsk{prompt: prompt, yes: yes}
	t.status.pinNotice(prompt)
	t.render()
}

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

func killPrompt(id string) string { return "kill " + strings.TrimSpace(id) + "?" }
