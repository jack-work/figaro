package cli

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// ---------------------------------------------------------------------------
// The invariants the D-over-(C+A) merge creates.
//
// Axis A virtualized the viewport behind a line index; axis D replaced the
// message-count page geometry with a row budget and took resetToTail off the
// per-frame path. Neither branch could pin the property that only exists once
// both are in: there is exactly ONE authority on "the retained window changed"
// (transcript.windowRev, published by invalidateWindow), so the page layer and
// the line index can never disagree about which window they describe, and the
// row budget is computed from the index's EXACT per-message row counts, not
// from an average over the row cache, which holds rows for everything the STORE
// retains, a strictly larger set than the window.
//
// D's tailRev answered "are t.pages still the client's tail?"; A's index had a
// separate per-frame shape diff deciding whether to refill lineTurn. Two checks
// over one fact is how a moved window ends up with lineTurn: resize anchoring,
// viewportAnchor: describing a window that no longer exists.
// ---------------------------------------------------------------------------

// mixedHeightHistory alternates short and tall messages so the window and the
// wider set the store retains end up holding messages of very different
// heights. That is what makes "which set did you average over" observable.
func mixedHeightHistory(n int) []aria.TurnPart {
	out := make([]aria.TurnPart, n)
	for i := range out {
		md := "short-" + itoa(i+1)
		if i+1 <= 2*n/3 { // the older two thirds are tall
			md = ""
			for l := range 14 {
				md += "tall-" + itoa(i+1) + " line-" + itoa(l) + "\n\n"
			}
		}
		out[i] = aria.TurnPart{Turn: aria.Turn{ID: uint64(i + 1), Sealed: true, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: md}}}}
	}
	return out
}

// lineLTFromIndex recomputes the slice-per-line map straight from the line
// index, independently of rebuildLineLT's "only when the shape moved"
// discipline. Any disagreement means an index change slipped past the
// invalidation signal.
func lineLTFromIndex(tr *transcript) []sliceKey {
	out := make([]sliceKey, 0, tr.index.total)
	for k := range tr.index.entries {
		e := &tr.index.entries[k]
		if e.sep {
			// Separator rows carry the FOLLOWING message's key. Driven off
			// sepRows rather than a literal so a change to the separator's
			// height cannot leave this oracle describing the old shape.
			for range sepRows {
				out = append(out, e.key)
			}
		}
		for range e.rows {
			out = append(out, e.key)
		}
	}
	return out
}

func assertIndexAgrees(t *testing.T, tr *transcript, when string) {
	t.Helper()
	if tr.index.rev != tr.windowRev {
		t.Fatalf("%s: index was built from windowRev %d, transcript is at %d",
			when, tr.index.rev, tr.windowRev)
	}
	want := lineLTFromIndex(tr)
	if len(tr.lineKey) != len(want) {
		t.Fatalf("%s: lineKey has %d entries, index has %d lines",
			when, len(tr.lineKey), len(want))
	}
	for i := range want {
		if tr.lineKey[i] != want[i] {
			t.Fatalf("%s: lineKey[%d] = %d, index says %d", when, i, tr.lineKey[i], want[i])
		}
	}
}

// TestMergedPageMutationAlwaysReachesTheIndex is the "one authority" invariant.
// Every route that moves the window: the floor dropping onto a fetched page,
// the floor rising back to the tail: must publish through invalidateWindow,
// and the line index must come out the far side describing the window that
// actually exists.
func TestMergedPageMutationAlwaysReachesTheIndex(t *testing.T) {
	history := transcriptHistory(300)
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 12), 50, 12, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	assertIndexAgrees(t, tr, "after enter")
	tr.follow = false // hold the window; following would reset it to the tail

	revs := map[uint64]bool{tr.windowRev: true}
	// Page older repeatedly: every landing drops the window's floor, which is
	// a window change and must reach the index.
	for i := range transcriptPageLimit + 2 {
		before := tr.windowRev
		tr.offset = 0
		if !pageOnce(tr, history) {
			t.Fatalf("older page %d: nothing fetched", i)
		}
		if tr.windowRev == before {
			t.Fatalf("older page %d landed without announcing a window change", i)
		}
		revs[tr.windowRev] = true
		tr.render()
		assertIndexAgrees(t, tr, "after paging older")
	}
	if len(revs) < transcriptPageLimit {
		t.Fatalf("only %d distinct window revisions over %d pages", len(revs), transcriptPageLimit+2)
	}

	// G: back to the tail, a full rebuild.
	before := tr.windowRev
	tr.key('G')
	if tr.windowRev == before {
		t.Fatal("returning to the tail did not announce a window change")
	}
	assertIndexAgrees(t, tr, "after G")

	// Resize re-wraps every row; the index must still agree afterwards.
	tr.resize(70, 16)
	assertIndexAgrees(t, tr, "after resize")
}

// TestMergedFollowFrameLeavesTheWindowAlone is the other half of the same
// invariant: the single authority must stay SILENT when nothing changed, so D's
// per-frame resetToTail removal survives A's per-frame index rebuild. A live
// frame re-renders the open message but must not announce a window change.
func TestMergedFollowFrameLeavesTheWindowAlone(t *testing.T) {
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	applyTail(client, readBefore(transcriptHistory(40), recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 12), 50, 12, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()

	rev := tr.windowRev
	for i := range 20 {
		client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(41), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{ID: 0, Set: map[string]any{
			"type": "prose", "markdown": "streaming token " + itoa(i)}}}}}}}})
		tr.tick++
		tr.render()
	}
	if tr.windowRev != rev {
		t.Fatalf("live frames announced %d window changes", tr.windowRev-rev)
	}
	assertIndexAgrees(t, tr, "while following")

	// ... but a newly committed message must announce one.
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 41, Sealed: true,
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "committed"}},
	}}}})
	tr.render()
	if tr.windowRev == rev {
		t.Fatal("a committed message did not announce a window change")
	}
	assertIndexAgrees(t, tr, "after a commit")
}

// TestMergedGeometryMeasuresTheWindowNotTheRowCache pins the second invariant.
// D derived the page geometry from an average over t.rowCache; on the merged
// stack that set is wrong twice over, because D's own change lets rows outlive
// the window inside the payload LRU. The budget now reads A's line index, which
// counts the retained window exactly.
//
// The fixture makes the two answers differ on purpose: scroll back until the
// window holds tall messages while the LRU still holds short ones.
func TestMergedGeometryMeasuresTheWindowNotTheRowCache(t *testing.T) {
	history := mixedHeightHistory(300)
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(60, 16), 60, 16, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	tr.follow = false
	for range transcriptPageLimit + 2 {
		tr.offset = 0
		if !pageOnce(tr, history) {
			break
		}
		tr.render()
	}
	tr.key('G') // back to the tail: the window shrinks, the store keeps the rows
	tr.render()
	tr.follow = false

	// The truth, computed independently of heldWindow(): walk the window and
	// add up the rows the row cache holds for it, plus the rule separator
	// between messages.
	wantRows, wantMsgs := 0, 0
	tr.forEachMessage(func(m aria.Message) {
		rows, ok := tr.rowCache[keyOf(m)]
		if !ok {
			t.Fatalf("retained message %d has no cached rows", m.Turn)
		}
		if wantMsgs > 0 {
			wantRows += sepRows
		}
		wantRows += len(rows.rows)
		wantMsgs++
	})
	gotRows, gotMsgs := tr.heldWindow()
	if gotRows != wantRows || gotMsgs != wantMsgs {
		t.Fatalf("heldWindow() = %d rows over %d messages, retained window is %d over %d",
			gotRows, gotMsgs, wantRows, wantMsgs)
	}

	// And the old estimate really would have said something else, so this test
	// is not comparing a number against itself.
	cacheRows, cacheMsgs := 0, 0
	for _, c := range tr.rowCache {
		cacheRows += len(c.rows) + sepRows
		cacheMsgs++
	}
	if cacheMsgs <= wantMsgs {
		t.Fatalf("fixture failed: row cache (%d messages) is no bigger than the window (%d)",
			cacheMsgs, wantMsgs)
	}
	if cacheRows/cacheMsgs == gotRows/gotMsgs {
		t.Fatalf("fixture failed: row-cache average and window average agree (%d rows/msg)",
			gotRows/gotMsgs)
	}
	if got, want := tr.avgRowsPerMessage(), gotRows/gotMsgs; got != want {
		t.Fatalf("avgRowsPerMessage() = %d, index says %d", got, want)
	}
}

// TestMergedOpenMessageIsExcludedFromTheBudget pins the one deliberate
// difference from a naive "average the index" implementation, and the reason
// tuneTail needs TWO numbers where axis D had one.
//
// D drove the retune off len(t.lines()), which counts the live message. That
// conflates "how much committed history am I retaining" (what the row budget
// governs) with "does the viewport look empty" (what the screen shows). A long
// streaming reply then masks a window that is under budget and should grow: the
// pager opens on the floor of transcriptMinPageSize messages and stays there for
// the whole turn, because the open message alone is already over budget*3/4.
//
// A's index knows which entry is the open one, so the merged tuneTail asks the
// right question of each.
func TestMergedOpenMessageIsExcludedFromTheBudget(t *testing.T) {
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	applyTail(client, readBefore(tallHistory(120, 12), recentCursor, transcriptPageSize))

	// A reply long enough on its own to blow the whole row budget, streaming
	// before the pager even opens.
	huge := ""
	for l := range 500 {
		huge += "a very long line of streaming output number " + itoa(l) + "\n\n"
	}
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(121), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{ID: 0, Set: map[string]any{"type": "prose", "markdown": huge}}}}}}}})

	tr := newTranscript(ldrender.NewFakeTerminal(60, 16), 60, 16, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	for range 4 {
		tr.render()
	}

	held, msgs := tr.heldWindow()
	// The fixture is only meaningful if the open message ALONE would trip the
	// grow test's budget*3/4 threshold.
	if open := tr.index.total - held; open <= pageRowBudget()*3/4 {
		t.Fatalf("fixture failed: open message is only %d rows, need > %d",
			open, pageRowBudget()*3/4)
	}
	if msgs <= transcriptMinPageSize {
		t.Fatalf("the streaming reply masked the retune: window stuck at %d messages (the cold floor is %d)",
			msgs, transcriptMinPageSize)
	}
	if held < pageRowBudget()*3/4 {
		t.Fatalf("committed window is %d rows, budget is %d: the retune never grew it",
			held, pageRowBudget())
	}

	// And growing the live message further must not move the budget's inputs.
	before, beforeMsgs := held, msgs
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(121), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{ID: 0, Set: map[string]any{"type": "prose", "markdown": huge + huge}}}}}}}})
	tr.render()
	if gotRows, gotMsgs := tr.heldWindow(); gotRows != before || gotMsgs != beforeMsgs {
		t.Fatalf("a growing open message moved the budget: %d rows/%d msgs -> %d/%d",
			before, beforeMsgs, gotRows, gotMsgs)
	}
}
