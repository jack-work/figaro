package anthropic

import (
	"encoding/json"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

func (a *Anthropic) projectMessages(msgs []message.Message) (result []nativeMessage, perFLT []json.RawMessage) {
	perFLT = make([]json.RawMessage, len(msgs))
	prevSnap := form.Snapshot{}
	for i, msg := range msgs {
		nm, ok := a.renderMessage(msg, &prevSnap)
		if !ok {
			continue
		}
		result = append(result, nm)
		if raw, err := json.Marshal(nm); err == nil {
			perFLT[i] = raw
		}
	}
	return result, perFLT
}

func (a *Anthropic) encodeAll(msgs []message.Message) [][]json.RawMessage {
	out := make([][]json.RawMessage, 0, len(msgs))
	prevSnap := form.Snapshot{}
	for _, msg := range msgs {
		nm, ok := a.renderMessage(msg, &prevSnap)
		if !ok {
			continue
		}
		raw, err := json.Marshal(nm)
		if err != nil {
			continue
		}
		out = append(out, []json.RawMessage{raw})
	}
	return out
}
