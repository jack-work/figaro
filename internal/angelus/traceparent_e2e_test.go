package angelus_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/provider"
)

const traceparentPolicy = `
[roles.owner]
grants = ["*:*"]

[roles.prompter]
grants = ["send:*", "read:*", "listen:*"]

[[bind]]
role = "owner"
principals = ["user:SELF"]

[[bind]]
role = "prompter"
principals = ["user:guest", "sandbox:dev-3"]
`

type traceRecorder struct{ traces chan trace.TraceID }

func (*traceRecorder) Name() string                                         { return "mock" }
func (*traceRecorder) Fingerprint() string                                  { return "trace-recorder/v1" }
func (*traceRecorder) SetModel(string)                                      {}
func (*traceRecorder) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }

func (p *traceRecorder) Send(ctx context.Context, _ provider.SendInput, bus provider.Bus) error {
	msg := message.Message{Role: message.RoleOutput, Content: []message.Content{message.TextContent("ok")}, StopReason: message.StopEnd}
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	p.traces <- trace.SpanContextFromContext(ctx).TraceID()
	return nil
}

// Through the daemon's own dispatch: the turn joins the caller's trace only
// for the owner and a well-formed traceparent.
func TestATurnJoinsOnlyTheOwnersTrace(t *testing.T) {
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	p := &traceRecorder{traces: make(chan trace.TraceID, 1)}
	r := newRigProviding(t, traceparentPolicy, "", func(string, provider.Knobs) (provider.Provider, error) { return p, nil })
	resp, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	const client = "4bf92f3577b34da6a3ce929d0e0e4736"
	valid := "00-" + client + "-00f067aa0ba902b7-01"

	turn := func(ctx context.Context, tp string) trace.TraceID {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"figaro_id": resp.FigaroID, "text": "hi", "traceparent": tp})
		if _, err := r.h[rpc.MethodQua](ctx, raw); err != nil {
			t.Fatalf("qua: %v", err)
		}
		select {
		case got := <-p.traces:
			return got
		case <-time.After(10 * time.Second):
			t.Fatal("the turn never reached the provider")
		}
		return trace.TraceID{}
	}

	if got := turn(asOwner(), valid); got.String() != client {
		t.Fatalf("the owner's valid traceparent: turn ran in %s, want %s", got, client)
	}
	for name, c := range map[string]struct {
		ctx context.Context
		tp  string
	}{
		"a guest":           {as("user", "guest"), valid},
		"a box":             {as("sandbox", "dev-3"), valid},
		"a malformed value": {asOwner(), "00-" + client + "-0000000000000000-01"},
	} {
		if got := turn(c.ctx, c.tp); got.String() == client || !got.IsValid() {
			t.Errorf("%s: turn ran in %s; it must start its own trace", name, got)
		}
	}
}
