package provider

import (
	"fmt"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/tool"
)

// SendableImages returns msg with every image block made acceptable to a
// provider, and reports whether anything changed.
//
// An image that is legal when written can be illegal when sent: Anthropic
// accepts 8000px per image in an ordinary request and only 2000px once the
// request carries more than twenty image or document blocks, and rejects the
// WHOLE request rather than the image. The offending picture is by then in the
// log, so every later turn rebuilds the same request and dies the same way.
//
// A picture that can be shrunk is shrunk, and says by how much, because a model
// reading coordinates off it must be told when it is no longer the size it was;
// one that cannot becomes text in place, so no block index moves. It runs on
// the way to an encoder rather than to the log, so the fix reaches records
// written before it existed.
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
