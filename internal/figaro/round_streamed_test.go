package figaro_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
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

// Copilot's messages route holds a tool call's arguments and sends them in one
// burst at the end of the round (traced on 11-08: an empty input_json_delta
// at once, then nothing for 2.7 to 5.9 s, then every argument within a few
// ms). figscope divides that round's output span by its tokens and reads an
// impossible TPOT. The round says how its output arrived so figscope can
// leave such rounds out.

type scriptedRound struct {
	push   func(provider.Bus)
	tokens int
}

func (scriptedRound) Name() string                                         { return "mock" }
func (scriptedRound) Fingerprint() string                                  { return "mock/v0" }
func (scriptedRound) SetModel(string)                                      {}
func (scriptedRound) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }

func (p scriptedRound) Send(_ context.Context, _ provider.SendInput, bus provider.Bus) error {
	p.push(bus)
	msg := message.Message{
		Role:       message.RoleOutput,
		Content:    []message.Content{message.TextContent("ok")},
		StopReason: message.StopEnd,
		Usage:      &message.Usage{InputTokens: 7, OutputTokens: p.tokens},
	}
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

func roundSpan(t *testing.T, p provider.Provider) sdktrace.ReadOnlySpan {
	t.Helper()
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
	be, id := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{Backend: be, Projector: uiir.New(nil), ID: id, Provider: p, Tools: tool.NewRegistry(), Form: cb})
	t.Cleanup(a.Kill)
	ch, unsub := subscribeChan(a)
	defer unsub()
	a.SubmitPrompt(rpc.QuaRequest{Text: "go"})
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
	for _, s := range rec.Ended() {
		if s.Name() == "figaro.round" {
			return s
		}
	}
	t.Fatal("no figaro.round span")
	return nil
}

func TestARoundWhoseToolArgumentsArriveInOneBurstIsNotStreamed(t *testing.T) {
	span := roundSpan(t, scriptedRound{tokens: 300, push: func(bus provider.Bus) {
		bus.PushToolInvokeStart("toolu_1", "bash")
		bus.PushToolInvokeDelta("toolu_1", "")
		time.Sleep(30 * time.Millisecond)
		for i := 0; i < 200; i++ {
			bus.PushToolInvokeDelta("toolu_1", `"w `)
		}
	}})
	attrs := span.Attributes()
	spanMS, ok := attr(attrs, "round.output_span_ms")
	if !ok || spanMS.AsFloat64() >= 30 {
		t.Fatalf("round.output_span_ms = %v (present %t): the empty first delta opened the span", spanMS.AsInterface(), ok)
	}
	if v, ok := attr(attrs, "round.streamed"); !ok || v.AsBool() {
		t.Fatalf("round.streamed = %v (present %t), want false for 300 tokens in %.3f ms", v.AsInterface(), ok, spanMS.AsFloat64())
	}
}

func TestARoundWhoseTextStreamsIsStreamed(t *testing.T) {
	span := roundSpan(t, scriptedRound{tokens: 40, push: func(bus provider.Bus) {
		for _, w := range strings.Fields("one two three four") {
			bus.PushDelta(message.Content{Type: message.ContentProse, Text: w + " "})
			time.Sleep(5 * time.Millisecond)
		}
	}})
	attrs := span.Attributes()
	if v, ok := attr(attrs, "round.output_span_ms"); !ok || v.AsFloat64() < 10 {
		t.Fatalf("round.output_span_ms = %v (present %t), want the ~15 ms the deltas took", v.AsInterface(), ok)
	}
	if v, ok := attr(attrs, "round.streamed"); !ok || !v.AsBool() {
		t.Fatalf("round.streamed = %v (present %t), want true", v.AsInterface(), ok)
	}
}
