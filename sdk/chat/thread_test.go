package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk"
	"github.com/jack-work/jkrpc"
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
	ch := th.subscribe(context.Background(), &sub{after: 1, r: newReader(Cursor{Turn: 2, Text: 1}), resumed: true}, nil)
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
		ch := th.subscribe(context.Background(), &sub{after: 1, r: newReader(Cursor{Turn: 2, Text: 1}), resumed: true}, nil)
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
	if last := events[len(events)-1]; text(events) != "A\n\nB more" || last.Kind != End || last.Text != "A\n\nB more" {
		t.Fatalf("events: %s", summary(events))
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
	pageSize  int
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
	f.mu.Lock()
	gate := f.readGate
	if gate != nil && f.reading != nil {
		close(f.reading)
		f.reading = nil
	}
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-f.done:
			return aria.Page{}, errors.New("fake: closed")
		case <-ctx.Done():
			return aria.Page{}, ctx.Err()
		}
	}
	var page aria.Page
	f.mu.Lock()
	turns, size := f.turns, f.pageSize
	f.mu.Unlock()
	for _, p := range turns {
		if p.ID < at.Turn {
			continue
		}
		if size > 0 && len(page.Parts) == size {
			page.More.After = true
			page.Next = &aria.Anchor{Turn: p.ID}
			break
		}
		page.Parts = append(page.Parts, p)
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
	th, _, release, err := c.thread(context.Background(), "x", true)
	defer release()
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

func TestAWaitForAQueuedTurnEndsWhenTheQueueEmptiesWithoutOne(t *testing.T) {
	var rt runtimeFeed
	conn := newFakeConn(rt.response("idle", 2, "", 1), sealedTurn(2, "ab"))
	conn.queue = queueWith()
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
	th, _, release, err := c.thread(context.Background(), "x", true)
	defer release()
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

func TestASteerTheRunningTurnAbsorbedClosesItsChannelEmpty(t *testing.T) {
	th := newThread(New(transport.Endpoint{}), "x")
	var rt runtimeFeed
	th.readBack(rt.response("thinking", 2, "", 0))
	first := th.subscribe(context.Background(), &sub{after: 1}, nil)
	th.notify(rpc.MethodAriaFrame, frameJSON(liveProse(2, 0, "A")))
	steer := th.subscribe(context.Background(), &sub{after: 2, steer: true}, nil)
	th.notify(rpc.MethodFormDelta, rt.delta("idle", 2, "", 0))
	if got := summary(drain(t, first)); got != `+"A" end:A` {
		t.Fatalf("first: %s", got)
	}
	if got := summary(drain(t, steer)); got != "" {
		t.Fatalf("steer: %s", got)
	}
}

func TestASteerThatOpensItsOwnTurnCarriesThatReply(t *testing.T) {
	th := newThread(New(transport.Endpoint{}), "x")
	var rt runtimeFeed
	th.readBack(rt.response("thinking", 2, "", 0))
	first := th.subscribe(context.Background(), &sub{after: 1}, nil)
	th.notify(rpc.MethodAriaFrame, frameJSON(liveProse(2, 0, "A")))
	steer := th.subscribe(context.Background(), &sub{after: 2, steer: true}, nil)
	th.notify(rpc.MethodFormDelta, rt.delta("idle", 2, "", 1))
	th.notify(rpc.MethodFormDelta, rt.delta("thinking", 3, "", 0))
	th.notify(rpc.MethodAriaFrame, frameJSON(liveProse(3, 0, "B")))
	th.notify(rpc.MethodFormDelta, rt.delta("idle", 3, "", 0))
	if got := summary(drain(t, first)); got != `+"A" end:A` {
		t.Fatalf("first: %s", got)
	}
	if got := summary(drain(t, steer)); got != `+"B" end:B` {
		t.Fatalf("steer: %s", got)
	}
}

func TestDeltasPileUpAsOneWhileTheConsumerLags(t *testing.T) {
	th := newThread(New(transport.Endpoint{}), "x")
	s := &sub{after: 1}
	ch := th.subscribe(context.Background(), s, nil)
	for i := range 5000 {
		s.push(Event{Kind: Delta, Text: "x", Cursor: Cursor{Turn: 2, Text: i + 1}}, false)
	}
	s.mu.Lock()
	queued := len(s.q)
	s.mu.Unlock()
	if queued > 1 {
		t.Fatalf("%d events queued for a consumer that is not reading", queued)
	}
	s.push(Event{Kind: End, Text: "done", Cursor: Cursor{Turn: 2, Text: 5000}}, true)
	events := drain(t, ch)
	if text(events) != strings.Repeat("x", 5000) || events[len(events)-1].Cursor.Text != 5000 {
		t.Fatalf("%d events, %d bytes", len(events), len(text(events)))
	}
}

func TestClosingTheClientReleasesAListenerThatStoppedReading(t *testing.T) {
	c := New(transport.Endpoint{})
	th := newThread(c, "x")
	s := &sub{after: 1}
	th.subscribe(context.Background(), s, nil)
	for i := range 200 {
		s.push(Event{Kind: ToolStart, Tool: &Tool{ID: fmt.Sprint(i)}}, false)
	}
	time.Sleep(10 * time.Millisecond)
	c.Close()
	select {
	case <-s.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the delivery goroutine outlived the client")
	}
}

type fakeAngelus struct {
	mu      sync.Mutex
	forks   int
	closes  int
	forkErr error
}

func (a *fakeAngelus) Fork(context.Context, string, uint64, uint64, []string, *rpc.FormPatch) (*rpc.ForkResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.forks++
	if a.forkErr != nil {
		return nil, a.forkErr
	}
	return &rpc.ForkResponse{Alternative: "child"}, nil
}

func (a *fakeAngelus) Attach(_ context.Context, id string) (*rpc.AttachResponse, error) {
	return &rpc.AttachResponse{FigaroID: id}, nil
}

func (a *fakeAngelus) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closes++
	return nil
}

func withFakeAngelus(c *Client, a *fakeAngelus) *int {
	dials := new(int)
	c.dialAngelus = func() (angelusConn, error) {
		*dials++
		return a, nil
	}
	return dials
}

func TestAForkWhoseReplyIsLostIsNotSentAgain(t *testing.T) {
	c := New(transport.Endpoint{})
	a := &fakeAngelus{forkErr: io.ErrUnexpectedEOF}
	withFakeAngelus(c, a)
	if _, err := c.Open(context.Background(), "root"); err == nil {
		t.Fatal("the lost fork reported success")
	}
	if a.forks != 1 {
		t.Fatalf("the fork was sent %d times", a.forks)
	}
}

func TestARefusedForkKeepsTheSharedConnection(t *testing.T) {
	c := New(transport.Endpoint{})
	a := &fakeAngelus{forkErr: &jkrpc.Error{Code: -32000, Message: "fanout: no room"}}
	dials := withFakeAngelus(c, a)
	for range 2 {
		if _, err := c.Open(context.Background(), "root"); err == nil {
			t.Fatal("the refused fork reported success")
		}
	}
	if a.closes != 0 || *dials != 1 || a.forks != 2 {
		t.Fatalf("closes %d, dials %d, forks %d", a.closes, *dials, a.forks)
	}
}

func TestClosingTheClientWhileAThreadDialsLeavesNoConnection(t *testing.T) {
	var rt runtimeFeed
	conn := newFakeConn(rt.response("idle", 1, "", 0))
	conn.dialGate = make(chan struct{})
	c := fakeClient(conn)
	resumed := make(chan error, 1)
	go func() {
		_, err := c.Resume(context.Background(), "x", Cursor{Turn: 1})
		resumed <- err
	}()
	time.Sleep(20 * time.Millisecond)
	c.Close()
	close(conn.dialGate)
	select {
	case <-conn.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a connection dialed across Close was kept")
	}
	if err := <-resumed; err == nil {
		t.Fatal("a resume across Close reported success")
	}
}

func TestForgettingAThreadReleasesItsConnectionAndListeners(t *testing.T) {
	var rt runtimeFeed
	conn := newFakeConn(rt.response("thinking", 2, "", 0))
	c := fakeClient(conn)
	ch, err := c.Resume(context.Background(), "x", Cursor{Turn: 2})
	if err != nil {
		t.Fatal(err)
	}
	c.Forget("x")
	if got := summary(drain(t, ch)); got != "error:the thread was forgotten" {
		t.Fatalf("events: %s", got)
	}
	select {
	case <-conn.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the connection outlived Forget")
	}
	c.mu.Lock()
	n := len(c.threads)
	c.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d threads held after Forget", n)
	}
}

func TestAThreadLeftIdleLetsItsConnectionGo(t *testing.T) {
	var rt runtimeFeed
	conn := newFakeConn(rt.response("idle", 2, "", 0), sealedTurn(2, "ab"))
	c := fakeClient(conn)
	c.IdleClose = 30 * time.Millisecond
	ch, err := c.Resume(context.Background(), "x", Cursor{Turn: 2, Text: 1})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	select {
	case <-conn.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("an idle thread kept its connection")
	}
	c.mu.Lock()
	n := len(c.threads)
	c.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d threads held after idling", n)
	}
}

func TestAPromptAcceptedDuringATurnDoesNotEndIt(t *testing.T) {
	th := newThread(New(transport.Endpoint{}), "x")
	var rt runtimeFeed
	th.readBack(rt.response("idle", 2, "", 0))
	ch := th.subscribe(context.Background(), &sub{after: 2}, nil)
	th.notify(rpc.MethodFormDelta, rt.delta("thinking", 3, "", 0))
	th.notify(rpc.MethodAriaFrame, frameJSON(liveProse(3, 0, "A")))
	th.notify(rpc.MethodFormDelta, rt.delta("accepted", 3, "", 1))
	th.notify(rpc.MethodAriaFrame, frameJSON(aria.TurnPart{Turn: aria.Turn{ID: 3, Live: &aria.Live{From: 0, V: 1, Nodes: []aria.NodeDelta{{ID: 0, Patch: map[string]livedoc.Delta{"markdown": {At: 1, Ins: " B"}}}}}}}))
	th.notify(rpc.MethodFormDelta, rt.delta("idle", 3, "", 0))
	events := drain(t, ch)
	if last := events[len(events)-1]; text(events) != "A B" || last.Kind != End || last.Text != "A B" {
		t.Fatalf("events: %s", summary(events))
	}
}

func TestAFinishedTurnEndsOnAnAriaWokenWithoutATurn(t *testing.T) {
	th := newThread(New(transport.Endpoint{}), "x")
	ch := th.subscribe(context.Background(), &sub{after: 1, r: newReader(Cursor{Turn: 2, Text: 1}), resumed: true}, nil)
	var rt runtimeFeed
	th.readBack(rt.response("idle", 0, "", 0), sealedTurn(2, "ab"))
	if got := summary(drain(t, ch)); got != `+"b" end:ab` {
		t.Fatalf("events: %s", got)
	}
}

func queueWith(states ...string) *rpc.FormResponse {
	m := map[string]json.RawMessage{}
	n, _ := json.Marshal(len(states))
	m["len"] = n
	for i, st := range states {
		b, _ := json.Marshal(st)
		m[fmt.Sprintf("items.%d.state", i+1)] = b
	}
	return &rpc.FormResponse{Snapshot: form.FromMap(m), Version: 1, Intrinsic: "queue", Incarnation: 1}
}

func TestAWaitEndsWhenOnlyAnsweredPromptsRemainInTheQueueForm(t *testing.T) {
	var rt runtimeFeed
	conn := newFakeConn(rt.response("idle", 2, "", 1), sealedTurn(2, "ab"))
	conn.queue = queueWith("committed", "committed", "dropped")
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

func TestAWaitHoldsWhileAPromptIsStillQueued(t *testing.T) {
	var rt runtimeFeed
	conn := newFakeConn(rt.response("idle", 2, "", 1), sealedTurn(2, "ab"))
	conn.queue = queueWith("committed", "queued")
	c := fakeClient(conn)
	c.CheckEvery = 10 * time.Millisecond
	ch, err := c.Resume(context.Background(), "x", Cursor{Turn: 2, Text: 1})
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	timeout := time.After(300 * time.Millisecond)
	for done := false; !done; {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatalf("ended while a prompt was queued: %s", summary(events))
			}
			events = append(events, e)
		case <-timeout:
			done = true
		}
	}
	if got := summary(events); got != `+"b"` {
		t.Fatalf("events: %s", got)
	}
}

func TestALiveChannelCarriesOnIntoATurnThatFinishedDuringAnOutage(t *testing.T) {
	th := newThread(New(transport.Endpoint{}), "x")
	var rt runtimeFeed
	th.readBack(rt.response("thinking", 2, "", 0))
	ch := th.subscribe(context.Background(), &sub{after: 1}, nil)
	th.feed(liveProse(2, 0, "w0 w1"))
	th.mu.Lock()
	th.folds = map[uint64]*turnFold{}
	th.mu.Unlock()
	th.readBack(rt.response("idle", 3, "", 0), sealedTurn(2, "w0"), sealedTurn(3, "ok"))
	events := drain(t, ch)
	if last := events[len(events)-1]; text(events) != "w0 w1\n\nok" || last.Kind != End || last.Text != text(events) {
		t.Fatalf("events: %s", summary(events))
	}
}

func TestTheTimedCheckLeavesATurnAloneWhileItIsBeingReadBack(t *testing.T) {
	var rt runtimeFeed
	running := liveProse(2, 0, "a")
	running.Nodes = []livedoc.Node{{Type: livedoc.NodeProse, Role: livedoc.RoleOutput, Markdown: "a"}}
	conn := newFakeConn(rt.response("thinking", 2, "", 0), running)
	c := fakeClient(conn)
	c.CheckEvery = 20 * time.Millisecond
	ch, err := c.Resume(context.Background(), "x", Cursor{Turn: 2})
	if err != nil {
		t.Fatal(err)
	}
	if e := <-ch; e.Text != "a" {
		t.Fatalf("first event %+v", e)
	}
	reading := make(chan struct{})
	conn.mu.Lock()
	conn.readGate, conn.reading = make(chan struct{}), reading
	conn.mu.Unlock()
	conn.notify(rpc.MethodAriaFrame, frameJSON(aria.TurnPart{Turn: aria.Turn{ID: 2, Live: &aria.Live{From: 0, V: 5, Nodes: []aria.NodeDelta{{ID: 0, Patch: map[string]livedoc.Delta{"markdown": {At: 1, Ins: "b"}}}}}}}))
	<-reading
	conn.mu.Lock()
	conn.runtime, conn.turns = rt.response("idle", 2, "", 0), []aria.TurnPart{sealedTurn(2, "ab")}
	conn.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	close(conn.readGate)
	if got := summary(drain(t, ch)); got != `+"b" end:ab` {
		t.Fatalf("events: %s", got)
	}
}

func TestAnUnreadableRuntimeFormFailsStreamsWithItsCauseAndSparesTheRest(t *testing.T) {
	var rt runtimeFeed
	conn := newFakeConn(rt.response("idle", 2, "", 0), sealedTurn(2, "ab"))
	conn.formFails = 1 << 30
	c := fakeClient(conn)
	c.ReconnectFor = 50 * time.Millisecond
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := c.Cancel(context.Background(), "x"); err != nil {
			t.Errorf("cancel: %v", err)
		}
		if _, err := c.History(context.Background(), "x", 0, 5); err != nil {
			t.Errorf("history: %v", err)
		}
		_, err := c.Resume(context.Background(), "x", Cursor{Turn: 2})
		if err == nil || !strings.Contains(err.Error(), "fake: busy") {
			t.Errorf("resume: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("calls hung on a runtime form that cannot be read")
	}
}

func TestTheTimedCheckRedialsAThreadLeftWithoutAConnection(t *testing.T) {
	var rt runtimeFeed
	running := liveProse(2, 0, "a")
	running.Nodes = []livedoc.Node{{Type: livedoc.NodeProse, Role: livedoc.RoleOutput, Markdown: "a"}}
	first := newFakeConn(rt.response("thinking", 2, "", 0), running)
	second := newFakeConn(rt.response("idle", 2, "", 0), sealedTurn(2, "ab"))
	c := fakeClient(first, second)
	c.CheckEvery = 20 * time.Millisecond
	th, _, release, err := c.thread(context.Background(), "x", true)
	defer release()
	if err != nil {
		t.Fatal(err)
	}
	ch := th.subscribe(context.Background(), &sub{after: 1, r: newReader(Cursor{Turn: 2}), resumed: true}, []uint64{2})
	if e := <-ch; e.Text != "a" {
		t.Fatalf("first event %+v", e)
	}
	th.mu.Lock()
	th.conn = nil
	th.mu.Unlock()
	if got := summary(drain(t, ch)); got != `+"b" end:ab` {
		t.Fatalf("events: %s", got)
	}
}

func TestAListenerReadingWhenTheClientClosesIsToldWhy(t *testing.T) {
	for range 50 {
		var rt runtimeFeed
		c := fakeClient(newFakeConn(rt.response("thinking", 2, "", 0)))
		ch, err := c.Resume(context.Background(), "x", Cursor{Turn: 2})
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		if got := summary(drain(t, ch)); got != "error:chat: client closed" {
			t.Fatalf("events: %q", got)
		}
	}
}

func TestMergingABacklogOfDeltasDoesNotCopyItOverAndOver(t *testing.T) {
	s := &sub{wake: make(chan struct{}, 1)}
	allocs := testing.AllocsPerRun(5, func() {
		s.q, s.more = nil, nil
		for i := range 2000 {
			s.push(Event{Kind: Delta, Text: "xxxxxxxx", Cursor: Cursor{Text: i}}, false)
		}
	})
	if allocs > 200 {
		t.Fatalf("merging 2000 deltas allocated %.0f times", allocs)
	}
}

func TestResumingFarBackReadsEveryPageToTheTail(t *testing.T) {
	var rt runtimeFeed
	var turns []aria.TurnPart
	for id := uint64(2); id <= 101; id++ {
		turns = append(turns, sealedTurn(id, "ab"))
	}
	conn := newFakeConn(rt.response("idle", 101, "", 0), turns...)
	conn.pageSize = 1
	c := fakeClient(conn)
	ch, err := c.Resume(context.Background(), "x", Cursor{Turn: 2, Text: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(drain(t, ch)); got != `+"b" end:ab` {
		t.Fatalf("events: %s", got)
	}
}

func TestACallInProgressHoldsItsThreadAgainstIdleClose(t *testing.T) {
	var rt runtimeFeed
	c := New(transport.Endpoint{})
	c.IdleClose = time.Millisecond
	c.dial = func(context.Context, string, sdk.NotifyHandler) (ariaConn, error) {
		time.Sleep(2 * time.Millisecond)
		return newFakeConn(rt.response("idle", 1, "", 0)), nil
	}
	for i := range 100 {
		if err := c.Cancel(context.Background(), "x"); err != nil {
			t.Fatalf("call %d on an open client: %v", i, err)
		}
		time.Sleep(time.Duration(i%3) * time.Millisecond)
	}
}
