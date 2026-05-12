package anthropicsdk

import (
	"context"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/provider"
)

// drainStream consumes the SDK SSE stream, forwards deltas to the
// bus, and returns the assembled assistant message at message_stop.
//
// The SDK's MessageAccumulator could fold deltas for us, but we need
// to emit live PushDelta / PushToolUseStart / PushToolUseDelta calls,
// so we walk the events ourselves and accumulate in parallel.
func drainStream(ctx context.Context, stream *ssestream.Stream[anthropic.MessageStreamEventUnion], model string, bus provider.Bus) (message.Message, error) {
	acc := anthropic.Message{Model: anthropic.Model(model)}
	// Tracks per-tool-use indices that have already emitted a
	// first-delta telemetry event. Keyed by block index in acc.Content.
	seenInputDelta := map[int]bool{}

	for stream.Next() {
		if err := ctx.Err(); err != nil {
			return message.Message{}, err
		}
		event := stream.Current()

		// Side-effects fire on the pre-accumulator view so we can
		// observe "is this the first input_json_delta for the tool"
		// independent of accumulator state.
		switch v := event.AsAny().(type) {
		case anthropic.ContentBlockStartEvent:
			handleBlockStart(ctx, v, bus)
		case anthropic.ContentBlockDeltaEvent:
			handleBlockDelta(ctx, v, &acc, bus, seenInputDelta)
		case anthropic.ContentBlockStopEvent:
			handleBlockStop(ctx, v, &acc)
		case anthropic.MessageStopEvent:
			// Accumulator captures stop_reason + usage; no side effect.
		}

		if err := acc.Accumulate(event); err != nil {
			return message.Message{}, fmt.Errorf("accumulate: %w", err)
		}
	}
	if err := stream.Err(); err != nil {
		return message.Message{}, err
	}
	return decodeAssistantMessage(acc), nil
}

func handleBlockStart(ctx context.Context, ev anthropic.ContentBlockStartEvent, bus provider.Bus) {
	switch cb := ev.ContentBlock.AsAny().(type) {
	case anthropic.ToolUseBlock:
		bus.PushToolUseStart(cb.ID, cb.Name)
		figOtel.Event(ctx, "provider.tool_use.block_start",
			attribute.String("tool_call_id", cb.ID),
			attribute.String("tool_name", cb.Name),
		)
	}
}

func handleBlockDelta(ctx context.Context, ev anthropic.ContentBlockDeltaEvent, acc *anthropic.Message, bus provider.Bus, seen map[int]bool) {
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
		bus.PushToolUseDelta(owner.ID, d.PartialJSON)
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

func handleBlockStop(ctx context.Context, ev anthropic.ContentBlockStopEvent, acc *anthropic.Message) {
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
	)
}

