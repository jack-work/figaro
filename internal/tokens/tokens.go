// Package tokens provides context-size estimation.
package tokens

import (
	"encoding/json"

	"github.com/jack-work/figaro/internal/message"
)

// ContextSize returns estimated tokens. Uses Usage data as a watermark
// and falls back to chars/4 for messages after it.
func ContextSize(msgs []message.Message) (tokens int, exact bool) {
	if len(msgs) == 0 {
		return 0, true
	}


	watermark := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Usage != nil {
			watermark = i
			break
		}
	}

	if watermark < 0 {
		// No usage data; estimate everything.
		total := 0
		for _, m := range msgs {
			total += EstimateMessage(m)
		}
		return total, false
	}

	u := msgs[watermark].Usage
	base := u.InputTokens + u.OutputTokens

	if watermark == len(msgs)-1 {
		return base, true
	}


	tail := 0
	for _, m := range msgs[watermark+1:] {
		tail += EstimateMessage(m)
	}
	return base + tail, false
}

// EstimateMessage returns a chars/4 estimate. Images = 1200 tokens.
func EstimateMessage(m message.Message) int {
	chars := 0
	for _, c := range m.Content {
		switch c.Type {
		case message.ContentText, message.ContentThinking:
			chars += len(c.Text)
		case message.ContentImage:
			chars += 4800
		case message.ContentToolInvoke:
			chars += len(c.ToolName)
			if c.Arguments != nil {
				if b, err := json.Marshal(c.Arguments); err == nil {
					chars += len(b)
				}
			}
		}
	}
	if chars == 0 {
		return 0
	}
	return (chars + 3) / 4
}
