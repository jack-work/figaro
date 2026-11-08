package cli

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
)

type tracedQuaer struct{ got rpc.QuaRequest }

func (q *tracedQuaer) ID() string { return "f1" }
func (q *tracedQuaer) QuaWith(_ context.Context, req rpc.QuaRequest) (int, bool, error) {
	q.got = req
	return 4, false, nil
}

// The submit span runs from the keystroke to the first painted reply, and
// the daemon joins it through the traceparent the request carries.
func TestSubmitSpanCarriesTraceparentAndEndsAtFirstReplyPaint(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	defer otel.SetTracerProvider(prev)

	q := &tracedQuaer{}
	if _, _, err := quaWith(context.Background(), q, rpc.QuaRequest{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if q.got.TraceParent == "" {
		t.Fatal("request carries no traceparent")
	}
	submitPainted("f1")
	stale := aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 4, Nodes: []livedoc.Node{{Type: livedoc.NodeProse}}}}}}
	submitFrame("f1", stale)
	submitPainted("f1")
	if len(rec.Ended()) != 0 {
		t.Fatal("ended before a reply frame for a turn after the cursor")
	}
	reply := aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 5, Live: &aria.Live{Nodes: []aria.NodeDelta{{ID: 0}}}}}}}
	submitFrame("f1", reply)
	submitPainted("f1")
	ended := rec.Ended()
	if len(ended) != 1 || ended[0].Name() != "cli.submit" {
		t.Fatalf("ended = %d", len(ended))
	}
	var names []string
	for _, e := range ended[0].Events() {
		names = append(names, e.Name)
	}
	if len(names) != 3 || names[0] != "rpc.acked" || names[1] != "reply.first_frame" || names[2] != "reply.first_paint" {
		t.Errorf("events = %v", names)
	}
}

func TestPageCarriesReplyIgnoresSteersAndOldTurns(t *testing.T) {
	steer := aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 9, Nodes: []livedoc.Node{{Role: livedoc.RoleInput}}}}}}
	if pageCarriesReply(steer, 3) {
		t.Error("a steering node is not a reply")
	}
	old := aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 3, Nodes: []livedoc.Node{{}}}}}}
	if pageCarriesReply(old, 3) {
		t.Error("a turn at the cursor predates the submit")
	}
}
