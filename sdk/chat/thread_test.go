package chat

import (
	"context"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
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
	th.mu.Lock()
	th.adopt([]aria.TurnPart{sealedTurn(2, "ab"), sealedTurn(3, "later")})
	th.mu.Unlock()
	if got := summary(drain(t, ch)); got != `+"b" end:ab` {
		t.Fatalf("events: %s", got)
	}
}

func TestAFinishedTurnReadBackRunsOnIntoAnOpenNextTurn(t *testing.T) {
	th := newThread(New(transport.Endpoint{}), "x")
	ch := th.subscribe(context.Background(), &sub{after: 1, r: newReader(Cursor{Turn: 2, Text: 2})}, nil)
	open := aria.TurnPart{Turn: aria.Turn{ID: 3, Inquiry: "q", Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Role: livedoc.RoleOutput, Markdown: "more"}}, Live: &aria.Live{From: 0, V: 0}}}
	th.mu.Lock()
	th.adopt([]aria.TurnPart{sealedTurn(2, "ab"), open})
	th.folds[3].finish("")
	th.folds[3].idle = true
	th.deliver(th.folds[3])
	th.mu.Unlock()
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
		th.mu.Lock()
		th.adopt(parts)
		th.mu.Unlock()
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
	running := sealedTurn(3, "ok")
	running.Sealed, running.Live = false, &aria.Live{From: 0, V: 0}
	th.adopt([]aria.TurnPart{sealedTurn(2, "w0"), running})
	th.mu.Unlock()
	th.end(3, "", true)
	events := drain(t, ch)
	last := events[len(events)-1]
	if text(events) != "w0 w1 w2\n\nok" || last.Text != text(events) {
		t.Fatalf("events: %s", summary(events))
	}
}
