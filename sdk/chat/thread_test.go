package chat

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
)

func sealedTurn(id uint64, reply string) aria.TurnPart {
	return aria.TurnPart{Turn: aria.Turn{ID: id, Inquiry: "q", Sealed: true, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Role: livedoc.RoleOutput, Markdown: reply}}}}
}

func drain(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var out []Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, e)
		case <-timeout:
			t.Fatalf("channel still open after %s", summary(out))
		}
	}
}

func TestAFinishedTurnReadBackEndsWithoutRunningIntoLaterTurns(t *testing.T) {
	th := newThread(New(transport.Endpoint{}), "x")
	ch := th.subscribe(context.Background(), &sub{after: 1, r: newReader(Cursor{Turn: 2, Text: 1})}, nil)
	var rt runtimeFeed
	th.readBack(rt.response("idle", 3, "", 0), sealedTurn(2, "ab"), sealedTurn(3, "later"))
	if got := summary(drain(t, ch)); got != `+"b" end:ab` {
		t.Fatalf("events: %s", got)
	}
}

func TestAFinishedTurnReadBackRunsOnIntoAnOpenNextTurn(t *testing.T) {
	th := newThread(New(transport.Endpoint{}), "x")
	ch := th.subscribe(context.Background(), &sub{after: 1, r: newReader(Cursor{Turn: 2, Text: 2})}, nil)
	open := aria.TurnPart{Turn: aria.Turn{ID: 3, Inquiry: "q", Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Role: livedoc.RoleOutput, Markdown: "more"}}, Live: &aria.Live{From: 0, V: 0}}}
	var rt runtimeFeed
	th.readBack(rt.response("thinking", 3, "", 0), sealedTurn(2, "ab"), open)
	th.notify(rpc.MethodFormDelta, rt.delta("idle", 3, "", 0))
	if got := summary(drain(t, ch)); got != `+"\n\nmore" end:ab

more` {
		t.Fatalf("events: %s", got)
	}
}
func TestReadingBackManyTurnsKeepsTheOneAListenerResumes(t *testing.T) {
	for range 50 {
		th := newThread(New(transport.Endpoint{}), "x")
		ch := th.subscribe(context.Background(), &sub{after: 1, r: newReader(Cursor{Turn: 2, Text: 1})}, nil)
		var parts []aria.TurnPart
		for id := uint64(2); id <= 9; id++ {
			parts = append(parts, sealedTurn(id, "ab"))
		}
		var rt runtimeFeed
		th.readBack(rt.response("idle", 9, "", 0), parts...)
		if got := summary(drain(t, ch)); got != `+"b" end:ab` {
			t.Fatalf("events: %s", got)
		}
	}
}

func liveProse(turn uint64, v int, md string) aria.TurnPart {
	return aria.TurnPart{Turn: aria.Turn{ID: turn, Live: &aria.Live{From: 0, V: v, Nodes: []aria.NodeDelta{{ID: 0, Set: map[string]any{"type": "prose", "role": "output", "markdown": md}}}}}}
}

func (th *thread) feed(parts ...aria.TurnPart) {
	th.mu.Lock()
	defer th.mu.Unlock()
	for i := range parts {
		th.process(note{part: &parts[i]})
	}
}

func (th *thread) end(turn uint64, reason string, idle bool) {
	th.mu.Lock()
	defer th.mu.Unlock()
	f := th.fold(turn)
	f.finish(reason)
	f.idle = idle
	th.deliver(f)
}

func TestTheEndTextIsWhatTheDeltasSpelledAcrossAnEmptyTurn(t *testing.T) {
	th := newThread(New(transport.Endpoint{}), "x")
	ch := th.subscribe(context.Background(), &sub{after: 1}, nil)
	th.feed(liveProse(2, 0, "A"))
	th.end(2, "", false)
	th.feed(aria.TurnPart{Turn: aria.Turn{ID: 3, Inquiry: "q"}})
	th.end(3, "", false)
	th.feed(liveProse(4, 0, "C"))
	th.end(4, "", true)
	events := drain(t, ch)
	last := events[len(events)-1]
	if text(events) != "A\n\nC" || last.Text != text(events) {
		t.Fatalf("events: %s", summary(events))
	}
}

func TestTheEndTextKeepsWhatWasShownWhenTheDaemonLostIt(t *testing.T) {
	th := newThread(New(transport.Endpoint{}), "x")
	ch := th.subscribe(context.Background(), &sub{after: 1}, nil)
	th.feed(liveProse(2, 0, "w0 w1 w2"))
	th.mu.Lock()
	th.folds = map[uint64]*turnFold{}
	th.mu.Unlock()
	running := sealedTurn(3, "ok")
	running.Sealed, running.Live = false, &aria.Live{From: 0, V: 0}
	var rt runtimeFeed
	th.readBack(rt.response("thinking", 3, "", 0), sealedTurn(2, "w0"), running)
	th.notify(rpc.MethodFormDelta, rt.delta("idle", 3, "", 0))
	events := drain(t, ch)
	last := events[len(events)-1]
	if text(events) != "w0 w1 w2\n\nok" || last.Text != text(events) {
		t.Fatalf("events: %s", summary(events))
	}
}

type runtimeFeed struct {
	snap form.Snapshot
	ver  uint64
}

func runtimeKeys(state string, turn uint64, reason string, inflight int) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	put := func(k string, v any) {
		b, _ := json.Marshal(v)
		m[k] = b
	}
	put("turn.state", state)
	put("turn.id", turn)
	put("inflight", inflight)
	if reason != "" {
		put("turn.reason", reason)
	}
	return m
}

func (r *runtimeFeed) response(state string, turn uint64, reason string, inflight int) *rpc.FormResponse {
	r.ver++
	r.snap = form.FromMap(runtimeKeys(state, turn, reason, inflight))
	return &rpc.FormResponse{Snapshot: r.snap, Version: r.ver, Intrinsic: "runtime", Incarnation: 1}
}

func (r *runtimeFeed) delta(state string, turn uint64, reason string, inflight int) json.RawMessage {
	next := form.FromMap(runtimeKeys(state, turn, reason, inflight))
	r.ver++
	d := rpc.FormDelta{Schema: rpc.FormDeltaSchema, Intrinsic: "runtime", Incarnation: 1, Version: r.ver, Patch: next.Diff(r.snap)}
	r.snap = next
	b, _ := json.Marshal(d)
	return b
}

func (th *thread) readBack(rt *rpc.FormResponse, parts ...aria.TurnPart) {
	th.mu.Lock()
	defer th.mu.Unlock()
	th.adopt(rt, parts)
}

func frameJSON(parts ...aria.TurnPart) json.RawMessage {
	b, _ := json.Marshal(aria.Page{Parts: parts})
	return b
}

func TestATurnThatFailsBeforeItOpensFailsItsPrompt(t *testing.T) {
	th := newThread(New(transport.Endpoint{}), "x")
	var rt runtimeFeed
	th.readBack(rt.response("idle", 1, "", 0))
	ch := th.subscribe(context.Background(), &sub{after: 1}, nil)
	th.notify(rpc.MethodFormDelta, rt.delta("accepted", 1, "", 1))
	th.notify(rpc.MethodTurnDone, json.RawMessage(`{"reason":"error: append message: disk full","idle":true}`))
	th.notify(rpc.MethodFormDelta, rt.delta("idle", 1, "error: append message: disk full", 0))
	if got := summary(drain(t, ch)); got != "error:append message: disk full" {
		t.Fatalf("events: %s", got)
	}
}

func TestATurnEndIsMatchedToItsTurnAcrossAResync(t *testing.T) {
	th := newThread(New(transport.Endpoint{}), "x")
	var rt runtimeFeed
	th.readBack(rt.response("thinking", 2, "", 0))
	ch := th.subscribe(context.Background(), &sub{after: 1}, nil)
	th.notify(rpc.MethodAriaFrame, frameJSON(liveProse(2, 0, "A")))
	stale := rt.delta("idle", 2, "", 1)
	snap := rt.response("thinking", 3, "", 0)
	running := liveProse(3, 0, "B")
	running.Nodes = []livedoc.Node{{Type: livedoc.NodeProse, Role: livedoc.RoleOutput, Markdown: "B"}}
	running.Live = &aria.Live{From: 0, V: 0}
	th.readBack(snap, sealedTurn(2, "A"), running)
	th.notify(rpc.MethodTurnDone, json.RawMessage(`{"reason":"end_turn","idle":false}`))
	th.notify(rpc.MethodFormDelta, stale)
	th.notify(rpc.MethodAriaFrame, frameJSON(aria.TurnPart{Turn: aria.Turn{ID: 3, Live: &aria.Live{From: 0, V: 1, Nodes: []aria.NodeDelta{{ID: 0, Patch: map[string]livedoc.Delta{"markdown": {At: 1, Ins: " more"}}}}}}}))
	th.notify(rpc.MethodTurnDone, json.RawMessage(`{"reason":"end_turn","idle":true}`))
	th.notify(rpc.MethodFormDelta, rt.delta("idle", 3, "", 0))
	events := drain(t, ch)
	if got := summary(events); got != `+"A" +"\n\nB" +" more" end:A

B more` {
		t.Fatalf("events: %s", got)
	}
}

func TestAFailedTurnReadBackFailsWithItsReason(t *testing.T) {
	th := newThread(New(transport.Endpoint{}), "x")
	var rt runtimeFeed
	ch := th.subscribe(context.Background(), &sub{after: 1, r: newReader(Cursor{Turn: 2})}, nil)
	th.readBack(rt.response("idle", 2, "error: provider overloaded", 0), sealedTurn(2, "par"))
	if got := summary(drain(t, ch)); got != `+"par" error:provider overloaded` {
		t.Fatalf("events: %s", got)
	}
}
