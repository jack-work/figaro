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

type assembled struct {
	Text       string
	ToolCalls  []toolCall
	StopReason string
	Usage      *message.Usage
}

func drainSSE(ctx context.Context, body io.Reader, bus provider.Bus) (assembled, error) {
	var out assembled
	byIndex := map[int]*toolCall{}
	byID := map[string]int{}
	var order []int
	started := map[int]bool{}
	// Synthetic slots count down from -1 so they never collide with a real index.
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
			// Keep-alives and comments are not a reason to lose the turn.
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
				// Gateways omit `index`: a new id opens a new slot, a bare
				// fragment continues the last one.
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

// toolInvokeContent turns malformed arguments into an empty object; the
// model gets the tool's error back rather than losing the turn.
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
