package cli

// WHAT THIS FILE ONCE FAILED TO CATCH
//
// Its readBefore double sliced history itself and treated `limit` as a COUNT
// OF PARTS. Production's limit is a BYTE BUDGET, and the double never called
// aria.Paginate at all. Result: ~30 tests in this file stayed GREEN while the
// live pager rendered ONE NODE for an 800-node aria — and, separately, a
// duplicated message at EVERY page boundary went unseen because the double
// excluded the anchor while production included it. The double did not merely
// fail to model production; it encoded the OPPOSITE semantics.
//
// Rule: a double must call the real function. If it cannot, it is a fixture,
// not a test. See the tmux-testing skill (~/.config/figaro/skills/tmux-testing.md).

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// applyTail folds the opening tail page into the client the way the input
// loop's cold enter does — including the WIRE'S ANSWER about the beginning
// (Page.More.Before), which is what the pager reads back as "can I still page
// older history". Applying the page without it leaves the store believing it
// holds the whole aria, and no test would ever page.
func applyTail(client *aria.Client, p aria.Page) {
	client.Apply(p)
	client.SetMoreBefore(p.More.Before)
}

// pageOnce drives ONE older-history fetch exactly as the input loop does: ask
// the pager for a cursor, read at it, apply. It reports whether a fetch
// happened — false means the pager wanted nothing (or grew its window over
// history the store already held, which costs no read).
func pageOnce(tr *transcript, history []aria.TurnPart) bool {
	req, ok := tr.pageCursor()
	if !ok {
		return false
	}
	at := aria.Anchor{Turn: uint64(req.before), Node: uint64(req.beforeNode)}
	tr.applyPage(req, committedPage(readBeforeAt(history, at, req.limit)))
	return true
}

// pageToFloor drives the fetch loop until the pager stops asking, with the
// reader parked at the top of the window throughout (the anchor restore pushes
// the offset down as the window grows, which is the prefetch distance doing its
// job — a reader who keeps scrolling keeps asking). Bounded so a bug reports
// instead of hanging.
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

// readBefore is the test double for the read RPC. It routes through the REAL
// aria.Paginate, with the real Anchor/Backward semantics the server uses
// (figaro/server.go: Anchor{Turn: req.Before}, Backward, budget).
//
// It used to slice the history itself, treating `parts` as a count. That is why
// ~30 tests stayed green while the live pager rendered a SINGLE node for an
// 800-node aria: the double never executed the paginator, so a direction bug,
// an off-by-one at the anchor, or a clipping bug could not fail a test.
//
// Tests want to say "a page of 30", but the wire only understands BYTES. Rather
// than push that translation into 37 call sites, do it here from the fixture's
// own node size — so the count stays the test's vocabulary while the paginator
// still runs for real.
// fixtureMemo caches the per-fixture work readBefore would otherwise redo on
// EVERY page: projecting []TurnPart to []Turn, and measuring the widest
// marshalled node. Both depend only on `history`, but a search walks the whole
// aria one page at a time, so recomputing them was O(N^2) json.Marshal calls
// plus an O(N) copy per page. That is harness cost, not production cost — the
// server holds its turns and never re-measures the log to serve a page — but it
// made BenchmarkTranscriptPagedSearchMiss/10000 158x slower than the same
// benchmark on the pre-turn-addressing tree and put /50000 out of reach, which
// hid the very numbers the acceptance matrix asks for. A one-entry cache keyed
// by the slice's own backing array is enough: every call site loops over one
// fixture at a time.
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

func readBefore(history []aria.TurnPart, before, parts int) aria.Page {
	return readBeforeAt(history, aria.Anchor{Turn: uint64(before)}, parts)
}

// readBeforeAt is readBefore anchored on a NODE as well as a turn — the shape
// the pager and the selection walk actually ask for.
func readBeforeAt(history []aria.TurnPart, at aria.Anchor, parts int) aria.Page {
	if parts <= 0 {
		return aria.Page{}
	}
	turns, widest := fixtureView(history)
	return aria.PaginateBefore(turns, at, parts*widest)
}

// nodeBytes mirrors aria's unexported nodeSize: the paginator spends its budget
// in bytes of marshalled node.
func nodeBytes(n livedoc.Node) int {
	b, err := json.Marshal(n)
	if err != nil {
		return 1
	}
	return len(b)
}

// TestTranscript_PagingOlderKeepsTheLiveTail: scrolling up grows the window
// DOWNWARD into the one owner, and the head of the window stays where it was —
// at the live tail.
//
// This is the phase-2a-part-2 falsifier. Before it, the first page of older
// history moved the window off the tail into a second copy (t.pages), and
// openMessage went quiet: the live turn was not in the window at all. The
// assertion that it is still there is the whole claim.
func TestTranscript_PagingOlderKeepsTheLiveTail(t *testing.T) {
	history := transcriptHistory(200)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 201, Live: &aria.Live{Nodes: []aria.NodeDelta{{
		ID: 0, Set: map[string]any{"type": "prose", "markdown": "still streaming"},
	}}}}}}})
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
	// THE DEGENERATE CASE. Nothing was jumped to and nothing was evicted, so
	// everything the store holds is ONE contiguous range and no gap can render.
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

// TestTranscript_SearchFindsAMatchInTheGrownWindow: after paging history in,
// a search for something in the NEWER part of the window still lands on it —
// the window never left the tail, so there is no "newer" to traverse back to.
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
	client.Apply(aria.Page{Parts: []aria.TurnPart{transcriptHistory(1)[0]}})
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(2), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{
		ID: 0, Set: map[string]any{"type": "prose", "markdown": "streaming prose"},
	}}}}}}})
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.selectNode(-1, false)
	// The open message is still IN the window after the detach (it is the last
	// thing in the store's tail interval), which is what makes it selectable.
	if tr.openMessage() == nil || tr.selection.focus.turn != 2 || !tr.selection.active {
		t.Fatalf("open selection lost: open=%v selection=%+v", tr.openMessage() != nil, tr.selection)
	}
	if text, err := selectedTextForTest(tr, transcriptHistory(1)); err != nil || text != "streaming prose" {
		t.Fatalf("open selected text = %q, %v", text, err)
	}
}

// TestTranscript_ScrollUpReachesTheFloorAndStops: paging back to the beginning
// proves the floor (an empty ReadBefore) and the pager then asks for nothing
// more — the un-latched replacement for noMoreOlder.
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
