package anthropicsdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/provider"
)

// dumpBytes is the per-side byte budget for span events that carry
// captured tool-input bytes. Keeps spans bounded while still giving
// us enough to reconstruct the corruption upstream.
const dumpBytes = 2048

// drainStream consumes the SDK SSE stream, forwards deltas to the
// bus, and returns the assembled assistant message at message_stop.
//
// The SDK's MessageAccumulator could fold deltas for us, but we need
// to emit live PushDelta / PushToolInvokeStart / PushToolInvokeDelta calls,
// so we walk the events ourselves and accumulate in parallel.
//
// Observability: per-tool-block byte tallies are reported on
// `provider.tool_use.block_stop`. If `acc.Accumulate` rejects a
// stream (typically because the model emitted malformed
// `input_json_delta` chunks that don't reassemble into valid JSON),
// the offending block's accumulated bytes are captured on the span
// via `provider.accumulate.failed` so the failure is diagnosable
// from `traces.jsonl` alone — no wire-dir replay required.
func drainStream(ctx context.Context, stream *ssestream.Stream[anthropic.MessageStreamEventUnion], model string, bus provider.Bus) (message.Message, anthropic.Message, error) {
	acc := anthropic.Message{Model: anthropic.Model(model)}
	// Tracks per-tool-use indices that have already emitted a
	// first-delta telemetry event. Keyed by block index in acc.Content.
	seenInputDelta := map[int]bool{}
	// Per-block running byte count of accumulated input_json_delta
	// payloads. Reported on block_stop and dumped on failure.
	bytesByIdx := map[int]int64{}

	for stream.Next() {
		if err := ctx.Err(); err != nil {
			return message.Message{}, anthropic.Message{}, err
		}
		event := stream.Current()

		// Side-effects fire on the pre-accumulator view so we can
		// observe "is this the first input_json_delta for the tool"
		// independent of accumulator state.
		switch v := event.AsAny().(type) {
		case anthropic.ContentBlockStartEvent:
			handleBlockStart(ctx, v, bus)
		case anthropic.ContentBlockDeltaEvent:
			handleBlockDelta(ctx, v, &acc, bus, seenInputDelta, bytesByIdx)
		case anthropic.ContentBlockStopEvent:
			handleBlockStop(ctx, v, &acc, bytesByIdx, bus)
		case anthropic.MessageStopEvent:
			// Accumulator captures stop_reason + usage; no side effect.
		}

		if err := acc.Accumulate(event); err != nil {
			recordAccumulateFailure(ctx, event, &acc, bytesByIdx, err)
			return message.Message{}, anthropic.Message{}, fmt.Errorf("accumulate: %w", err)
		}
	}
	if err := stream.Err(); err != nil {
		figOtel.RecordError(ctx, "provider.stream.error", err,
			attribute.Int("n_content_blocks", len(acc.Content)),
		)
		return message.Message{}, anthropic.Message{}, err
	}
	figOtel.Event(ctx, "provider.stream.complete",
		attribute.Int("n_content_blocks", len(acc.Content)),
		attribute.String("stop_reason", string(acc.StopReason)),
	)
	return decodeAssistantMessage(acc), acc, nil
}

func handleBlockStart(ctx context.Context, ev anthropic.ContentBlockStartEvent, bus provider.Bus) {
	switch cb := ev.ContentBlock.AsAny().(type) {
	case anthropic.ToolUseBlock:
		bus.PushToolInvokeStart(cb.ID, cb.Name)
		figOtel.Event(ctx, "provider.tool_use.block_start",
			attribute.String("tool_call_id", cb.ID),
			attribute.String("tool_name", cb.Name),
		)
	}
}

func handleBlockDelta(ctx context.Context, ev anthropic.ContentBlockDeltaEvent, acc *anthropic.Message, bus provider.Bus, seen map[int]bool, bytesByIdx map[int]int64) {
	switch d := ev.Delta.AsAny().(type) {
	case anthropic.TextDelta:
		if d.Text != "" {
			bus.PushDelta(message.Content{Type: message.ContentText, Text: d.Text})
		}
	case anthropic.ThinkingDelta:
		if d.Thinking != "" {
			bus.PushDelta(message.Content{Type: message.ContentThinking, Text: d.Thinking})
		}
	case anthropic.InputJSONDelta:
		if d.PartialJSON == "" {
			return
		}
		idx := int(ev.Index)
		if idx < 0 || idx >= len(acc.Content) {
			return
		}
		owner := acc.Content[idx]
		if owner.Type != "tool_use" {
			return
		}
		bus.PushToolInvokeDelta(owner.ID, d.PartialJSON)
		bytesByIdx[idx] += int64(len(d.PartialJSON))
		if !seen[idx] {
			seen[idx] = true
			figOtel.Event(ctx, "provider.tool_use.first_input_delta",
				attribute.String("tool_call_id", owner.ID),
				attribute.String("tool_name", owner.Name),
				attribute.Int("bytes", len(d.PartialJSON)),
			)
		}
	}
}

func handleBlockStop(ctx context.Context, ev anthropic.ContentBlockStopEvent, acc *anthropic.Message, bytesByIdx map[int]int64, bus provider.Bus) {
	idx := int(ev.Index)
	if idx < 0 || idx >= len(acc.Content) {
		return
	}
	b := acc.Content[idx]
	if b.Type != "tool_use" {
		return
	}
	figOtel.Event(ctx, "provider.tool_use.block_stop",
		attribute.String("tool_call_id", b.ID),
		attribute.String("tool_name", b.Name),
		attribute.Int64("input_bytes", bytesByIdx[idx]),
	)
	// Speculative dispatch: the SDK has finished accumulating this
	// tool_use block's input JSON. Decode it and signal the harness so
	// the tool can begin executing in parallel with the rest of the
	// stream.
	var args map[string]interface{}
	if raw := []byte(b.Input); len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil || args == nil {
			return
		}
	} else {
		args = map[string]interface{}{}
	}
	bus.PushToolReady(message.Content{
		Type:       message.ContentToolInvoke,
		ToolCallID: b.ID,
		ToolName:   b.Name,
		Arguments:  args,
	})
}

// recordAccumulateFailure dumps the most-likely offending block's
// accumulated bytes onto the active span. The SDK appends each
// `input_json_delta` chunk to `acc.Content[idx].Input` *before*
// calling `json.Marshal` on the block at `content_block_stop`; when
// that marshal fails (json.RawMessage validates), the malformed
// buffer is still sitting in `Input` — we just have to read it.
//
// We pick the last tool_use block as the offender heuristic: text
// and thinking blocks don't route through json.RawMessage marshaling
// and so can't be the cause of this error class.
func recordAccumulateFailure(ctx context.Context, ev anthropic.MessageStreamEventUnion, acc *anthropic.Message, bytesByIdx map[int]int64, cause error) {
	attrs := []attribute.KeyValue{
		attribute.String("event_type", ev.Type),
		attribute.Int("n_content_blocks", len(acc.Content)),
	}
	for i := len(acc.Content) - 1; i >= 0; i-- {
		b := acc.Content[i]
		if b.Type != "tool_use" {
			continue
		}
		raw := []byte(b.Input)
		attrs = append(attrs,
			attribute.Int("offender.idx", i),
			attribute.String("offender.tool_call_id", b.ID),
			attribute.String("offender.tool_name", b.Name),
			attribute.Int("offender.input_len", len(raw)),
			attribute.Int64("offender.bytes_streamed", bytesByIdx[i]),
			attribute.String("offender.input_head", safeHead(raw, dumpBytes)),
			attribute.String("offender.input_tail", safeTail(raw, dumpBytes)),
		)
		if off := jsonSyntaxOffset(cause); off >= 0 {
			attrs = append(attrs,
				attribute.Int64("offender.err_offset", off),
				attribute.String("offender.err_window", windowAround(raw, int(off), 200)),
			)
		}
		break
	}
	figOtel.RecordError(ctx, "provider.accumulate.failed", cause, attrs...)
}

func safeHead(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n])
}

func safeTail(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[len(b)-n:])
}

func windowAround(b []byte, off, radius int) string {
	lo, hi := off-radius, off+radius
	if lo < 0 {
		lo = 0
	}
	if hi > len(b) {
		hi = len(b)
	}
	if lo > hi {
		return ""
	}
	return string(b[lo:hi])
}

// jsonSyntaxOffset unwraps an error chain looking for a
// *json.SyntaxError and returns its byte Offset, or -1 if not found.
// The SDK wraps the marshal error with fmt.Errorf, so a naive type
// assertion fails — errors.As walks the chain for us.
func jsonSyntaxOffset(err error) int64 {
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return se.Offset
	}
	return -1
}
