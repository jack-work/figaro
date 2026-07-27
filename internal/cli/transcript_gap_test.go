package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// ---------------------------------------------------------------------------
// GAPS: one row, and the fetch that means it usually never paints.
//
// THE DEGENERATE CASE IS THE ACCEPTANCE TEST. An ordinary aria — no jumps, no
// eviction — must have ONE range and render NO gap, ever. If a gap appears in
// normal scroll-up the extent bookkeeping is wrong, and no rendering rule may
// paper over it. That is TestOrdinaryScrollUpNeverRendersAGap, and its canary
// is TestAFalseAdjacencyFailsTheDegenerateCase.
// ---------------------------------------------------------------------------

// gapRows is every sentinel row currently in line space.
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

// TestOrdinaryScrollUpNeverRendersAGap: the whole point. Page an aria in from
// the tail to the beginning, one screen at a time, and no hole may ever
// render — because the store must be able to say that the last node of turn t
// and the first of turn t+1 are neighbours (the extents the wire states).
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
	// And the footer tells the truth about it: nothing is missing any more.
	if !tr.whole() {
		t.Fatal("the whole aria is loaded and the footer still marks the total")
	}
}

// TestAFalseAdjacencyFailsTheDegenerateCase is the canary for the test above:
// force a hole the store cannot know about (evict the middle) and the
// one-range assertion — and the no-gap assertion — must both fail. An
// assertion that has never failed is not evidence.
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
	// The hole a jump or a mid-history eviction makes.
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

// TestGapIsExactlyOneRowWhateverItHides: two holes of wildly different size
// occupy the same number of lines. Row height is only knowable by rendering,
// so a proportional placeholder would be a number we invented.
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
	// Both holes cost their content minus ONE row. The difference between them
	// is entirely the content they swallowed, never the sentinel.
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

// TestGapDoesNotCorruptTheCoordinates: `:` jump and Ctrl-O read nodeSpanOf and
// the line index. A gap row carries no node, and must therefore be invisible
// to both — the coordinates of every real node must be exactly what they were
// before the hole appeared, shifted only by the rows the hole actually
// replaced.
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
	// The line the span names really is that node's row, gap row and all.
	if line := plainBody(tr.lineAt(got.first)); strings.Contains(line, "not loaded") {
		t.Fatalf("nodeSpanOf pointed at the sentinel: %q", line)
	}
	// And the jump lands on it rather than on the hole.
	tr.startJump(jumpTarget{turn: 55})
	if tr.jump != nil || tr.jumpNote != "" {
		t.Fatalf(":55 did not land across the hole: %v %q", tr.jump, tr.jumpNote)
	}
	if tr.offset != got.first-tr.index.entries[tr.index.entryAt(got.first)].sepHeight() {
		// The landing snaps the ENTRY's first row to the top, which is the
		// separator when it has one; either is fine, the hole is not.
		if line := plainBody(tr.lineAt(tr.offset)); strings.Contains(line, "not loaded") {
			t.Fatalf(":55 landed on the sentinel row")
		}
	}
}

// TestFooterMarksAnIncompleteBuffer: the total is ROWS WE HOLD. With anything
// missing it is marked, so the number is never read as the size of the
// conversation.
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
	// Load the whole thing and the mark goes away.
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

// TestEnsureOnBindClosesTheHole is the fetch trigger: a hole within the
// prefetch distance of the viewport produces a FILL request, and serving it
// through the real Client.Ensure — with ReadBefore as the fetcher — closes the
// hole and the sentinel stops being drawn.
func TestEnsureOnBindClosesTheHole(t *testing.T) {
	history := transcriptHistory(60)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	reads := 0
	client.SetFetcher(func(ctx context.Context, before aria.Anchor, limit int) (aria.Fetched, error) {
		reads++
		p := committedPage(readBeforeAt(history, before, limit))
		return aria.Fetched{Msgs: p.msgs, Extents: p.extents, More: p.more}, nil
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

	// Park the viewport on the sentinel: BINDING IT is the fetch trigger.
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

// TestPrefetchDistanceMeansTheSentinelUsuallyNeverPaints: a hole two screens
// below the viewport is already being fetched; one far away is not.
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
	// Parked far above it: no fill, and the row is not on screen either.
	tr.offset = gapAt + 4*transcriptPrefetchScreens*tr.h
	if req, ok := tr.pageCursor(); ok && req.fill != nil {
		t.Fatalf("filled a hole %d rows away", tr.offset-gapAt)
	}
	// Coming within the prefetch distance — still a screen and a half ABOVE
	// the viewport's top, so it cannot be on screen — arms the fill.
	tr.offset = gapAt + transcriptPrefetchScreens*tr.h - 1
	if tr.offset <= gapAt {
		t.Fatal("fixture: the viewport already shows the hole; this proves nothing")
	}
	req, ok := tr.pageCursor()
	if !ok || req.fill == nil {
		t.Fatalf("no fill armed at the prefetch distance: %+v %v", req, ok)
	}
}

// TestGapSurvivesAResizeWithoutMovingTheReader: the sentinel is one row at
// every width, and the viewport anchor can name it (its key is its own first
// missing anchor), so a resize with the hole at the top does not jump.
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

// TestLiveTailStillArrivesBesideAGap: a hole in the middle of the window must
// not take the window off the tail — the open turn is still the last thing in
// it. (The 2a-part-2 property, re-asserted with a hole present.)
func TestLiveTailStillArrivesBesideAGap(t *testing.T) {
	history := transcriptHistory(60)
	client := aria.NewClient()
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 61, Live: &aria.Live{Nodes: []aria.NodeDelta{{
		ID: 0, Set: map[string]any{"type": string(livedoc.NodeProse), "markdown": "still streaming"},
	}}}}}}})
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
