// Package tokens provides context-size estimation.
package tokens

import (
	"encoding/json"

	"github.com/jack-work/figaro/internal/message"
)

// ContextFromUsage is the single definition of "context used" as of the turn
// that reported u.
//
// The figure is **prompt + that turn's output**: every token the provider
// counted on the way in, plus the assistant reply that is now part of the
// transcript. It is therefore the size the next request's prompt starts from
// (modulo anything appended since), not the size of the request that was sent.
//
// All three input buckets must be summed. Figaro stamps cache breakpoints on
// every turn by default (see the cache-control skill), so after the first turn
// nearly the whole prompt comes back as CacheReadTokens and InputTokens is
// only the uncached tail. Anthropic reports these as disjoint counts:
//
//	prompt = InputTokens + CacheReadTokens + CacheWriteTokens
//
// Summing only Input+Output, as this code used to: under-reports a cached
// conversation by one to two orders of magnitude.
//
// Every caller that derives a context size from a Usage block must go through
// here so the incremental (agent.refreshMetrics) and full-fold (ContextSize)
// paths cannot drift.
func ContextFromUsage(u *message.Usage) int {
	if u == nil {
		return 0
	}
	return u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens + u.OutputTokens
}

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

	base := ContextFromUsage(msgs[watermark].Usage)

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
		case message.ContentProse, message.ContentThinking:
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
		case message.ContentToolResult:
			chars += len(c.ToolCallID) + len(c.ToolName) + len(c.Text)
		}
	}
	if chars == 0 {
		return 0
	}
	return (chars + 3) / 4
}
