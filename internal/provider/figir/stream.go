package figir

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
)

type Usage = message.Usage

type StreamEvent struct {
	Type        string                     `json:"type"`
	Text        string                     `json:"text,omitempty"`
	Signature   string                     `json:"signature,omitempty"`
	ToolCallID  string                     `json:"tool_call_id,omitempty"`
	ToolName    string                     `json:"tool_name,omitempty"`
	Arguments   map[string]interface{}     `json:"arguments,omitempty"`
	PartialJSON string                     `json:"partial_json,omitempty"`
	StopReason  string                     `json:"stop_reason,omitempty"`
	WireStop    string                     `json:"wire_stop,omitempty"`
	Usage       *Usage                     `json:"usage,omitempty"`
	Error       string                     `json:"error,omitempty"`
	Meta        map[string]json.RawMessage `json:"meta,omitempty"`
}

type assembled struct {
	Blocks     []message.Content
	StopReason string
	WireStop   string
	Usage      *message.Usage
}

type pendingTool struct {
	blockIdx int
	id       string
	name     string
	args     map[string]interface{}
	rawJSON  strings.Builder
	sig      string
	meta     map[string]json.RawMessage
	started  bool
	ready    bool
}

func drainSSE(ctx context.Context, body io.Reader, bus provider.Bus) (assembled, error) {
	var (
		out           assembled
		tools         []*pendingTool
		byID          = map[string]*pendingTool{}
		lastTool      *pendingTool
		eventHint     string
		events        int
		sawMessageEnd bool
		done          bool
	)

	getOrOpenTool := func(id, name string) *pendingTool {
		if id == "" && lastTool != nil && !lastTool.ready {
			if name != "" {
				lastTool.name = name
			}
			return lastTool
		}
		if id != "" {
			if pt, ok := byID[id]; ok {
				if name != "" {
					pt.name = name
				}
				lastTool = pt
				return pt
			}
		}
		pt := &pendingTool{
			blockIdx: len(out.Blocks),
			id:       id,
			name:     name,
		}
		out.Blocks = append(out.Blocks, message.Content{Type: message.ContentToolInvoke})
		if id != "" {
			byID[id] = pt
		}
		tools = append(tools, pt)
		lastTool = pt
		return pt
	}

	maybeStart := func(pt *pendingTool) {
		if !pt.started && pt.id != "" && pt.name != "" {
			pt.started = true
			bus.PushToolInvokeStart(pt.id, pt.name)
		}
	}

	buildToolContent := func(pt *pendingTool) message.Content {
		args := pt.args
		if args == nil {
			args = map[string]interface{}{}
			if raw := strings.TrimSpace(pt.rawJSON.String()); raw != "" {
				if err := json.Unmarshal([]byte(raw), &args); err != nil || args == nil {
					args = map[string]interface{}{}
				}
			}
		}
		return message.Content{
			Type:       message.ContentToolInvoke,
			ToolCallID: pt.id,
			ToolName:   pt.name,
			Arguments:  args,
			Signature:  pt.sig,
			Meta:       pt.meta,
		}
	}

	finalizeTool := func(pt *pendingTool) {
		c := buildToolContent(pt)
		out.Blocks[pt.blockIdx] = c
		if !pt.ready && pt.id != "" && pt.name != "" {
			maybeStart(pt)
			pt.ready = true
			bus.PushToolReady(c)
		}
	}

	syncPartialTools := func() {
		for _, pt := range tools {
			if !pt.ready && pt.id != "" && pt.name != "" {
				out.Blocks[pt.blockIdx] = buildToolContent(pt)
			}
		}
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			syncPartialTools()
			return out, ctx.Err()
		default:
		}
		provider.SawByte(ctx)
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			eventHint = ""
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventHint = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			done = true
			break
		}
		if payload == "" {
			continue
		}
		events++
		var ev StreamEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			if eventHint == "error" {
				syncPartialTools()
				return out, fmt.Errorf("sse error: %s", payload)
			}
			continue
		}
		if ev.Type == "" {
			ev.Type = eventHint
		}

		switch ev.Type {
		case "thinking_delta":
			if n := len(out.Blocks); n > 0 && out.Blocks[n-1].Type == message.ContentThinking {
				b := &out.Blocks[n-1]
				b.Text += ev.Text
				if ev.Signature != "" {
					b.Signature = ev.Signature
				}
				b.Meta = mergeMeta(b.Meta, ev.Meta)
			} else {
				out.Blocks = append(out.Blocks, message.Content{
					Type:      message.ContentThinking,
					Text:      ev.Text,
					Signature: ev.Signature,
					Meta:      mergeMeta(nil, ev.Meta),
				})
			}
			if ev.Text != "" || ev.Signature != "" || len(ev.Meta) > 0 {
				bus.PushDelta(message.Content{
					Type:      message.ContentThinking,
					Text:      ev.Text,
					Signature: ev.Signature,
					Meta:      ev.Meta,
				})
			}

		case "prose_delta":
			if n := len(out.Blocks); n > 0 && out.Blocks[n-1].Type == message.ContentProse {
				b := &out.Blocks[n-1]
				b.Text += ev.Text
				if ev.Signature != "" {
					b.Signature = ev.Signature
				}
				b.Meta = mergeMeta(b.Meta, ev.Meta)
			} else if ev.Text != "" {
				out.Blocks = append(out.Blocks, message.Content{
					Type:      message.ContentProse,
					Text:      ev.Text,
					Signature: ev.Signature,
					Meta:      mergeMeta(nil, ev.Meta),
				})
			}
			if ev.Text != "" {
				bus.PushDelta(message.Content{
					Type:      message.ContentProse,
					Text:      ev.Text,
					Signature: ev.Signature,
					Meta:      ev.Meta,
				})
			}

		case "tool_invoke_start":
			pt := getOrOpenTool(ev.ToolCallID, ev.ToolName)
			if ev.Signature != "" {
				pt.sig = ev.Signature
			}
			pt.meta = mergeMeta(pt.meta, ev.Meta)
			if ev.Arguments != nil {
				pt.args = ev.Arguments
			}
			maybeStart(pt)

		case "tool_invoke_delta":
			pt := getOrOpenTool(ev.ToolCallID, ev.ToolName)
			if ev.Signature != "" {
				pt.sig = ev.Signature
			}
			pt.meta = mergeMeta(pt.meta, ev.Meta)
			maybeStart(pt)
			if ev.PartialJSON != "" {
				pt.rawJSON.WriteString(ev.PartialJSON)
				if pt.started {
					bus.PushToolInvokeDelta(pt.id, ev.PartialJSON)
				}
			}

		case "tool_invoke_end", "tool_invoke":
			pt := getOrOpenTool(ev.ToolCallID, ev.ToolName)
			if ev.Signature != "" {
				pt.sig = ev.Signature
			}
			pt.meta = mergeMeta(pt.meta, ev.Meta)
			if ev.Arguments != nil {
				pt.args = ev.Arguments
			}
			if ev.PartialJSON != "" {
				pt.rawJSON.WriteString(ev.PartialJSON)
			}
			finalizeTool(pt)

		case "usage":
			if ev.Usage != nil {
				u := *ev.Usage
				out.Usage = &u
			}

		case "message_end":
			sawMessageEnd = true
			if ev.StopReason != "" {
				out.StopReason = ev.StopReason
			}
			if ev.WireStop != "" {
				out.WireStop = ev.WireStop
			}
			if ev.Usage != nil {
				u := *ev.Usage
				out.Usage = &u
			}

		case "error":
			syncPartialTools()
			msg := ev.Error
			if msg == "" {
				msg = payload
			}
			return out, fmt.Errorf("sse error: %s", msg)
		}
	}

	if err := scanner.Err(); err != nil {
		syncPartialTools()
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, &provider.StreamCut{Awaited: "message_end", Events: events, Err: err}
	}
	if !done && !sawMessageEnd {
		syncPartialTools()
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, &provider.StreamCut{Awaited: "message_end", Events: events}
	}

	for _, pt := range tools {
		finalizeTool(pt)
	}
	return out, nil
}

func (a assembled) toIRMessage() message.Message {
	msg := message.Message{Role: message.RoleOutput, Usage: a.Usage}
	hasCalls := false
	for _, b := range a.Blocks {
		switch b.Type {
		case message.ContentThinking:
			if b.Text != "" || b.Signature != "" || len(b.Meta) > 0 {
				msg.Content = append(msg.Content, b)
			}
		case message.ContentProse:
			if b.Text != "" {
				msg.Content = append(msg.Content, b)
			}
		case message.ContentToolInvoke:
			if b.ToolCallID != "" && b.ToolName != "" {
				if b.Arguments == nil {
					b.Arguments = map[string]interface{}{}
				}
				hasCalls = true
				msg.Content = append(msg.Content, b)
			}
		}
	}
	msg.StopReason = normalizeStopReason(a.StopReason, hasCalls)
	return msg
}

func normalizeStopReason(raw string, hasCalls bool) message.StopReason {
	if hasCalls {
		return message.StopToolInvoke
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "length", "max_tokens":
		return message.StopLength
	case "tool_invoke", "tool_calls", "tool_use", "function_call":
		return message.StopToolInvoke
	case "aborted":
		return message.StopAborted
	case "error":
		return message.StopError
	case "stop", "end_turn", "":
		return message.StopEnd
	}
	return message.StopError
}

func mergeMeta(dst, src map[string]json.RawMessage) map[string]json.RawMessage {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]json.RawMessage, len(src))
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
