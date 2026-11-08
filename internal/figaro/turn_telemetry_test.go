package figaro_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

type streamingProvider struct{}

func (streamingProvider) Name() string                                         { return "mock" }
func (streamingProvider) Fingerprint() string                                  { return "mock/v0" }
func (streamingProvider) SetModel(string)                                      {}
func (streamingProvider) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }

func (streamingProvider) Send(_ context.Context, _ provider.SendInput, bus provider.Bus) error {
	bus.PushDelta(message.Content{Type: message.ContentThinking, Text: "hm"})
	time.Sleep(3 * time.Millisecond)
	for _, w := range []string{"one ", "two ", "three"} {
		bus.PushDelta(message.Content{Type: message.ContentProse, Text: w})
	}
	msg := message.Message{
		Role:       message.RoleOutput,
		Content:    []message.Content{message.TextContent("one two three")},
		StopReason: message.StopEnd,
		Usage:      &message.Usage{InputTokens: 7, OutputTokens: 5, CacheReadTokens: 1000, CacheWriteTokens: 30},
	}
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

// The dashboards read TTFT layers, usage and the gap histogram off these
// spans; a client's traceparent must make the turn part of its trace.
func TestTurnSpansCarryRoundTimingUsageAndTheClientTrace(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock-model-v1"`),
		"system.provider": json.RawMessage(`"mock"`),
	}, nil))
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend: testBE, Projector: uiir.New(nil), ID: testID,
		Provider: streamingProvider{}, Tools: tool.NewRegistry(), Form: cb,
	})
	t.Cleanup(a.Kill)

	ch, unsub := subscribeChan(a)
	defer unsub()
	const clientTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	a.SubmitPrompt(rpc.QuaRequest{Text: "count", TraceParent: "00-" + clientTrace + "-00f067aa0ba902b7-01"})
	deadline := time.After(20 * time.Second)
	for done := false; !done; {
		select {
		case n := <-ch:
			done = n.Method == rpc.MethodTurnDone
		case <-deadline:
			t.Fatal("timeout waiting for turn.done")
		}
	}
	a.Kill()

	var qua, round sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		switch s.Name() {
		case "figaro.qua":
			qua = s
		case "figaro.round":
			round = s
		}
	}
	if qua == nil || round == nil {
		t.Fatalf("want figaro.qua and figaro.round, got %d spans", len(rec.Ended()))
	}
	if got := qua.SpanContext().TraceID().String(); got != clientTrace {
		t.Errorf("figaro.qua trace = %s, want the client's %s", got, clientTrace)
	}
	if round.Parent().SpanID() != qua.SpanContext().SpanID() {
		t.Error("figaro.round is not a child of figaro.qua")
	}
	if _, ok := attr(qua.Attributes(), "figaro.turn"); !ok {
		t.Error("figaro.qua has no figaro.turn")
	}

	attrs := round.Attributes()
	for key, want := range map[string]int64{
		"gen_ai.usage.input_tokens":                7,
		"gen_ai.usage.output_tokens":               5,
		"gen_ai.usage.cache_read.input_tokens":     1000,
		"gen_ai.usage.cache_creation.input_tokens": 30,
		"round.output_deltas":                      4,
	} {
		v, ok := attr(attrs, key)
		if !ok || v.AsInt64() != want {
			t.Errorf("%s = %v (present %t), want %d", key, v.AsInterface(), ok, want)
		}
	}
	if v, _ := attr(attrs, "gen_ai.response.finish_reason"); v.AsString() != string(message.StopEnd) {
		t.Errorf("finish_reason = %q", v.AsString())
	}
	counts, ok := attr(attrs, "round.itl_counts")
	if !ok {
		t.Fatal("no round.itl_counts")
	}
	var gaps int64
	for _, n := range counts.AsInt64Slice() {
		gaps += n
	}
	if gaps != 3 {
		t.Errorf("itl gaps = %d, want 3 for 4 deltas", gaps)
	}

	at := map[string]time.Time{}
	for _, e := range round.Events() {
		at[e.Name] = e.Time
	}
	for _, name := range []string{"first_thinking", "first_text", "last_output"} {
		if at[name].IsZero() {
			t.Errorf("round has no %s event", name)
		}
	}
	if !at["first_thinking"].Before(at["first_text"]) {
		t.Error("first_thinking should precede first_text")
	}
}

func attr(kvs []attribute.KeyValue, key string) (attribute.Value, bool) {
	for _, kv := range kvs {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}
