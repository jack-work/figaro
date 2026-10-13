package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// An ordinary aria has one range and renders no gap, ever; no rendering rule
// may paper over extent bookkeeping.

func gapRows(tr *transcript) []string {
	var out []string
	tr.buildIndex()
	for i := range tr.index.total {
		if l := plainBody(tr.lineAt(i)); strings.Contains(l, "not loaded") {
			out = append(out, l)
		}
	}
	return out
}

func TestOrdinaryScrollUpNeverRendersAGap(t *testing.T) {
	history := transcriptHistory(120)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(60, 12), 60, 12, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	for range 200 {
		tr.offset = 0
		if rows := gapRows(tr); len(rows) > 0 {
			t.Fatalf("an ordinary scroll-up rendered a gap: %q (ranges=%d)",
				rows, len(client.Store().Ranges()))
		}
		if !pageOnce(tr, history) {
			break
		}
	}
	if got := len(client.Store().Ranges()); got != 1 {
		t.Fatalf("the whole aria came in as %d ranges; the degenerate case is ONE", got)
	}
	if got := tr.messages()[0].Turn; got != 1 {
		t.Fatalf("the walk stopped at turn %d, not the beginning", got)
	}
	if !tr.whole() {
		t.Fatal("the whole aria is loaded and the footer still marks the total")
	}
}

// The canary for the test above: a forced hole must fail both assertions.
func TestAFalseAdjacencyFailsTheDegenerateCase(t *testing.T) {
	history := transcriptHistory(60)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(60, 12), 60, 12, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	tr.offset = 0
	pageOnce(tr, history)
	if got := len(client.Store().Ranges()); got != 1 {
		t.Fatalf("fixture: expected one range before the eviction, got %d", got)
	}
	client.Store().Evict(aria.Anchor{Turn: 40}, aria.Anchor{Turn: 44})
	tr.invalidateWindow()
	if got := len(client.Store().Ranges()); got != 2 {
		t.Fatalf("the canary did not make a hole: %d ranges", got)
	}
	rows := gapRows(tr)
	if len(rows) != 1 {
		t.Fatalf("a hole must render as EXACTLY ONE row; got %d: %q", len(rows), rows)
	}
	if !strings.Contains(rows[0], "5 turns not loaded") {
		t.Fatalf("the sentinel does not name the hole: %q", rows[0])
	}
}

// Row height is only knowable by rendering, so a hole is one row whatever it hides.
func TestGapIsExactlyOneRowWhateverItHides(t *testing.T) {
	measure := func(from, to uint64) (int, string) {
		history := transcriptHistory(120)
		client := aria.NewClient()
		applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
		tr := newTranscript(ldrender.NewFakeTerminal(60, 12), 60, 12, ldrender.NodeText{}, client, "", time.Time{})
		tr.enter()
		tr.follow = false
		for range 6 {
			tr.offset = 0
			pageOnce(tr, history)
		}
		before := tr.index.total
		client.Store().Evict(aria.Anchor{Turn: from}, aria.Anchor{Turn: to})
		tr.invalidateWindow()
		tr.buildIndex()
		rows := gapRows(tr)
		if len(rows) != 1 {
			t.Fatalf("hole [%d,%d] rendered %d sentinel rows", from, to, len(rows))
		}
		return before - tr.index.total, rows[0]
	}
	small, smallRow := measure(100, 100)
	big, bigRow := measure(80, 100)
	if smallRow == bigRow {
		t.Fatalf("the two sentinels are identical (%q); the count is not being read", smallRow)
	}
	if !strings.Contains(smallRow, "1 turn not loaded") {
		t.Fatalf("a one-turn hole says %q", smallRow)
	}
	if !strings.Contains(bigRow, "21 turns not loaded") {
		t.Fatalf("a 21-turn hole says %q", bigRow)
	}
	if big <= small {
		t.Fatalf("fixture: the big hole (%d rows saved) swallowed no more than the small one (%d)", big, small)
	}
}

// A gap row carries no node, so nodeSpanOf and the jump must not see it.
func TestGapDoesNotCorruptTheCoordinates(t *testing.T) {
	history := transcriptHistory(60)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(60, 12), 60, 12, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	for range 3 {
		tr.offset = 0
		pageOnce(tr, history)
	}
	tr.buildIndex()
	ref := nodeRef{turn: 55, index: 0}
	want, ok := tr.nodeSpanOf(ref)
	if !ok {
		t.Fatal("fixture: turn 55 node 0 is not in the window")
	}
	client.Store().Evict(aria.Anchor{Turn: 40}, aria.Anchor{Turn: 44})
	tr.invalidateWindow()
	tr.buildIndex()
	got, ok := tr.nodeSpanOf(ref)
	if !ok {
		t.Fatal("the node below the hole lost its span")
	}
	if got.last-got.first != want.last-want.first {
		t.Fatalf("the node's span changed height: %v -> %v", want, got)
	}
	if line := plainBody(tr.lineAt(got.first)); strings.Contains(line, "not loaded") {
		t.Fatalf("nodeSpanOf pointed at the sentinel: %q", line)
	}
	tr.startJump(jumpTarget{turn: 55})
	if tr.jump != nil || tr.jumpNote != "" {
		t.Fatalf(":55 did not land across the hole: %v %q", tr.jump, tr.jumpNote)
	}
	if tr.offset != got.first-tr.index.entries[tr.index.entryAt(got.first)].sepHeight() {
		// The landing may snap to the entry's separator; either is fine.
		if line := plainBody(tr.lineAt(tr.offset)); strings.Contains(line, "not loaded") {
			t.Fatalf(":55 landed on the sentinel row")
		}
	}
}

// The footer total is rows held, marked while anything is missing.
func TestFooterMarksAnIncompleteBuffer(t *testing.T) {
	history := transcriptHistory(60)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(60, 12), 60, 12, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	tr.buildIndex()
	rule, _ := tr.footerRows(tr.index.total, 6)
	if !strings.Contains(stripANSI(rule), "+") {
		t.Fatalf("history exists below the floor and the footer does not say so: %q", stripANSI(rule))
	}
	for range 200 {
		tr.offset = 0
		if !pageOnce(tr, history) {
			break
		}
	}
	tr.buildIndex()
	rule, _ = tr.footerRows(tr.index.total, 6)
	if strings.Contains(stripANSI(rule), "/") && strings.Contains(stripANSI(rule), "+") {
		t.Fatalf("the whole aria is held and the footer still marks it: %q", stripANSI(rule))
	}
}

// A hole within prefetch distance produces a fill request; Client.Ensure
// closes it.
func TestEnsureOnBindClosesTheHole(t *testing.T) {
	history := transcriptHistory(60)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	reads := 0
	client.SetFetcher(func(ctx context.Context, before aria.Anchor, limit int) (aria.Page, error) {
		reads++
		return readBeforeAt(history, before, limit), nil
	})
	tr := newTranscript(ldrender.NewFakeTerminal(60, 12), 60, 12, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	for range 3 {
		tr.offset = 0
		pageOnce(tr, history)
	}
	client.Store().Evict(aria.Anchor{Turn: 45}, aria.Anchor{Turn: 47})
	tr.invalidateWindow()
	tr.buildIndex()
	if len(gapRows(tr)) != 1 {
		t.Fatal("fixture: no hole to fill")
	}

	// Binding the sentinel is the fetch trigger.
	for k := range tr.index.entries {
		if e := &tr.index.entries[k]; e.isGap() {
			tr.offset = e.start
		}
	}
	req, ok := tr.pageCursor()
	if !ok || req.fill == nil {
		t.Fatalf("binding the gap did not ask for a fill: %+v, %v", req, ok)
	}
	if err := client.Ensure(context.Background(), req.fill.From, req.fill.To); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if reads == 0 {
		t.Fatal("Ensure closed the hole without reading anything")
	}
	tr.invalidateWindow()
	if rows := gapRows(tr); len(rows) != 0 {
		t.Fatalf("the sentinel survived the fill: %q", rows)
	}
	if got := len(client.Store().Ranges()); got != 1 {
		t.Fatalf("the fill left %d ranges; a closed hole coalesces", got)
	}
}

func TestPrefetchDistanceMeansTheSentinelUsuallyNeverPaints(t *testing.T) {
	history := transcriptHistory(200)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(60, 12), 60, 12, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	for range 8 {
		tr.offset = 0
		pageOnce(tr, history)
	}
	client.Store().Evict(aria.Anchor{Turn: 170}, aria.Anchor{Turn: 172})
	tr.invalidateWindow()
	tr.buildIndex()
	var gapAt int
	for k := range tr.index.entries {
		if tr.index.entries[k].isGap() {
			gapAt = tr.index.entries[k].start
		}
	}
	if gapAt == 0 {
		t.Fatal("fixture: no hole")
	}
	tr.offset = gapAt + 4*transcriptPrefetchScreens*tr.h
	if req, ok := tr.pageCursor(); ok && req.fill != nil {
		t.Fatalf("filled a hole %d rows away", tr.offset-gapAt)
	}
	// Within prefetch distance but still a screen and a half above the top.
	tr.offset = gapAt + transcriptPrefetchScreens*tr.h - 1
	if tr.offset <= gapAt {
		t.Fatal("fixture: the viewport already shows the hole; this proves nothing")
	}
	req, ok := tr.pageCursor()
	if !ok || req.fill == nil {
		t.Fatalf("no fill armed at the prefetch distance: %+v %v", req, ok)
	}
}

// The viewport anchor can name the sentinel: its key is its first missing anchor.
func TestGapSurvivesAResizeWithoutMovingTheReader(t *testing.T) {
	history := transcriptHistory(60)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(60, 12), 60, 12, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	for range 3 {
		tr.offset = 0
		pageOnce(tr, history)
	}
	client.Store().Evict(aria.Anchor{Turn: 45}, aria.Anchor{Turn: 47})
	tr.invalidateWindow()
	tr.buildIndex()
	for k := range tr.index.entries {
		if e := &tr.index.entries[k]; e.isGap() {
			tr.offset = e.start + e.sepHeight()
		}
	}
	if !strings.Contains(plainBody(tr.lineAt(tr.offset)), "not loaded") {
		t.Fatal("fixture: the viewport is not on the sentinel")
	}
	tr.resize(40, 12)
	if got := plainBody(tr.lineAt(tr.offset)); !strings.Contains(got, "not loaded") {
		t.Fatalf("the resize moved the reader off the sentinel: %q", got)
	}
	if rows := gapRows(tr); len(rows) != 1 || len(rows[0]) == 0 {
		t.Fatalf("after the resize the hole renders %d rows: %q", len(rows), rows)
	}
}

func TestLiveTailStillArrivesBesideAGap(t *testing.T) {
	history := transcriptHistory(60)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 61, Live: &aria.Live{Nodes: []aria.NodeDelta{{
		ID: 0, Set: map[string]any{"type": string(livedoc.NodeProse), "markdown": "still streaming"},
	}}}}}}}, aria.Notify)
	tr := newTranscript(ldrender.NewFakeTerminal(60, 12), 60, 12, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	for range 3 {
		tr.offset = 0
		pageOnce(tr, history)
	}
	client.Store().Evict(aria.Anchor{Turn: 45}, aria.Anchor{Turn: 47})
	tr.invalidateWindow()
	if len(gapRows(tr)) != 1 {
		t.Fatal("fixture: no hole")
	}
	if open := tr.openMessage(); open == nil || open.Turn != 61 {
		t.Fatalf("a hole in the window silenced the live turn: %+v", open)
	}
	if !strings.Contains(strings.Join(tr.lines(), "\n"), "still streaming") {
		t.Fatal("the live turn is not drawn beside the hole")
	}
}
