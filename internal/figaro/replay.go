package figaro

import (
	"errors"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
)

func (a *Agent) SubmitReplay(inquiry message.Message, window form.Patch) error {
	evt := replayEvent(inquiry)
	if evt.text == "" && len(evt.images) == 0 {
		return errors.New("the recorded inquiry holds nothing to ask")
	}
	if !window.IsIdentity() {
		if _, err := a.backend.ApplyForm(a.id, window); err != nil {
			return err
		}
		a.form.Apply(window)
	}
	if !a.inbox.Send(evt) {
		return ErrStopping
	}
	if !a.turnActive() {
		a.publishRuntime(rpc.RuntimeAccepted, "")
	}
	return nil
}

func replayEvent(inquiry message.Message) event {
	evt := event{typ: eventUserPrompt, replay: true}
	var texts []string
	for _, c := range inquiry.Content {
		switch c.Type {
		case message.ContentProse:
			if c.Text == "" {
				continue
			}
			evt.segments = append(evt.segments, promptSegment{sender: c.Sender, principal: c.Principal, text: c.Text})
			texts = append(texts, c.Text)
		case message.ContentImage:
			if c.ToolCallID == "" {
				evt.images = append(evt.images, c)
			}
		}
	}
	evt.text = strings.Join(texts, "\n\n")
	return evt
}
