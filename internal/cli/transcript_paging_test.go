package cli

// A double must call the real paginator; one that sliced history itself
// encoded the opposite semantics and kept thirty tests green.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	livelog "github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// Without Page.More.Before the store believes it holds the whole aria.
func applyTail(client *aria.Client, p aria.Page) {
	client.Apply(p, aria.Notify)
	client.SetMoreBefore(p.More.Before)
}

// pageOnce reports false when the pager wanted nothing or grew over history
// the store already held.
func pageOnce(tr *transcript, history []aria.TurnPart) bool {
	req, ok := tr.pageCursor()
	if !ok {
		return false
	}
	at := req.at
	tr.applyPage(req, readBeforeAt(history, at, req.limit))
	return true
}

// The anchor restore pushes the offset down as the window grows, so the
// reader is re-parked at the top each time.
func pageToFloor(tr *transcript, history []aria.TurnPart) int {
	n := 0
	for range 200 {
		tr.offset = 0
		if !pageOnce(tr, history) {
			return n
		}
		n++
	}
	return n
}

func transcriptHistory(n int) []aria.TurnPart {
	out := make([]aria.TurnPart, n)
	for i := range out {
		out[i] = aria.TurnPart{Turn: aria.Turn{ID: uint64(i + 1), Sealed: true, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("message-%03d", i+1)}}}}
	}
	return out
}

func testSelectionPoint(lt, index int, node livedoc.Node) selectionPoint {
	return selectionPoint{nodeRef: nodeRef{turn: lt, index: index}, hash: nodeHash(node)}
}

// fixtureMemo keys on the slice's backing array: re-measuring the fixture on
// every page was O(N^2) json.Marshal in a search, harness cost not production.
var fixtureMemo struct {
	data   *aria.TurnPart
	n      int
	turns  []aria.Turn
	widest int
}

func fixtureView(history []aria.TurnPart) ([]aria.Turn, int) {
	if len(history) == 0 {
		return nil, 1
	}
	if fixtureMemo.data == &history[0] && fixtureMemo.n == len(history) {
		return fixtureMemo.turns, fixtureMemo.widest
	}
	turns := make([]aria.Turn, len(history))
	widest := 1
	for i, p := range history {
		turns[i] = p.Turn
		for _, n := range p.Nodes {
			if s := nodeBytes(n); s > widest {
				widest = s
			}
		}
	}
	fixtureMemo.data, fixtureMemo.n = &history[0], len(history)
	fixtureMemo.turns, fixtureMemo.widest = turns, widest
	return turns, widest
}

// readBefore routes through the real livelog.Paginate; `parts` is translated to
// bytes from the fixture's widest node.
func readBefore(history []aria.TurnPart, before, parts int) aria.Page {
	return readBeforeAt(history, aria.Anchor{Turn: uint64(before)}, parts)
}

func readBeforeAt(history []aria.TurnPart, at aria.Anchor, parts int) aria.Page {
	if parts <= 0 {
		return aria.Page{}
	}
	turns, widest := fixtureView(history)
	return livelog.PaginateBefore(turns, at, aria.Anchor{}, parts*widest)
}

// nodeBytes mirrors aria's unexported nodeSize.
func nodeBytes(n livedoc.Node) int {
	b, err := json.Marshal(n)
	if err != nil {
		return 1
	}
	return len(b)
}

// Paging older grows the window downward; the head stays at the live tail.
func TestTranscript_PagingOlderKeepsTheLiveTail(t *testing.T) {
	history := transcriptHistory(200)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 201, Live: &aria.Live{Nodes: []aria.NodeDelta{{
		ID: 0, Set: map[string]any{"type": "prose", "markdown": "still streaming"},
	}}}}}}}, aria.Notify)
	ft := ldrender.NewFakeTerminal(50, 8)
	tr := newTranscript(ft, 50, 8, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	tr.follow = false

	newest := tr.messages()
	tailTurn := newest[len(newest)-1].Turn
	for range 4 {
		tr.offset = 0
		if !pageOnce(tr, history) {
			t.Fatal("expected an older page")
		}
	}
	held := tr.messages()
	if held[0].Turn >= newest[0].Turn {
		t.Fatalf("window did not grow downward: oldest %d -> %d", newest[0].Turn, held[0].Turn)
	}
	if got := held[len(held)-1].Turn; got != tailTurn {
		t.Fatalf("window left the tail: newest %d, want %d", got, tailTurn)
	}
	if open := tr.openMessage(); open == nil || open.Turn != 201 {
		t.Fatalf("the live turn left the window: %+v", open)
	}
	// Nothing jumped or evicted, so the store holds one contiguous range.
	if got := len(client.Store().Ranges()); got != 1 {
		t.Fatalf("ordinary scroll-up left %d ranges; a gap would render", got)
	}
	tr.key('G')
	if !tr.follow {
		t.Fatal("G did not re-attach")
	}
	m := tr.messages()
	if got := m[len(m)-1].Turn; got != tailTurn {
		t.Fatalf("G did not restore the tail, newest = %d", got)
	}
}

func TestTranscript_SearchPagesOlderWithBoundedRetention(t *testing.T) {
	history := transcriptHistory(200)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.find("message-025")
	for tr.searchingHistory() {
		if !pageOnce(tr, history) {
			break
		}
	}
	if tr.searchingHistory() {
		t.Fatal("search did not settle")
	}
	lines := tr.lines()
	if tr.offset >= len(lines) || !strings.Contains(lines[tr.offset], "message-025") {
		t.Fatalf("search offset %d did not land on match", tr.offset)
	}
}

func TestTranscript_SelectionSurvivesPayloadEviction(t *testing.T) {
	history := transcriptHistory(200)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	tr.selection = nodeSelection{
		active: true,
		anchor: testSelectionPoint(200, 0, history[199].Nodes[0]),
		focus:  testSelectionPoint(200, 0, history[199].Nodes[0]),
	}
	for range transcriptPageLimit {
		tr.offset = 0
		if !pageOnce(tr, history) {
			t.Fatal("expected older page")
		}
	}
	tr.selection.focus = testSelectionPoint(111, 0, history[110].Nodes[0])
	plan, ok := tr.selectionPlan()
	if !ok {
		t.Fatal("selection endpoints were lost after eviction")
	}
	text, err := selectionText(plan, transcriptPageSize, func(at aria.Anchor, limit int) (aria.Page, error) {
		return readBeforeAt(history, at, limit), nil
	})
	if err != nil || !strings.Contains(text, "message-111") || !strings.Contains(text, "message-200") {
		t.Fatalf("rehydrated selected text = %q, %v", text, err)
	}
	held := len(tr.messages())
	tr.clearSelection()
	if got := len(tr.messages()); got != held {
		t.Fatalf("clearing the selection moved the window: %d -> %d messages", held, got)
	}
}

func TestTranscript_ResizeAnchorsPagedMessage(t *testing.T) {
	history := transcriptHistory(200)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	tr.lines()
	for i, k := range tr.lineKey {
		if k.turn() == 190 {
			tr.offset = i
			break
		}
	}
	tr.resize(32, 8)
	if tr.offset >= len(tr.lineKey) || tr.lineKey[tr.offset].turn() != 190 {
		t.Fatalf("resize moved anchor to turn %d", tr.lineKey[tr.offset].turn())
	}
}

func TestTranscript_SearchFindsAMatchInTheGrownWindow(t *testing.T) {
	history := transcriptHistory(200)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	for range 4 {
		tr.offset = 0
		pageOnce(tr, history)
	}
	tr.find("message-190")
	for tr.searchingHistory() {
		if !pageOnce(tr, history) {
			break
		}
	}
	lines := tr.lines()
	if tr.offset >= len(lines) || !strings.Contains(lines[tr.offset], "message-190") {
		t.Fatalf("newer search offset %d did not land on match", tr.offset)
	}
}

func TestTranscript_SelectsOpenNodeAfterLeavingFollow(t *testing.T) {
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{transcriptHistory(1)[0]}}, aria.Notify)
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(2), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{
		ID: 0, Set: map[string]any{"type": "prose", "markdown": "streaming prose"},
	}}}}}}}, aria.Notify)
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.selectNode(-1, false)
	if tr.openMessage() == nil || tr.selection.focus.turn != 2 || !tr.selection.active {
		t.Fatalf("open selection lost: open=%v selection=%+v", tr.openMessage() != nil, tr.selection)
	}
	if text, err := selectedTextForTest(tr, transcriptHistory(1)); err != nil || text != "streaming prose" {
		t.Fatalf("open selected text = %q, %v", text, err)
	}
}

// An empty ReadBefore proves the floor; the pager then asks for nothing more.
func TestTranscript_ScrollUpReachesTheFloorAndStops(t *testing.T) {
	history := transcriptHistory(120)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	tr.offset = 0
	if n := pageToFloor(tr, history); n == 0 {
		t.Fatal("expected the walk to fetch at least one page")
	}
	if !tr.atAriaFloor() {
		t.Fatal("the walk stopped without proving the floor")
	}
	if got := tr.messages()[0].Turn; got != 1 {
		t.Fatalf("the window's floor is turn %d, want the beginning", got)
	}
	if got := len(client.Store().Ranges()); got != 1 {
		t.Fatalf("paging the whole aria in left %d ranges; want one", got)
	}
	tr.offset = 0
	if _, ok := tr.pageCursor(); ok {
		t.Fatal("the pager kept asking for history below the beginning")
	}
}
