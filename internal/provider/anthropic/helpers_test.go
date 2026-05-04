package anthropic

import (
	"encoding/json"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
)

// projectMessages renders a whole IR slice for tests that want
// before/after wire-shape parity.
func (a *Anthropic) projectMessages(msgs []message.Message) (result []nativeMessage, perFLT []json.RawMessage) {
	perFLT = make([]json.RawMessage, len(msgs))
	prevSnap := chalkboard.Snapshot{}
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

// encodeAll mimics what the agent's catchUpTranslator does: encode
// each IR message into per-message wire bytes.
func (a *Anthropic) encodeAll(msgs []message.Message) [][]json.RawMessage {
	out := make([][]json.RawMessage, 0, len(msgs))
	prevSnap := chalkboard.Snapshot{}
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
