// Package tokens provides context-size estimation for arias.
//
// The primary function, ContextSize, returns the token count for a
// conversation block. It uses authoritative Usage data from the last
// assistant message when available, falling back to a chars/4
// heuristic for any messages appended after the last watermark.
//
// This is a pure function of the block — no network, no state.
package tokens

import (
	"encoding/json"

	"github.com/jack-work/figaro/internal/message"
)

// ContextSize returns the estimated token count for a conversation block.
//
// It walks backwards to find the last assistant message with Usage
// (a "watermark"). If found, it uses InputTokens + OutputTokens as
// the authoritative base, then adds a chars/4 heuristic for any
// messages appended after. exact is true only when the watermark is
// the last message in the block.
func ContextSize(block *message.Block) (tokens int, exact bool) {
	if block == nil || len(block.Messages) == 0 {
		return 0, true
	}

	// Find last assistant message with Usage.
	watermark := -1
	for i := len(block.Messages) - 1; i >= 0; i-- {
		if block.Messages[i].Usage != nil {
			watermark = i
			break
		}
	}

	if watermark < 0 {
		// No authoritative data — estimate everything.
		total := 0
		for _, m := range block.Messages {
			total += EstimateMessage(m)
		}
		return total, false
	}

	u := block.Messages[watermark].Usage
	base := u.InputTokens + u.OutputTokens

	if watermark == len(block.Messages)-1 {
		return base, true
	}

	// Heuristic tail for messages after the watermark.
	tail := 0
	for _, m := range block.Messages[watermark+1:] {
		tail += EstimateMessage(m)
	}
	return base + tail, false
}

// EstimateMessage returns a chars/4 token estimate for a single message.
// Images are estimated at 1200 tokens (4800 chars) per pi-mono convention.
func EstimateMessage(m message.Message) int {
	chars := 0
	for _, c := range m.Content {
		switch c.Type {
		case message.ContentText, message.ContentThinking:
			chars += len(c.Text)
		case message.ContentImage:
			chars += 4800
		case message.ContentToolCall:
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
