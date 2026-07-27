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

func TestTranscript_BoundedPagesRefetchNewerAndFollowLive(t *testing.T) {
	history := transcriptHistory(200)
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	client.Apply(readBefore(history, recentCursor, transcriptPageSize))
	ft := ldrender.NewFakeTerminal(50, 8)
	tr := newTranscript(ft, 50, 8, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	tr.follow = false

	for range 4 {
		tr.offset = 0
		tr.checkOlder = true
		req, ok := tr.pageCursor()
		if !ok || req.direction != pageOlder {
			t.Fatalf("older request = %+v, %v", req, ok)
		}
		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize)))
	}
	if len(tr.pages) != transcriptPageLimit {
		t.Fatalf("retained pages = %d, want %d", len(tr.pages), transcriptPageLimit)
	}
	if got := len(tr.messages()); got != transcriptPageSize*transcriptPageLimit {
		t.Fatalf("retained messages = %d", got)
	}
	if len(tr.newer) == 0 {
		t.Fatal("evicted newer pages must retain a refetch cursor")
	}
	before := tr.messages()
	history = append(history, aria.TurnPart{Turn: aria.Turn{ID: uint64(201), Sealed: true, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "message-201"}}}})
	client.Apply(aria.Page{Parts: []aria.TurnPart{history[200]}})
	for range 2 {
		tr.offset = len(tr.lineKey)
		tr.checkNewer = true
		req, ok := tr.pageCursor()
		if !ok || req.direction != pageNewer {
			t.Fatalf("newer request = %+v, %v", req, ok)
		}
		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize)))
	}
	after := tr.messages()
	if after[len(after)-1].Turn <= before[len(before)-1].Turn {
		t.Fatalf("newer page did not advance window: %d -> %d", before[len(before)-1].Turn, after[len(after)-1].Turn)
	}
	if got := after[len(after)-1].Turn; got != 200 {
		t.Fatalf("stable refetch included live LT or skipped old tail; newest = %d", got)
	}
	if len(tr.pages) != transcriptPageLimit {
		t.Fatalf("newer refetch retained %d pages", len(tr.pages))
	}

	heldOldest := after[0].Turn
	tr.render()
	if got := tr.messages()[0].Turn; got != heldOldest {
		t.Fatalf("live update moved held history: %d -> %d", heldOldest, got)
	}
	tr.key('G')
	messages := tr.messages()
	if got := messages[len(messages)-1].Turn; got != 201 {
		t.Fatalf("G did not restore live tail, newest LT = %d", got)
	}
}

func TestTranscript_SearchPagesOlderWithBoundedRetention(t *testing.T) {
	history := transcriptHistory(200)
	client := aria.NewClient()
	client.Apply(readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.find("message-025")
	for tr.searchingHistory() {
		req, ok := tr.pageCursor()
		if !ok {
			break
		}
		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize)))
	}
	if tr.searchingHistory() {
		t.Fatal("search did not settle")
	}
	if len(tr.pages) > transcriptPageLimit {
		t.Fatalf("search retained %d pages", len(tr.pages))
	}
	lines := tr.lines()
	if tr.offset >= len(lines) || !strings.Contains(lines[tr.offset], "message-025") {
		t.Fatalf("search offset %d did not land on match", tr.offset)
	}
}

func TestTranscript_SelectionSurvivesPayloadEviction(t *testing.T) {
	history := transcriptHistory(200)
	client := aria.NewClient()
	client.Apply(readBefore(history, recentCursor, transcriptPageSize))
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
		tr.checkOlder = true
		req, ok := tr.pageCursor()
		if !ok {
			t.Fatal("expected older page")
		}
		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize)))
	}
	if len(tr.pages) != transcriptPageLimit {
		t.Fatalf("selection retained %d payload pages", len(tr.pages))
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
	tr.clearSelection()
	if len(tr.pages) != transcriptPageLimit {
		t.Fatalf("clearing selection retained %d pages, want %d", len(tr.pages), transcriptPageLimit)
	}
}

func TestTranscript_ResizeAnchorsPagedMessage(t *testing.T) {
	history := transcriptHistory(200)
	client := aria.NewClient()
	client.Apply(readBefore(history, recentCursor, transcriptPageSize))
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

func TestTranscript_SearchTraversesEvictedNewerPages(t *testing.T) {
	history := transcriptHistory(200)
	client := aria.NewClient()
	client.Apply(readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	for range 4 {
		tr.offset = 0
		tr.checkOlder = true
		req, _ := tr.pageCursor()
		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize)))
	}
	tr.find("message-190")
	for tr.searchingHistory() {
		req, ok := tr.pageCursor()
		if !ok {
			break
		}
		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize)))
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

func TestTranscript_ReloadsOldestAfterNewerEviction(t *testing.T) {
	history := transcriptHistory(120)
	client := aria.NewClient()
	client.Apply(readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	for !tr.noMoreOlder {
		tr.offset = 0
		tr.checkOlder = true
		req, ok := tr.pageCursor()
		if !ok {
			break
		}
		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize)))
	}
	tr.offset = len(tr.lineKey)
	tr.checkNewer = true
	req, ok := tr.pageCursor()
	if !ok {
		t.Fatal("expected newer refetch")
	}
	tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize)))
	if tr.noMoreOlder {
		t.Fatal("evicting the oldest page must re-enable older paging")
	}
	tr.offset = 0
	tr.checkOlder = true
	req, ok = tr.pageCursor()
	if !ok || req.direction != pageOlder {
		t.Fatalf("oldest page was not reloadable: %+v, %v", req, ok)
	}
}
