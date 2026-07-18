package cli

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

func transcriptHistory(n int) []aria.Committed {
	out := make([]aria.Committed, n)
	for i := range out {
		out[i] = aria.Committed{
			LT: i + 1, Role: "assistant",
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("message-%03d", i+1)}},
		}
	}
	return out
}

func testSelectionPoint(lt, index int, node livedoc.Node) selectionPoint {
	return selectionPoint{nodeRef: nodeRef{lt: lt, index: index}, hash: nodeHash(node)}
}

func readBefore(history []aria.Committed, before, limit int) aria.AriaRead {
	hi := sort.Search(len(history), func(i int) bool { return history[i].LT >= before })
	lo := hi - limit
	if lo < 0 {
		lo = 0
	}
	return aria.AriaRead{Committed: append([]aria.Committed(nil), history[lo:hi]...)}
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
		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize).Committed))
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
	history = append(history, aria.Committed{
		LT: 201, Role: "assistant",
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "message-201"}},
	})
	client.Apply(aria.AriaRead{Committed: []aria.Committed{history[200]}})
	for range 2 {
		tr.offset = len(tr.lineLT)
		tr.checkNewer = true
		req, ok := tr.pageCursor()
		if !ok || req.direction != pageNewer {
			t.Fatalf("newer request = %+v, %v", req, ok)
		}
		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize).Committed))
	}
	after := tr.messages()
	if after[len(after)-1].LT <= before[len(before)-1].LT {
		t.Fatalf("newer page did not advance window: %d -> %d", before[len(before)-1].LT, after[len(after)-1].LT)
	}
	if got := after[len(after)-1].LT; got != 200 {
		t.Fatalf("stable refetch included live LT or skipped old tail; newest = %d", got)
	}
	if len(tr.pages) != transcriptPageLimit {
		t.Fatalf("newer refetch retained %d pages", len(tr.pages))
	}

	heldOldest := after[0].LT
	tr.render()
	if got := tr.messages()[0].LT; got != heldOldest {
		t.Fatalf("live update moved held history: %d -> %d", heldOldest, got)
	}
	tr.key('G')
	messages := tr.messages()
	if got := messages[len(messages)-1].LT; got != 201 {
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
		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize).Committed))
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
		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize).Committed))
	}
	if len(tr.pages) != transcriptPageLimit {
		t.Fatalf("selection retained %d payload pages", len(tr.pages))
	}
	tr.selection.focus = testSelectionPoint(111, 0, history[110].Nodes[0])
	plan, ok := tr.selectionPlan()
	if !ok {
		t.Fatal("selection endpoints were lost after eviction")
	}
	text, err := selectionText(plan, transcriptPageSize, func(before, limit int) (aria.AriaRead, error) {
		return readBefore(history, before, limit), nil
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
	for i, lt := range tr.lineLT {
		if lt == 190 {
			tr.offset = i
			break
		}
	}
	tr.resize(32, 8)
	if tr.offset >= len(tr.lineLT) || tr.lineLT[tr.offset] != 190 {
		t.Fatalf("resize moved anchor to LT %d", tr.lineLT[tr.offset])
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
		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize).Committed))
	}
	tr.find("message-190")
	for tr.searchingHistory() {
		req, ok := tr.pageCursor()
		if !ok {
			break
		}
		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize).Committed))
	}
	lines := tr.lines()
	if tr.offset >= len(lines) || !strings.Contains(lines[tr.offset], "message-190") {
		t.Fatalf("newer search offset %d did not land on match", tr.offset)
	}
}

func TestTranscript_SelectsOpenNodeAfterLeavingFollow(t *testing.T) {
	client := aria.NewClient()
	client.Apply(aria.AriaRead{Committed: []aria.Committed{transcriptHistory(1)[0]}})
	client.Apply(aria.AriaRead{Live: &aria.Live{
		LT: 2, V: 0, Role: "assistant",
		Nodes: []aria.NodeDelta{{
			ID: "open", Set: map[string]any{"type": "prose", "markdown": "streaming prose"},
		}},
	}})
	tr := newTranscript(ldrender.NewFakeTerminal(50, 8), 50, 8, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.selectNode(-1, false)
	if tr.heldOpen == nil || tr.selection.focus.lt != 2 || !tr.selection.active {
		t.Fatalf("open selection lost: held=%v selection=%+v", tr.heldOpen != nil, tr.selection)
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
		tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize).Committed))
	}
	tr.offset = len(tr.lineLT)
	tr.checkNewer = true
	req, ok := tr.pageCursor()
	if !ok {
		t.Fatal("expected newer refetch")
	}
	tr.applyPage(req, committedMessages(readBefore(history, req.before, transcriptPageSize).Committed))
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
