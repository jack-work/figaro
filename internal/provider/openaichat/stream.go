package openaichat

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
)

// streamChunk is one SSE frame of a streamed completion.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string     `json:"content"`
			ToolCalls []toolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

// assembled is the result of draining one response stream.
type assembled struct {
	Text       string
	ToolCalls  []toolCall
	StopReason string
	Usage      *message.Usage
}

// drainSSE consumes a streamed Chat Completions response, pushing deltas to
// the bus as they arrive and accumulating the final assistant message.
func drainSSE(ctx context.Context, body io.Reader, bus provider.Bus) (assembled, error) {
	var out assembled
	byIndex := map[int]*toolCall{}
	byID := map[string]int{}
	var order []int
	started := map[int]bool{}
	// Synthetic slots count down so they can never collide with a real
	// index; haveLast is a separate flag because -1 is a legal slot here.
	lastIdx, haveLast := 0, false
	nextSynthetic := -1

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			// A gateway is free to emit keep-alives and comments; a frame we
			// cannot parse is not a reason to lose a turn already in flight.
			continue
		}
		if chunk.Usage != nil {
			if u := chunk.Usage.toIR(); u != nil {
				out.Usage = u
			}
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				out.StopReason = choice.FinishReason
			}
			if text := choice.Delta.Content; text != "" {
				out.Text += text
				bus.PushDelta(message.TextContent(text))
			}
			for _, tc := range choice.Delta.ToolCalls {
				// `index` is required by the spec and gateways omit it
				// anyway. Defaulting the missing case to 0 merged every
				// call of a turn into one slot and concatenated their
				// arguments, a client must not silently corrupt a tool
				// call because an upstream left a field out. With no
				// index: a new id opens a new slot, and a bare fragment
				// continues the slot it is obviously continuing.
				idx := 0
				switch {
				case tc.Index != nil:
					idx = *tc.Index
				case tc.ID != "":
					if seen, ok := byID[tc.ID]; ok {
						idx = seen
					} else {
						idx = nextSynthetic
						nextSynthetic--
						byID[tc.ID] = idx
					}
				case haveLast:
					idx = lastIdx
				default:
					idx = nextSynthetic
					nextSynthetic--
				}
				lastIdx, haveLast = idx, true
				cur, ok := byIndex[idx]
				if !ok {
					cur = &toolCall{Type: "function"}
					byIndex[idx] = cur
					order = append(order, idx)
				}
				if tc.ID != "" {
					cur.ID = tc.ID
				}
				if tc.Function.Name != "" {
					cur.Function.Name = tc.Function.Name
				}
				if cur.ID != "" && cur.Function.Name != "" && !started[idx] {
					started[idx] = true
					bus.PushToolInvokeStart(cur.ID, cur.Function.Name)
				}
				if frag := tc.Function.Arguments; frag != "" {
					cur.Function.Arguments += frag
					if started[idx] {
						bus.PushToolInvokeDelta(cur.ID, frag)
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}
	for _, idx := range order {
		call := byIndex[idx]
		if call.ID == "" || call.Function.Name == "" {
			continue
		}
		out.ToolCalls = append(out.ToolCalls, *call)
		bus.PushToolReady(toolInvokeContent(*call))
	}
	return out, nil
}

// toolInvokeContent decodes one accumulated call into IR. Malformed
// arguments become an empty object rather than failing the turn: the model
// gets the tool's own error back and can correct itself, which is strictly
// better than losing the whole response.
func toolInvokeContent(call toolCall) message.Content {
	args := map[string]any{}
	if strings.TrimSpace(call.Function.Arguments) != "" {
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			args = map[string]any{}
		}
	}
	return message.Content{
		Type:       message.ContentToolInvoke,
		ToolCallID: call.ID,
		ToolName:   call.Function.Name,
		Arguments:  args,
	}
}

// toIRMessage folds a drained stream into the canonical assistant message.
func (a assembled) toIRMessage() message.Message {
	msg := message.Message{Role: message.RoleOutput, Usage: a.Usage}
	if a.Text != "" {
		msg.Content = append(msg.Content, message.TextContent(a.Text))
	}
	for _, call := range a.ToolCalls {
		msg.Content = append(msg.Content, toolInvokeContent(call))
	}
	msg.StopReason = stopReason(a.StopReason, len(a.ToolCalls) > 0)
	return msg
}

// stopReason maps the dialect's finish_reason onto figaro's vocabulary.
func stopReason(finish string, hasCalls bool) message.StopReason {
	switch finish {
	case "length":
		return message.StopLength
	case "tool_calls", "function_call":
		return message.StopToolInvoke
	case "stop":
		if hasCalls {
			return message.StopToolInvoke
		}
		return message.StopEnd
	}
	if hasCalls {
		return message.StopToolInvoke
	}
	return message.StopEnd
}
