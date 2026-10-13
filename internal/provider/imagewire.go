package provider

import (
	"fmt"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/tool"
)

// SendableImages shrinks or textualizes image blocks on the way to an encoder:
// Anthropic allows 8000px per image, 2000px once a request carries more than
// twenty, and rejects the whole request. A shrunk image says by how much; a
// textualized one keeps its block index.
func SendableImages(msg message.Message) (message.Message, bool) {
	lim := tool.DefaultImageLimits()
	changed := false
	for i := range msg.Content {
		c := msg.Content[i]
		if c.Type != message.ContentImage || c.Data == "" {
			continue
		}
		fitted, needed, ok := tool.FitSendable(c.Data, c.MimeType, lim)
		if !needed {
			continue
		}
		if !changed {
			msg.Content = append([]message.Content(nil), msg.Content...)
			changed = true
		}
		if !ok {
			msg.Content[i] = message.Content{
				Type: message.ContentProse,
				Text: fmt.Sprintf("[image omitted: it could not be resized to the %dpx limit every image in a large request must meet]", tool.DefaultMaxSendDim),
			}
			continue
		}
		msg.Content[i].Data = fitted.Data
		msg.Content[i].MimeType = fitted.MimeType
		if note := fitted.Note(); note != "" {
			msg.Content[i].Text = note
		}
	}
	return msg, changed
}
