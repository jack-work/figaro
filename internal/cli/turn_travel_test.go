package cli

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

type travelRead struct {
	at       aria.Anchor
	backward bool
	budget   int
}

type travelReader struct {
	turns []aria.Turn
	calls []travelRead
}

func (r *travelReader) Read(_ context.Context, at aria.Anchor, budget int) (aria.Page, error) {
	r.calls = append(r.calls, travelRead{at: at, budget: budget})
	return aria.Paginate(r.turns, at, aria.Forward, 8192), nil
}

func (r *travelReader) ReadBefore(_ context.Context, at aria.Anchor, budget int) (aria.Page, error) {
	r.calls = append(r.calls, travelRead{at: at, backward: true, budget: budget})
	return aria.PaginateBefore(r.turns, at, 8192), nil
}

func (*travelReader) Queued(context.Context) (*rpc.QueuedResponse, error) {
	return &rpc.QueuedResponse{}, nil
}

func travelFixture(tb testing.TB, count, nodes int) (*interactiveInput, *travelReader) {
	tb.Helper()
	r := &travelReader{}
	for id := 1; id <= count; id++ {
		tn := aria.Turn{ID: uint64(id), Sealed: true, Inquiry: fmt.Sprintf("question %d", id)}
		for n := 0; n < nodes; n++ {
			tn.Nodes = append(tn.Nodes, livedoc.Node{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK,
				Output: strings.Repeat("payload ", 512), Summary: fmt.Sprintf("turn %d node %d", id, n)})
		}
		r.turns = append(r.turns, tn)
	}
	set := &renderSettings{}
	lt := newLivelogTurn(&countingWriter{}, 72, 18, set, "travel", time.Time{}, newSessionStatus("travel", time.Time{}), nil, nil)
	lt.enterTranscript()
	lt.apply(aria.PaginateBefore(r.turns, aria.Anchor{}, 8192))
	lt.client.SetMoreBefore(true)
	in := &interactiveInput{lt: lt, fcli: r, mu: &sync.Mutex{}, set: set, figaroID: "travel", cancel: func() {}, disconnectCh: make(chan struct{}, 1)}
	lt.setHistoryFetcher(in.historyFetcher())
	return in, r
}

func assertInquiry(tb testing.TB, in *interactiveInput, turn int) {
	tb.Helper()
	tr := in.lt.tr
	if tr.jump != nil || !tr.selection.active || tr.selection.focus.nodeRef != (nodeRef{turn: turn, index: inquiryNode}) {
		visible, _ := tr.stickyTurn()
		tb.Fatalf("want inquiry %d; viewport turn=%d jump=%+v selection=%+v note=%q", turn, visible, tr.jump, tr.selection, tr.jumpNote)
	}
	if !strings.Contains(strings.Join(viewportRows(tr, 8), "\n"), fmt.Sprintf("question %d", turn)) {
		tb.Fatalf("question %d is not visible: %q", turn, viewportRows(tr, 8))
	}
}

func TestTurnTravelSparsePages(t *testing.T) {
	for _, sticky := range []bool{false, true} {
		t.Run(fmt.Sprintf("sticky=%v", sticky), func(t *testing.T) {
			in, reader := travelFixture(t, 8, 64)
			in.set.sticky = sticky
			feed(t, in, ":1\r")
			assertInquiry(t, in, 1)
			if len(reader.calls) != 1 || reader.calls[0] != (travelRead{at: aria.Anchor{Turn: 1}}) {
				t.Fatalf(":1 should read one default-sized page at 1.0: %+v", reader.calls)
			}
			for turn := 2; turn <= 8; turn++ {
				reader.calls = nil
				feed(t, in, "\x1bn")
				assertInquiry(t, in, turn)
				if len(reader.calls) != 1 || reader.calls[0] != (travelRead{at: aria.Anchor{Turn: uint64(turn)}}) {
					t.Fatalf("next inquiry %d must read its head, not fill a hole: %+v", turn, reader.calls)
				}
			}
			for turn := 7; turn >= 1; turn-- {
				reader.calls = nil
				feed(t, in, "\x1bp")
				assertInquiry(t, in, turn)
				if len(reader.calls) != 0 {
					t.Fatalf("resident inquiry %d should use :N's local landing: %+v", turn, reader.calls)
				}
			}
		})
	}
}

func TestPreviousTurnFetchesClippedHeadDirectly(t *testing.T) {
	in, reader := travelFixture(t, 8, 64)
	feed(t, in, ":7\r")
	assertInquiry(t, in, 7)
	reader.calls = nil
	feed(t, in, "\x1bp")
	assertInquiry(t, in, 6)
	want := []travelRead{{at: aria.Anchor{Turn: 7}, backward: true}, {at: aria.Anchor{Turn: 6}}}
	if fmt.Sprint(reader.calls) != fmt.Sprint(want) {
		t.Fatalf("expected a neighbor lookup then :6's head request; got %+v", reader.calls)
	}
}

func TestJumpResponseDoesNotOverrideNewTarget(t *testing.T) {
	in, reader := travelFixture(t, 8, 64)
	tr := in.lt.tr
	tr.startJump(jumpTarget{turn: 1})
	old, ok := tr.pageCursor()
	if !ok {
		t.Fatal("no request for turn 1")
	}
	tr.startJump(jumpTarget{turn: 3})
	p, _ := reader.Read(context.Background(), old.at, 0)
	tr.applyPage(old, p)
	if tr.jump == nil || tr.jump.target.turn != 3 {
		t.Fatal("stale response replaced target 3")
	}
	req, ok := tr.pageCursor()
	if !ok || req.at != (aria.Anchor{Turn: 3}) {
		t.Fatalf("want target 3, got %+v", req)
	}
	p, _ = reader.Read(context.Background(), req.at, 0)
	tr.applyPage(req, p)
	assertInquiry(t, in, 3)
}

func BenchmarkTurnTravelResident(b *testing.B) {
	in, _ := travelFixture(b, 8, 4)
	tr := in.lt.tr
	tr.follow = false
	tr.client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 1, Inquiry: "question 1", Sealed: true}}, {Turn: aria.Turn{ID: 2, Inquiry: "question 2", Sealed: true}}}}, aria.Quiet)
	tr.from = aria.Anchor{Turn: 1}
	tr.invalidateWindow()
	tr.settle()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr.startJump(jumpTarget{turn: 1})
		tr.jumpTurn(1)
	}
}

func TestTurnTravelEdges(t *testing.T) {
	in, reader := travelFixture(t, 2, 4)
	feed(t, in, ":1\r")
	reader.calls = nil
	feed(t, in, "\x1bp")
	assertInquiry(t, in, 1)
	if len(reader.calls) != 0 {
		t.Fatalf("before first turn: %+v", reader.calls)
	}
	feed(t, in, ":2\r")
	reader.calls = nil
	feed(t, in, "\x1bn")
	assertInquiry(t, in, 2)
	if len(reader.calls) != 1 || reader.calls[0].at != (aria.Anchor{Turn: 3}) {
		t.Fatalf("next-turn boundary probe: %+v", reader.calls)
	}
}

func TestShiftControlTurnTravel(t *testing.T) {
	in, _ := travelFixture(t, 8, 64)
	feed(t, in, ":1\r")
	feed(t, in, "\x1b[110;6u")
	assertInquiry(t, in, 2)
	feed(t, in, "\x1b[112;6u")
	assertInquiry(t, in, 1)
}

func TestNextTurnDoesNotSkipUnloadedTurns(t *testing.T) {
	in, reader := travelFixture(t, 8, 64)
	tr := in.lt.tr
	for _, turn := range []uint64{1, 7} {
		tr.client.Apply(aria.Paginate(reader.turns, aria.Anchor{Turn: turn}, aria.Forward, 8192), aria.Quiet)
	}
	tr.follow = false
	tr.from = aria.Anchor{Turn: 1}
	tr.invalidateWindow()
	tr.startJump(jumpTarget{turn: 1})
	assertInquiry(t, in, 1)
	reader.calls = nil
	feed(t, in, "\x1bn")
	assertInquiry(t, in, 2)
	if len(reader.calls) != 1 || reader.calls[0].at != (aria.Anchor{Turn: 2}) || reader.calls[0].backward {
		t.Fatalf("next from 1 must read 2.0: %+v", reader.calls)
	}
}
