package figaro

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/wirelog"
)

var itlBoundsMS = []int64{1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 2500}

type roundClock struct {
	start time.Time

	mu                                                      sync.Mutex
	firstThinking, firstText, firstTool, firstToolArg, last time.Time
	firstFilled, lastFilled                                 time.Time
	deltas, toolArgDeltas                                   int64
	gaps                                                    [12]int64
}

func newRoundClock(start time.Time) *roundClock { return &roundClock{start: start} }

func (c *roundClock) delta(kind message.ContentType, size int) {
	if c == nil {
		return
	}
	now := time.Now()
	c.mu.Lock()
	c.filled(now, size)
	if kind == message.ContentThinking {
		stampFirst(&c.firstThinking, now)
	} else {
		stampFirst(&c.firstText, now)
	}
	c.deltas++
	c.gap(now)
	c.mu.Unlock()
}

func (c *roundClock) toolStart() {
	if c == nil {
		return
	}
	now := time.Now()
	c.mu.Lock()
	stampFirst(&c.firstTool, now)
	c.mu.Unlock()
}

func (c *roundClock) toolArg(size int) {
	if c == nil {
		return
	}
	now := time.Now()
	c.mu.Lock()
	c.filled(now, size)
	stampFirst(&c.firstToolArg, now)
	c.toolArgDeltas++
	c.gap(now)
	c.mu.Unlock()
}

func (c *roundClock) filled(now time.Time, size int) {
	if size == 0 {
		return
	}
	stampFirst(&c.firstFilled, now)
	c.lastFilled = now
}

const (
	streamedMinTokens     = 32
	streamedTokensPerMSAt = 10
)

func streamedRound(outputTokens int, span time.Duration) bool {
	if outputTokens < streamedMinTokens {
		return true
	}
	return span.Seconds()*1000*streamedTokensPerMSAt >= float64(outputTokens)
}

func stampFirst(t *time.Time, now time.Time) {
	if t.IsZero() {
		*t = now
	}
}

func (c *roundClock) gap(now time.Time) {
	if !c.last.IsZero() {
		c.gaps[itlBucket(now.Sub(c.last))]++
	}
	c.last = now
}

func itlBucket(d time.Duration) int {
	ms := d.Milliseconds()
	for i, b := range itlBoundsMS {
		if ms < b {
			return i
		}
	}
	return len(itlBoundsMS)
}

func (c *roundClock) annotate(span trace.Span, outputTokens int) {
	if c == nil || !span.IsRecording() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var first time.Time
	for _, m := range []struct {
		name string
		at   time.Time
	}{
		{"first_thinking", c.firstThinking},
		{"first_text", c.firstText},
		{"first_tool", c.firstTool},
		{"first_tool_arg", c.firstToolArg},
	} {
		if m.at.IsZero() {
			continue
		}
		span.AddEvent(m.name, trace.WithTimestamp(m.at))
		if first.IsZero() || m.at.Before(first) {
			first = m.at
		}
	}
	attrs := []attribute.KeyValue{
		attribute.Int64("round.output_deltas", c.deltas),
		attribute.Int64("round.tool_arg_deltas", c.toolArgDeltas),
	}
	if !first.IsZero() {
		attrs = append(attrs, attribute.Int64("round.first_output_ms", first.Sub(c.start).Milliseconds()))
	}
	if !c.last.IsZero() && !first.IsZero() {
		span.AddEvent("last_output", trace.WithTimestamp(c.last))
		attrs = append(attrs, attribute.Int64("round.stream_ms", c.last.Sub(first).Milliseconds()))
	}
	if !c.firstFilled.IsZero() {
		out := c.lastFilled.Sub(c.firstFilled)
		attrs = append(attrs,
			attribute.Float64("round.output_span_ms", float64(out.Microseconds())/1000),
			attribute.Bool("round.streamed", streamedRound(outputTokens, out)))
	}
	if c.deltas+c.toolArgDeltas > 1 {
		attrs = append(attrs,
			attribute.Int64Slice("round.itl_bounds_ms", itlBoundsMS),
			attribute.Int64Slice("round.itl_counts", c.gaps[:]))
	}
	span.SetAttributes(attrs...)
}

var httpStatusTail = regexp.MustCompile(`\((\d{3})\)\s*$`)

func roundErrorClass(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if m := httpStatusTail.FindStringSubmatch(err.Error()); m != nil {
		return "http_" + m[1]
	}
	return wirelog.ErrorClass(err)
}

func endRound(span trace.Span, clock *roundClock, out *message.Message, sendErr, roundErr error) {
	defer span.End()
	if !span.IsRecording() {
		return
	}
	tokens := 0
	if out != nil && out.Usage != nil {
		tokens = out.Usage.OutputTokens
	}
	clock.annotate(span, tokens)
	if out != nil {
		span.SetAttributes(attribute.String("gen_ai.response.finish_reason", string(out.StopReason)))
		if u := out.Usage; u != nil {
			span.SetAttributes(
				attribute.Int("gen_ai.usage.input_tokens", u.InputTokens),
				attribute.Int("gen_ai.usage.output_tokens", u.OutputTokens),
				attribute.Int("gen_ai.usage.cache_read.input_tokens", u.CacheReadTokens),
				attribute.Int("gen_ai.usage.cache_creation.input_tokens", u.CacheWriteTokens))
		}
	}
	err := sendErr
	if err == nil {
		err = roundErr
	}
	if err != nil {
		class := roundErrorClass(err)
		span.SetAttributes(attribute.String("error.type", class))
		span.SetStatus(codes.Error, class)
	}
}
