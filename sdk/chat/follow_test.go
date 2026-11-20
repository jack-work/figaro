package chat

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
)

func take(t *testing.T, ch <-chan Event, n int) []Event {
	t.Helper()
	var out []Event
	timeout := time.After(5 * time.Second)
	for len(out) < n {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed after %s", summary(out))
			}
			out = append(out, e)
		case <-timeout:
			t.Fatalf("wanted %d events, got %s", n, summary(out))
		}
	}
	return out
}

func quiet(t *testing.T, ch <-chan Event) {
	t.Helper()
	select {
	case e, ok := <-ch:
		if ok {
			t.Fatalf("unexpected %s %q", e.Kind, e.Text)
		}
		t.Fatal("channel closed")
	case <-time.After(150 * time.Millisecond):
	}
}

func TestFollowAnnouncesEveryTurnWithItsPromptAndSender(t *testing.T) {
	th := newThread(New(transport.Endpoint{}), "x")
	ch := th.subscribe(context.Background(), &sub{follow: true, after: 1}, nil)
	th.feed(opening(2, "from the king", "k1", "king"))
	th.feed(liveProse(2, 0, "A"))
	th.end(2, "", true)
	events := take(t, ch, 3)
	quiet(t, ch)
	th.feed(opening(3, "from a terminal", "", ""))
	th.feed(liveProse(3, 0, "B"))
	th.end(3, "", true)
	events = append(events, take(t, ch, 3)...)
	if got := summary(events); got != `turn.start:from the king +"A" end:A turn.start:from a terminal +"B" end:B` {
		t.Fatalf("events: %s", got)
	}
	if s := events[0].Sender; s == nil || s.ID != "k1" || s.Name != "king" || events[0].Cursor.Turn != 2 {
		t.Fatalf("first start: %+v", events[0])
	}
	if events[3].Sender != nil || events[3].Cursor.Turn != 3 {
		t.Fatalf("second start: %+v", events[3])
	}
}

func TestFollowEndCursorPointsAtTheNextTurn(t *testing.T) {
	th := newThread(New(transport.Endpoint{}), "x")
	ch := th.subscribe(context.Background(), &sub{follow: true, after: 1}, nil)
	th.feed(opening(2, "q", "", ""), liveProse(2, 0, "A"))
	th.end(2, "", true)
	events := take(t, ch, 3)
	if got := events[0].Cursor; got != (Cursor{Turn: 2, Begun: true}) {
		t.Fatalf("start cursor %+v", got)
	}
	if got := events[2].Cursor; got != (Cursor{Turn: 3}) {
		t.Fatalf("end cursor %+v", got)
	}
}

func TestFollowStaysOpenAcrossAFailedTurn(t *testing.T) {
	th := newThread(New(transport.Endpoint{}), "x")
	ch := th.subscribe(context.Background(), &sub{follow: true, after: 1}, nil)
	th.feed(opening(2, "q", "", ""), liveProse(2, 0, "par"))
	th.end(2, "error: provider overloaded", true)
	th.feed(opening(3, "again", "", ""), liveProse(3, 0, "ok"))
	th.end(3, "", true)
	if got := summary(take(t, ch, 6)); got != `turn.start:q +"par" error:provider overloaded turn.start:again +"ok" end:ok` {
		t.Fatalf("events: %s", got)
	}
}

func TestFollowWaitsForAPromptToAnIdleAria(t *testing.T) {
	var rt runtimeFeed
	conn := newFakeConn(rt.response("idle", 2, "", 0), sealedTurn(1, "old"), sealedTurn(2, "older"))
	c := fakeClient(conn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := c.Follow(ctx, "x", Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	quiet(t, ch)
	conn.notify(rpc.MethodFormDelta, rt.delta("accepted", 3, "", 1))
	conn.notify(rpc.MethodAriaFrame, frameJSON(opening(3, "wake up", "k1", "king")))
	conn.notify(rpc.MethodAriaFrame, frameJSON(liveProse(3, 0, "up")))
	conn.notify(rpc.MethodFormDelta, rt.delta("idle", 3, "", 0))
	if got := summary(take(t, ch, 3)); got != `turn.start:wake up +"up" end:up` {
		t.Fatalf("events: %s", got)
	}
	quiet(t, ch)
}

func TestFollowFromNowSkipsTheTurnRunningAtTheCall(t *testing.T) {
	var rt runtimeFeed
	running := sealedTurn(2, "so far")
	running.Sealed, running.Live = false, &aria.Live{From: 0, V: 0}
	conn := newFakeConn(rt.response("thinking", 2, "", 0), sealedTurn(1, "old"), running)
	c := fakeClient(conn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := c.Follow(ctx, "x", Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	conn.notify(rpc.MethodFormDelta, rt.delta("idle", 2, "", 0))
	quiet(t, ch)
	conn.notify(rpc.MethodFormDelta, rt.delta("thinking", 3, "", 0))
	conn.notify(rpc.MethodAriaFrame, frameJSON(opening(3, "next", "", "")))
	conn.notify(rpc.MethodAriaFrame, frameJSON(liveProse(3, 0, "x")))
	conn.notify(rpc.MethodFormDelta, rt.delta("idle", 3, "", 0))
	if got := summary(take(t, ch, 3)); got != `turn.start:next +"x" end:x` {
		t.Fatalf("events: %s", got)
	}
}

func TestFollowFromNowOnADormantAriaSkipsItsOldTurns(t *testing.T) {
	conn := newFakeConn(nil)
	conn.dormant = true
	conn.turns = []aria.TurnPart{sealedTurn(1, "a"), sealedTurn(2, "b")}
	c := fakeClient(conn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := c.Follow(ctx, "x", Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	quiet(t, ch)
}

func TestFollowResumedAfterTurnStartDoesNotRepeatIt(t *testing.T) {
	var rt runtimeFeed
	conn := newFakeConn(rt.response("idle", 2, "", 0), sealedTurn(2, "abc"))
	c := fakeClient(conn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := c.Follow(ctx, "x", Cursor{Turn: 2, Begun: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(take(t, ch, 2)); got != `+"abc" end:abc` {
		t.Fatalf("events: %s", got)
	}
}

func TestFollowResumedAtTheStartOfATurnAnnouncesIt(t *testing.T) {
	var rt runtimeFeed
	conn := newFakeConn(rt.response("idle", 2, "", 0), sealedTurn(2, "abc"))
	c := fakeClient(conn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := c.Follow(ctx, "x", Cursor{Turn: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(take(t, ch, 3)); got != `turn.start:q +"abc" end:abc` {
		t.Fatalf("events: %s", got)
	}
}

func TestFollowResumedFromAnEndCursorWaitsForTheNextTurn(t *testing.T) {
	var rt runtimeFeed
	conn := newFakeConn(rt.response("idle", 2, "", 0), sealedTurn(2, "abc"))
	c := fakeClient(conn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := c.Follow(ctx, "x", Cursor{Turn: 3})
	if err != nil {
		t.Fatal(err)
	}
	quiet(t, ch)
	conn.notify(rpc.MethodFormDelta, rt.delta("thinking", 3, "", 0))
	conn.notify(rpc.MethodAriaFrame, frameJSON(opening(3, "go", "", ""), liveProse(3, 0, "y")))
	conn.notify(rpc.MethodFormDelta, rt.delta("idle", 3, "", 0))
	if got := summary(take(t, ch, 3)); got != `turn.start:go +"y" end:y` {
		t.Fatalf("events: %s", got)
	}
}

func TestFollowKeepsItsPlaceWhenTheConnectionDropsMidTurn(t *testing.T) {
	var rt runtimeFeed
	first := newFakeConn(rt.response("thinking", 2, "", 0))
	running := sealedTurn(2, "w0 w1 w2 w3")
	second := newFakeConn(rt.response("idle", 2, "", 0), running)
	c := fakeClient(first, second)
	c.ReconnectFor = 5 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := c.Follow(ctx, "x", Cursor{Turn: 2})
	if err != nil {
		t.Fatal(err)
	}
	first.notify(rpc.MethodAriaFrame, frameJSON(opening(2, "q", "", "")))
	first.notify(rpc.MethodAriaFrame, frameJSON(liveProse(2, 0, "w0 w1")))
	seen := take(t, ch, 2)
	first.Close()
	rest := take(t, ch, 2)
	events := append(seen, rest...)
	if text(events) != "w0 w1 w2 w3" || events[len(events)-1].Kind != End || events[len(events)-1].Text != text(events) {
		t.Fatalf("events: %s", summary(events))
	}
	if n := strings.Count(summary(events), "turn.start"); n != 1 {
		t.Fatalf("%d turn.start in %s", n, summary(events))
	}
}

func toolPart(turn uint64) aria.TurnPart {
	return aria.TurnPart{Turn: aria.Turn{ID: turn, Inquiry: "q", Sealed: true, Nodes: []livedoc.Node{{Type: livedoc.NodeTool, Name: "read", Status: livedoc.StatusOK, Output: "r"}}}}
}

func TestFollowCarriesToolEventsBetweenTheStartAndTheEnd(t *testing.T) {
	var rt runtimeFeed
	conn := newFakeConn(rt.response("idle", 2, "", 0), toolPart(2))
	c := fakeClient(conn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := c.Follow(ctx, "x", Cursor{Turn: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(take(t, ch, 4)); got != `turn.start:q tool.start:read: tool.end:r: end:` {
		t.Fatalf("events: %s", got)
	}
}

func TestForgettingAFollowedThreadClosesItsChannelWithAnError(t *testing.T) {
	var rt runtimeFeed
	conn := newFakeConn(rt.response("idle", 2, "", 0))
	c := fakeClient(conn)
	ch, err := c.Follow(context.Background(), "x", Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	c.Forget("x")
	events := drain(t, ch)
	if len(events) != 1 || events[0].Kind != Failed {
		t.Fatalf("events: %s", summary(events))
	}
}
