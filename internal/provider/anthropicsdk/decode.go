package anthropicsdk

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
)

// validAccumulatedBlock reports whether an accumulated block is
// API-legal to replay. Shared by the IR decoder (so the sealed message
// matches the in-flight asm, which never creates a node for empty
// text/thinking — keeping them shifts later block indices and
// duplicates the live render; empty summarized-thinking blocks are the
// common case with Display: Summarized) and the cache path (so an
// open+close-with-no-deltas block never persists without its required
// field).
func validAccumulatedBlock(b anthropic.ContentBlockUnion) bool {
	switch b.Type {
	case "text":
		return strings.TrimSpace(b.Text) != ""
	case "thinking":
		return strings.TrimSpace(b.Thinking) != ""
	case "redacted_thinking":
		return b.Data != ""
	case "tool_use":
		return b.ID != "" && len(b.Input) > 0
	case "":
		return false
	}
	return true
}

// cacheableAccumulatedBlock is the wire-replay predicate — wider than
// validAccumulatedBlock for thinking: a signed empty-summary block must
// replay (the API requires the thinking block leading a tool-use
// assistant) even though the renderer skips it. fatal marks the whole
// message uncacheable: a tool_use whose input is not a JSON object
// cannot replay, and dropping just that block would orphan its
// tool_result on the wire.
func cacheableAccumulatedBlock(b anthropic.ContentBlockUnion) (keep, fatal bool) {
	switch b.Type {
	case "text":
		return strings.TrimSpace(b.Text) != "", false
	case "thinking":
		return b.Signature != "" || strings.TrimSpace(b.Thinking) != "", false
	case "redacted_thinking":
		return b.Data != "", false
	case "tool_use":
		if b.ID == "" {
			return false, false
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(b.Input, &obj) != nil {
			return false, true
		}
		return true, false
	case "":
		return false, false
	}
	return true, false
}

// decodeAssistantMessage projects an SDK Message (the final
// accumulated assistant turn) to the figaro IR.
func decodeAssistantMessage(m anthropic.Message) message.Message {
	// model/provider are not on the IR message — they live in the
	// chalkboard (system.model / system.provider), derived on read.
	out := message.Message{
		Role: message.RoleOutput,
	}
	for _, b := range m.Content {
		if !validAccumulatedBlock(b) {
			continue
		}
		switch v := b.AsAny().(type) {
		case anthropic.TextBlock:
			out.Content = append(out.Content, message.Content{Type: message.ContentProse, Text: v.Text})
		case anthropic.ThinkingBlock:
			// Text only — for display and other providers. The signature lives
			// in the cached wire bytes (acc.ToParam), never the IR.
			out.Content = append(out.Content, message.Content{Type: message.ContentThinking, Text: v.Thinking})
		case anthropic.ToolUseBlock:
			out.Content = append(out.Content, message.Content{
				Type:       message.ContentToolInvoke,
				ToolCallID: v.ID,
				ToolName:   v.Name,
				Arguments:  asArgsMap(v.Input),
			})
		}
	}
	out.StopReason = mapStopReason(m.StopReason)
	out.Usage = &message.Usage{
		InputTokens:      int(m.Usage.InputTokens),
		OutputTokens:     int(m.Usage.OutputTokens),
		CacheReadTokens:  int(m.Usage.CacheReadInputTokens),
		CacheWriteTokens: int(m.Usage.CacheCreationInputTokens),
	}
	return out
}

// asArgsMap converts a tool_use Input (json.RawMessage) to a Go map.
func asArgsMap(input json.RawMessage) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return nil
	}
	return m
}

func mapStopReason(s anthropic.StopReason) message.StopReason {
	switch s {
	case anthropic.StopReasonEndTurn, anthropic.StopReasonStopSequence:
		return message.StopEnd
	case anthropic.StopReasonMaxTokens:
		return message.StopLength
	case anthropic.StopReasonToolUse:
		return message.StopToolInvoke
	}
	return ""
}

// repairAccumulatedToolInput escapes control characters the model left RAW
// inside the just-closed tool_use block's input, in place, before the SDK
// marshals it.
//
// MEASURED failure: a turn dies at content_block_stop with
//
//	accumulate: error converting content block to JSON: json: error calling
//	MarshalJSON for type json.RawMessage: invalid character '\t' in string literal
//
// The mechanism is exact. The SDK appends every `input_json_delta` chunk to
// `acc.Content[i].Input` (a json.RawMessage), then at content_block_stop calls
// json.Marshal on the block — and marshaling a RawMessage VALIDATES it. JSON
// forbids unescaped bytes below 0x20 inside a string, so one literal TAB in a
// tool argument (an `edit` carrying Go source is the reliable way to produce
// one) takes down the whole turn, including every tool call already streamed.
//
// Dropping the fine-grained-tool-streaming beta (see auth.go) removed the
// STRUCTURAL half of this: chunks now reassemble into complete JSON. It cannot
// remove this half — the buffering guarantee is about where the chunk
// boundaries fall, not about how the model escaped its own string contents.
//
// So the bytes are repaired rather than mourned: the turn's work is already
// done and the only thing standing between it and the user is an escape the
// model owed us. Gated on json.Valid, so a well-formed input costs one scan
// and is never rewritten.
func repairAccumulatedToolInput(cb *anthropic.ContentBlockUnion) bool {
	if cb.Type != "tool_use" || len(cb.Input) == 0 || json.Valid(cb.Input) {
		return false
	}
	fixed, changed := escapeRawControlChars(cb.Input)
	if !changed || !json.Valid(fixed) {
		// Not the control-char defect, or not the ONLY defect. Leave the
		// buffer as it stands so the failure dump reports what really
		// arrived rather than a half-mended copy of it.
		return false
	}
	cb.Input = fixed
	return true
}

// escapeRawControlChars rewrites bytes below 0x20 that appear INSIDE a JSON
// string literal into their escape sequences, and reports whether it changed
// anything.
//
// The string-literal state matters: the same tab between two tokens is legal
// whitespace and must survive untouched, or a pretty-printed tool input would
// be corrupted by the very function meant to save it. Bytes outside strings
// are copied verbatim, and the input is returned unaliased when clean.
func escapeRawControlChars(in []byte) ([]byte, bool) {
	var out []byte
	inString, escaped := false, false
	for i := 0; i < len(in); i++ {
		c := in[i]
		switch {
		case !inString:
			if c == '"' {
				inString = true
			}
		case escaped:
			escaped = false
		case c == '\\':
			escaped = true
		case c == '"':
			inString = false
		case c < 0x20:
			if out == nil {
				out = append(out, in[:i]...)
			}
			out = append(out, escapeControl(c)...)
			continue
		}
		if out != nil {
			out = append(out, c)
		}
	}
	if out == nil {
		return in, false
	}
	return out, true
}

func escapeControl(c byte) string {
	switch c {
	case '\t':
		return `\t`
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	case '\b':
		return `\b`
	case '\f':
		return `\f`
	}
	const hex = "0123456789abcdef"
	return `\u00` + string([]byte{hex[c>>4], hex[c&0xf]})
}

// quarantineMalformedToolInput rescues a turn whose tool_use block carries
// arguments that are not JSON, by replacing the wreckage with a legal envelope
// that says so and keeps the bytes.
//
// It runs only after repairAccumulatedToolInput has already declined — i.e.
// the input is broken by more than raw control characters. MEASURED, four
// times in one day, always the same shape: an `edit` whose Go source arrived
// with its tabs, newlines AND quotes unescaped, so a string value never closes
// and the next key runs into it. Escaping harder cannot recover that: the
// boundary between a value and the key after it is gone, and a guess would
// write the wrong bytes into the user's source file.
//
// So the call is not repaired, it is REFUSED — and refusing costs one tool
// call instead of an entire turn. The envelope is a JSON object, which is what
// keeps the wire legal: the assistant message replays with its tool_use, its
// tool_result pairs with it, and the cache path (cacheableAccumulatedBlock)
// sees an object rather than a fatal block. Downstream, message.MalformedArgs
// is the whole contract: it never executes, and the model is told to resend.
func quarantineMalformedToolInput(ctx context.Context, acc *anthropic.Message, cause error) bool {
	for i := len(acc.Content) - 1; i >= 0; i-- {
		cb := &acc.Content[i]
		if cb.Type != "tool_use" || json.Valid(cb.Input) {
			continue
		}
		envelope, err := json.Marshal(message.MalformedArgs(string(cb.Input)))
		if err != nil {
			return false
		}
		figOtel.Event(ctx, "provider.tool_use.input_quarantined",
			attribute.String("tool_call_id", cb.ID),
			attribute.String("tool_name", cb.Name),
			attribute.Int("input_len", len(cb.Input)),
			attribute.String("cause", cause.Error()),
		)
		cb.Input = envelope
		return true
	}
	return false
}
