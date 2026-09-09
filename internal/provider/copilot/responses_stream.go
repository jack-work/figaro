package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"golang.org/x/net/websocket"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
)

type responseObject struct {
	ID                string            `json:"id"`
	Output            []json.RawMessage `json:"output"`
	Status            string            `json:"status"`
	Usage             responseUsage     `json:"usage"`
	Error             json.RawMessage   `json:"error"`
	IncompleteDetails struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

type responseUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

type responseStreamEvent struct {
	Type      string             `json:"type"`
	Delta     string             `json:"delta"`
	Item      responseOutputItem `json:"item"`
	ItemID    string             `json:"item_id"`
	CallID    string             `json:"call_id"`
	Name      string             `json:"name"`
	Arguments json.RawMessage    `json:"arguments"`
	Response  responseObject     `json:"response"`
	Error     json.RawMessage    `json:"error"`
	// Absent is NOT index zero: an uncorrelated delta must never enter the
	// first tool's arguments merely because Go zero-initialized this field.
	OutputIndex  *int `json:"output_index"`
	ContentIndex int  `json:"content_index"`
	SummaryIndex int  `json:"summary_index"`
}

type responseOutputItem struct {
	Type      string            `json:"type"`
	ID        string            `json:"id"`
	Role      string            `json:"role"`
	Status    string            `json:"status"`
	Content   []responseContent `json:"content"`
	Summary   []responseContent `json:"summary"`
	CallID    string            `json:"call_id"`
	Name      string            `json:"name"`
	Arguments json.RawMessage   `json:"arguments"`
}

// One request's accumulator, never a remote session. Completed native items
// survive cancellation/failure byte-for-byte, including encrypted reasoning.
// Unfinished text is recoverable; an unfinished function call is not runnable.
type responsePartial struct {
	parts   []*responsePart
	indexed map[int]*responsePart
}

type responsePart struct {
	index *int
	kind  string
	raw   json.RawMessage
	call  *responseCall
	text  map[int]*strings.Builder
}

func (p *responsePartial) part(event responseStreamEvent, kind string, call *responseCall) *responsePart {
	if call != nil && call.part != nil {
		return call.part
	}
	if event.OutputIndex != nil && p.indexed != nil {
		if part := p.indexed[*event.OutputIndex]; part != nil {
			return part
		}
	}
	// Some proxies omit indices on prose deltas. Keep consecutive fragments
	// together, without conflating separate indexed messages/reasoning items.
	if event.OutputIndex == nil && call == nil && len(p.parts) > 0 {
		last := p.parts[len(p.parts)-1]
		if last.index == nil && last.kind == kind && last.raw == nil {
			return last
		}
	}
	part := &responsePart{index: event.OutputIndex, kind: kind, call: call, text: map[int]*strings.Builder{}}
	p.parts = append(p.parts, part)
	if event.OutputIndex != nil {
		if p.indexed == nil {
			p.indexed = map[int]*responsePart{}
		}
		p.indexed[*event.OutputIndex] = part
	}
	if call != nil {
		call.part = part
	}
	return part
}

func (p *responsePart) appendText(index int, text string) {
	b := p.text[index]
	if b == nil {
		b = &strings.Builder{}
		p.text[index] = b
	}
	b.WriteString(text)
}

func (p *responsePartial) outputItems() []json.RawMessage {
	parts := append([]*responsePart(nil), p.parts...)
	// Indexed items are ordered by the protocol. Unindexed legacy events
	// retain arrival order; do not invent an index for an omitted field.
	allIndexed := true
	for _, part := range parts {
		if part.index == nil {
			allIndexed = false
			break
		}
	}
	if allIndexed {
		sort.SliceStable(parts, func(i, j int) bool { return *parts[i].index < *parts[j].index })
	}
	var out []json.RawMessage
	for _, part := range parts {
		if part.call != nil && !part.call.ready {
			continue
		}
		if part.raw != nil {
			out = append(out, part.raw)
			continue
		}
		var item any
		if call := part.call; call != nil {
			item = map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": call.arguments.String()}
		} else {
			idxs := make([]int, 0, len(part.text))
			for i := range part.text {
				idxs = append(idxs, i)
			}
			sort.Ints(idxs)
			var content []map[string]string
			kind := "output_text"
			if part.kind == "reasoning" {
				kind = "summary_text"
			}
			for _, i := range idxs {
				if text := part.text[i].String(); text != "" {
					content = append(content, map[string]string{"type": kind, "text": text})
				}
			}
			if len(content) == 0 {
				continue
			}
			if part.kind == "reasoning" {
				item = map[string]any{"type": "reasoning", "summary": content}
			} else {
				item = map[string]any{"type": "message", "role": "assistant", "content": content}
			}
		}
		if b, err := json.Marshal(item); err == nil {
			out = append(out, quarantineResponseItem(b))
		}
	}
	return out
}

type responseCall struct {
	ID        string
	Name      string
	arguments strings.Builder
	ready     bool
	decoded   map[string]interface{}
	part      *responsePart
}

// Only metadata crosses this interface. In particular, no arguments, opaque
// IDs, native items, headers, or upstream error messages reach diagnostics.
type responseStreamObserver interface {
	Event(string, int)
	ArgumentDelta(int, bool)
	UnknownEvent()
}

type responseStreamState struct {
	calls   map[string]*responseCall
	items   map[string]*responseCall
	byIndex map[int]*responseCall
	partial responsePartial
	started bool
}

func newResponseStreamState() *responseStreamState {
	return &responseStreamState{calls: map[string]*responseCall{}, items: map[string]*responseCall{}, byIndex: map[int]*responseCall{}}
}

func readResponseStream(ctx context.Context, conn *websocket.Conn, bus provider.Bus, observers ...responseStreamObserver) (responseObject, error) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer close(done)
	state := newResponseStreamState()
	for {
		var raw json.RawMessage
		if err := websocket.JSON.Receive(conn, &raw); err != nil {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = fmt.Errorf("copilot responses: receive: %w", err)
			}
			return responseObject{Output: state.partial.outputItems()}, err
		}
		var event responseStreamEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return responseObject{Output: state.partial.outputItems()}, fmt.Errorf("copilot responses: decode event: %w", err)
		}
		for _, o := range observers {
			o.Event(event.Type, len(raw))
		}
		response, terminal, err := state.consume(event, raw, bus, observers)
		if terminal || err != nil {
			if err != nil {
				// Never retry a rejected request after any assistant output has
				// escaped: a completed call may already have had side effects.
				var rejected *responseAPIError
				if errors.As(err, &rejected) {
					rejected.retryable = !state.started
				}
				if response.Output == nil {
					response.Output = state.partial.outputItems()
				}
			}
			return response, err
		}
	}
}

func (s *responseStreamState) consume(event responseStreamEvent, raw json.RawMessage, bus provider.Bus, observers []responseStreamObserver) (responseObject, bool, error) {
	fail := func(err error) (responseObject, bool, error) { return responseObject{}, false, err }
	switch event.Type {
	case "response.output_text.delta":
		if event.Delta != "" {
			s.started = true
			s.partial.part(event, "message", nil).appendText(event.ContentIndex, event.Delta)
			bus.PushDelta(message.Content{Type: message.ContentProse, Text: event.Delta})
		}
	case "response.reasoning.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if event.Delta != "" {
			s.started = true
			index := event.ContentIndex
			if event.Type == "response.reasoning_summary_text.delta" {
				index = event.SummaryIndex
			}
			s.partial.part(event, "reasoning", nil).appendText(index, event.Delta)
			bus.PushDelta(message.Content{Type: message.ContentThinking, Text: event.Delta})
		}
	case "response.output_item.added", "response.output_item.done":
		s.started = true
		var call *responseCall
		if event.Item.Type == "function_call" {
			if event.Item.CallID == "" || event.Item.Name == "" {
				return fail(fmt.Errorf("copilot responses: function call missing call_id or name"))
			}
			call = s.calls[event.Item.CallID]
			newCall := call == nil
			if newCall {
				call = ensureResponseCall(s.calls, event.Item.CallID, event.Item.Name)
				bus.PushToolInvokeStart(call.ID, call.Name)
			} else if call.Name != event.Item.Name {
				return fail(fmt.Errorf("copilot responses: function name changed during stream"))
			}
			if event.OutputIndex != nil {
				if old := s.byIndex[*event.OutputIndex]; old != nil && old != call {
					return fail(fmt.Errorf("copilot responses: output index reused for another function"))
				}
				s.byIndex[*event.OutputIndex] = call
			}
			// Only the announced ID is retained. Copilot can re-encrypt it on
			// each delta; retaining every spelling grows a useless alias map.
			if event.Item.ID != "" && call.part == nil {
				s.items[event.Item.ID] = call
			}
			if event.Item.Arguments != nil {
				args := responseArgumentBytes(event.Item.Arguments)
				if err := call.setArguments(args); err != nil {
					return fail(err)
				}
				if newCall && event.Type == "response.output_item.added" && len(args) > 0 {
					bus.PushToolInvokeDelta(call.ID, string(args))
				}
			}
		}
		part := s.partial.part(event, event.Item.Type, call)
		if part.kind != event.Item.Type || part.call != call {
			return fail(fmt.Errorf("copilot responses: output index changed item identity"))
		}
		if event.Type == "response.output_item.done" {
			if call != nil {
				if err := readyResponseCall(call, bus); err != nil {
					return fail(err)
				}
			}
			var envelope struct {
				Item json.RawMessage `json:"item"`
			}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				return fail(err)
			}
			part.raw = quarantineResponseItem(envelope.Item)
		}
	case "response.function_call_arguments.delta":
		call := responseCallFor(s.calls, s.items, s.byIndex, event)
		for _, o := range observers {
			o.ArgumentDelta(len(event.Delta), call != nil)
		}
		if call != nil && event.Delta != "" {
			if call.ready {
				return fail(fmt.Errorf("copilot responses: argument delta after completed function call"))
			}
			call.arguments.WriteString(event.Delta)
			bus.PushToolInvokeDelta(call.ID, event.Delta)
		}
	case "response.function_call_arguments.done":
		call := responseCallFor(s.calls, s.items, s.byIndex, event)
		if call == nil {
			return fail(fmt.Errorf("copilot responses: completed arguments have no announced function call"))
		}
		if event.Arguments != nil {
			if err := call.setArguments(responseArgumentBytes(event.Arguments)); err != nil {
				return fail(err)
			}
		}
		if err := readyResponseCall(call, bus); err != nil {
			return fail(err)
		}
	case "response.completed", "response.incomplete", "response.failed", "response.cancelled", "response.canceled":
		response := event.Response
		if response.Status == "" {
			response.Status = strings.TrimPrefix(event.Type, "response.")
		}
		if response.Status == "failed" {
			return responseObject{Status: "failed"}, true, responseFailure(event.Error, response.Error)
		}
		if response.Status == "cancelled" || response.Status == "canceled" {
			return responseObject{Status: "cancelled"}, true, fmt.Errorf("copilot responses: response cancelled by provider")
		}
		if response.Status != "completed" && response.Status != "incomplete" {
			return fail(fmt.Errorf("copilot responses: unexpected terminal response status"))
		}
		if response.Status == "incomplete" && response.IncompleteDetails.Reason != "max_output_tokens" {
			// Steering requires following an automatic continuation on this
			// socket. We do not implement it, so report it rather than hanging
			// or mislabeling a steered/filtered response as a token limit.
			response.Output = nil
			return response, true, fmt.Errorf("copilot responses: incomplete response (%s)", safeResponseReason(response.IncompleteDetails.Reason))
		}
		if err := s.reconcile(&response); err != nil {
			return fail(err)
		}
		return response, true, nil
	case "error":
		return responseObject{Status: "failed"}, true, responseFailure(event.Error, event.Response.Error)
	case "response.created", "response.in_progress", "response.queued",
		"response.content_part.added", "response.content_part.done", "response.output_text.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
		"response.reasoning_summary_text.done", "response.reasoning_text.done", "response.reasoning.done":
		// Known lifecycle/aggregate events; their content is handled elsewhere.
	default:
		for _, o := range observers {
			o.UnknownEvent()
		}
	}
	return responseObject{}, false, nil
}

// The terminal object is authoritative, but it cannot retroactively change a
// call which was already dispatched. Incomplete call items never become ready
// merely because a truncated JSON prefix happens to parse.
func (s *responseStreamState) reconcile(response *responseObject) error {
	if response.Output == nil {
		response.Output = s.partial.outputItems()
	}
	out := make([]json.RawMessage, 0, len(response.Output))
	seen := map[string]bool{}
	for _, raw := range response.Output {
		var item responseOutputItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return fmt.Errorf("copilot responses: decode terminal item: %w", err)
		}
		if item.Type != "function_call" {
			out = append(out, raw)
			continue
		}
		if item.CallID == "" || item.Name == "" {
			return fmt.Errorf("copilot responses: terminal function missing call_id or name")
		}
		if seen[item.CallID] {
			return fmt.Errorf("copilot responses: duplicate terminal function call_id")
		}
		seen[item.CallID] = true
		call := s.calls[item.CallID]
		if item.Status == "in_progress" || item.Status == "incomplete" || (response.Status == "incomplete" && item.Status != "completed" && (call == nil || !call.ready)) {
			if call != nil && call.ready {
				return fmt.Errorf("copilot responses: completed function became incomplete")
			}
			continue
		}
		if call == nil {
			call = ensureResponseCall(s.calls, item.CallID, item.Name)
		}
		if call.Name != item.Name {
			return fmt.Errorf("copilot responses: terminal function name changed")
		}
		if err := call.setArguments(responseArgumentBytes(item.Arguments)); err != nil {
			return err
		}
		// Terminal-only calls run after the whole message is committed by the
		// agent, not speculatively while this object is still being validated.
		out = append(out, quarantineResponseItem(raw))
	}
	for id, call := range s.calls {
		if call.ready && !seen[id] {
			return fmt.Errorf("copilot responses: terminal output omitted a completed function call")
		}
	}
	response.Output = out
	return nil
}

func ensureResponseCall(calls map[string]*responseCall, id, name string) *responseCall {
	if id == "" {
		return nil
	}
	if call := calls[id]; call != nil {
		return call
	}
	call := &responseCall{ID: id, Name: name}
	calls[id] = call
	return call
}

func responseCallFor(calls map[string]*responseCall, items map[string]*responseCall, byIndex map[int]*responseCall, event responseStreamEvent) *responseCall {
	if event.CallID != "" {
		return calls[event.CallID]
	}
	if event.ItemID != "" {
		if call := items[event.ItemID]; call != nil {
			return call
		}
	}
	// Copilot re-encrypts item_id per event; output_index is its stable handle.
	if event.OutputIndex != nil {
		return byIndex[*event.OutputIndex]
	}
	return nil
}

func (call *responseCall) setArguments(raw []byte) error {
	if call.ready {
		args := responseArguments(string(raw))
		if !reflect.DeepEqual(args, call.decoded) {
			return fmt.Errorf("copilot responses: completed function arguments changed")
		}
		return nil
	}
	call.arguments.Reset()
	call.arguments.Write(raw)
	return nil
}

func responseArguments(raw string) map[string]interface{} {
	arguments, err := decodeResponseArguments(raw)
	if err != nil {
		return message.MalformedArgs(raw)
	}
	return arguments
}

func readyResponseCall(call *responseCall, bus provider.Bus) error {
	if call.ready {
		return nil
	}
	if call.ID == "" || call.Name == "" {
		return fmt.Errorf("copilot responses: function call missing call_id or name")
	}
	call.decoded = responseArguments(call.arguments.String())
	call.ready = true
	bus.PushToolReady(message.Content{Type: message.ContentToolInvoke, ToolCallID: call.ID, ToolName: call.Name, Arguments: call.decoded})
	return nil
}

func responseArgumentBytes(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		return []byte(encoded)
	}
	return raw
}

type responseAPIError struct {
	code      string
	status    int
	retryable bool
}

func (e *responseAPIError) Error() string {
	return fmt.Sprintf("copilot responses: provider rejected request (%s, status %d)", e.code, e.status)
}

func responseFailure(payloads ...json.RawMessage) error {
	for _, raw := range payloads {
		var e struct {
			Code   string `json:"code"`
			Type   string `json:"type"`
			Status int    `json:"status"`
		}
		if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &e) != nil {
			continue
		}
		code := e.Code
		if code == "" {
			code = e.Type
		}
		switch code {
		case "unauthorized", "invalid_api_key", "authentication_error", "rate_limit_exceeded", "rate_limit_error", "invalid_request_error", "invalid_request", "invalid_value", "unsupported_parameter", "model_not_found", "permission_denied", "server_error", "internal_error", "overloaded_error", "context_length_exceeded":
		default:
			code = "provider_error"
		}
		if e.Status < 400 || e.Status > 599 {
			e.Status = 0
		}
		return &responseAPIError{code: code, status: e.Status}
	}
	return &responseAPIError{code: "provider_error"}
}

func safeResponseReason(reason string) string {
	switch reason {
	case "max_output_tokens", "content_filter", "steered":
		return reason
	default:
		return "unknown reason"
	}
}

// Keep the native item's other fields verbatim, but give a rejected call a
// valid arguments object for replay. The sentinel retains the original bytes;
// the agent refuses execution and sends INVALID_JSON on the original call_id.
func quarantineResponseItem(raw json.RawMessage) json.RawMessage {
	var item responseOutputItem
	if json.Unmarshal(raw, &item) != nil || item.Type != "function_call" {
		return raw
	}
	arguments := string(responseArgumentBytes(item.Arguments))
	if _, err := decodeResponseArguments(arguments); err == nil {
		return raw
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return raw
	}
	envelope, _ := json.Marshal(message.MalformedArgs(arguments))
	fields["arguments"], _ = json.Marshal(string(envelope))
	out, err := json.Marshal(fields)
	if err != nil {
		return raw
	}
	return out
}
