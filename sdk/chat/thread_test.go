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
