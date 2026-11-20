package chat

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk"
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

type fakeConn struct {
	mu        sync.Mutex
	notify    sdk.NotifyHandler
	turns     []aria.TurnPart
	runtime   *rpc.FormResponse
	formFails int
	queue     *rpc.FormResponse
	hangAfter int
	readGate  chan struct{}
	reading   chan struct{}
	dialGate  chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func newFakeConn(rt *rpc.FormResponse, turns ...aria.TurnPart) *fakeConn {
	return &fakeConn{turns: turns, runtime: rt, done: make(chan struct{})}
}

func (f *fakeConn) Qua(context.Context, string, *rpc.FormInput) (int, bool, error) {
	return 0, false, errors.New("fake: no prompts")
}

func (f *fakeConn) Read(ctx context.Context, at aria.Anchor, _ int) (aria.Page, error) {
	if f.readGate != nil {
		if f.reading != nil {
			close(f.reading)
			f.reading = nil
		}
		select {
		case <-f.readGate:
		case <-f.done:
			return aria.Page{}, errors.New("fake: closed")
		case <-ctx.Done():
			return aria.Page{}, ctx.Err()
		}
	}
	var page aria.Page
	for _, p := range f.turns {
		if p.ID >= at.Turn {
			page.Parts = append(page.Parts, p)
		}
	}
	return page, nil
}

func (f *fakeConn) ReadBefore(context.Context, aria.Anchor, int) (aria.Page, error) {
	return aria.Page{}, nil
}

func (f *fakeConn) FormOf(ctx context.Context, name string) (*rpc.FormResponse, error) {
	f.mu.Lock()
	if f.hangAfter > 0 {
		f.hangAfter--
		if f.hangAfter == 0 {
			f.mu.Unlock()
			<-ctx.Done()
			return nil, ctx.Err()
		}
	}
	defer f.mu.Unlock()
	if f.formFails > 0 {
		f.formFails--
		return nil, errors.New("fake: busy")
	}
	if name == "queue" {
		return f.queue, nil
	}
	return f.runtime, nil
}

func (f *fakeConn) Interrupt(context.Context) error { return nil }

func (f *fakeConn) Close() error {
	f.closeOnce.Do(func() { close(f.done) })
	return nil
}

func (f *fakeConn) Done() <-chan struct{} { return f.done }

func fakeClient(conns ...*fakeConn) *Client {
	c := New(transport.Endpoint{})
	var mu sync.Mutex
	c.dial = func(_ context.Context, _ string, notify sdk.NotifyHandler) (ariaConn, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(conns) == 0 {
			return nil, errors.New("fake: no more connections")
		}
		f := conns[0]
		conns = conns[1:]
		mu.Unlock()
		if f.dialGate != nil {
			<-f.dialGate
		}
		mu.Lock()
		f.notify = notify
		return f, nil
	}
	return c
}

func TestAFailedCatchUpReadIsRetriedWhileTheConnectionLives(t *testing.T) {
	var rt runtimeFeed
	conn := newFakeConn(rt.response("idle", 2, "", 0), sealedTurn(2, "ab"))
	c := fakeClient(conn)
	ch, err := c.Resume(context.Background(), "x", Cursor{Turn: 2, Text: 1})
	if err != nil {
		t.Fatal(err)
	}
	conn.mu.Lock()
	conn.formFails = 2
	conn.mu.Unlock()
	conn.notify(rpc.MethodAriaFrame, frameJSON(aria.TurnPart{Turn: aria.Turn{ID: 2, Live: &aria.Live{From: 0, V: 7, Nodes: []aria.NodeDelta{{ID: 0, Set: map[string]any{"markdown": "abc"}}}}}}))
	if got := summary(drain(t, ch)); got != `+"b" end:ab` {
		t.Fatalf("events: %s", got)
	}
}

func TestACatchUpCutByAConnectionDropWaitsForTheNextConnection(t *testing.T) {
	var rt runtimeFeed
	first := newFakeConn(rt.response("thinking", 2, "", 0))
	first.readGate = make(chan struct{})
	reading := make(chan struct{})
	first.reading = reading
	second := newFakeConn(rt.response("idle", 2, "", 0), sealedTurn(2, "ab"))
	second.dialGate = make(chan struct{})
	c := fakeClient(first, second)
	ch, err := c.Resume(context.Background(), "x", Cursor{Turn: 2, Text: 1})
	if err != nil {
		t.Fatal(err)
	}
	<-reading
	first.Close()
	time.Sleep(100 * time.Millisecond)
	close(second.dialGate)
	if got := summary(drain(t, ch)); got != `+"b" end:ab` {
		t.Fatalf("events: %s", got)
	}
}

func TestAListenerAddedAfterADropBringsTheConnectionBack(t *testing.T) {
	var rt runtimeFeed
	first := newFakeConn(rt.response("idle", 1, "", 0))
	second := newFakeConn(rt.response("idle", 2, "", 0), sealedTurn(2, "ab"))
	c := fakeClient(first, second)
	th, _, err := c.thread(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		th.mu.Lock()
		gone := th.conn == nil
		th.mu.Unlock()
		if gone || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	ch := th.subscribe(context.Background(), &sub{after: 1, r: newReader(Cursor{Turn: 2, Text: 1})}, []uint64{2})
	if got := summary(drain(t, ch)); got != `+"b" end:ab` {
		t.Fatalf("events: %s", got)
	}
}

func TestResumingATurnPastTheTailFailsAtOnce(t *testing.T) {
	var rt runtimeFeed
	c := fakeClient(newFakeConn(rt.response("idle", 2, "", 0), sealedTurn(2, "ab")))
	ch, err := c.Resume(context.Background(), "x", Cursor{Turn: 5})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(drain(t, ch)); got != "error:turn 5 does not exist; the newest is 2" {
		t.Fatalf("events: %s", got)
	}
}

func queueOf(n int) *rpc.FormResponse {
	b, _ := json.Marshal(n)
	return &rpc.FormResponse{Snapshot: form.FromMap(map[string]json.RawMessage{"len": b}), Version: 1, Intrinsic: "queue", Incarnation: 1}
}

func TestAWaitForAQueuedTurnEndsWhenTheQueueEmptiesWithoutOne(t *testing.T) {
	var rt runtimeFeed
	conn := newFakeConn(rt.response("idle", 2, "", 1), sealedTurn(2, "ab"))
	conn.queue = queueOf(0)
	c := fakeClient(conn)
	c.CheckEvery = 20 * time.Millisecond
	ch, err := c.Resume(context.Background(), "x", Cursor{Turn: 2, Text: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(drain(t, ch)); got != `+"b" end:ab` {
		t.Fatalf("events: %s", got)
	}
}

func TestAConnectionThatStopsAnsweringIsReplaced(t *testing.T) {
	var rt runtimeFeed
	running := liveProse(2, 0, "a")
	running.Nodes = []livedoc.Node{{Type: livedoc.NodeProse, Role: livedoc.RoleOutput, Markdown: "a"}}
	first := newFakeConn(rt.response("thinking", 2, "", 0), running)
	first.hangAfter = 3
	second := newFakeConn(rt.response("idle", 2, "", 0), sealedTurn(2, "ab"))
	c := fakeClient(first, second)
	c.CheckEvery = 20 * time.Millisecond
	ch, err := c.Resume(context.Background(), "x", Cursor{Turn: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(drain(t, ch)); got != `+"a" +"b" end:ab` {
		t.Fatalf("events: %s", got)
	}
}

func TestANewTurnAnnouncedBeforeItsFramesStreamsWithoutARead(t *testing.T) {
	var rt runtimeFeed
	conn := newFakeConn(rt.response("idle", 2, "", 0))
	conn.readGate = make(chan struct{})
	c := fakeClient(conn)
	th, _, err := c.thread(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	ch := th.subscribe(context.Background(), &sub{after: 2}, nil)
	conn.notify(rpc.MethodFormDelta, rt.delta("thinking", 3, "", 0))
	conn.notify(rpc.MethodAriaFrame, frameJSON(liveProse(3, 0, "B")))
	conn.notify(rpc.MethodFormDelta, rt.delta("idle", 3, "", 0))
	if got := summary(drain(t, ch)); got != `+"B" end:B` {
		t.Fatalf("events: %s", got)
	}
}
