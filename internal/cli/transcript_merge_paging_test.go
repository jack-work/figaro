package cli

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// windowRev, published by invalidateWindow, is the one authority on "the
// retained window changed", and the row budget reads the index's exact row
// counts, not an average over the row cache, which outlives the window.

// Tall older turns make the window and the row cache average differently.
func mixedHeightHistory(n int) []aria.TurnPart {
	out := make([]aria.TurnPart, n)
	for i := range out {
		md := "short-" + itoa(i+1)
		if i+1 <= 2*n/3 {
			md = ""
			for l := range 14 {
				md += "tall-" + itoa(i+1) + " line-" + itoa(l) + "\n\n"
			}
		}
		out[i] = aria.TurnPart{Turn: aria.Turn{ID: uint64(i + 1), Sealed: true, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: md}}}}
	}
	return out
}

// lineLTFromIndex is independent of rebuildLineLT's invalidation discipline.
func lineLTFromIndex(tr *transcript) []sliceKey {
	out := make([]sliceKey, 0, tr.index.total)
	for k := range tr.index.entries {
		e := &tr.index.entries[k]
		if e.sep {
			// Separator rows carry the following message's key.
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

// Every route that moves the window must publish through invalidateWindow.
func TestMergedPageMutationAlwaysReachesTheIndex(t *testing.T) {
	history := transcriptHistory(300)
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	applyTail(client, readBefore(history, recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 12), 50, 12, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	assertIndexAgrees(t, tr, "after enter")
	tr.follow = false

	revs := map[uint64]bool{tr.windowRev: true}
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

	before := tr.windowRev
	tr.key('G')
	if tr.windowRev == before {
		t.Fatal("returning to the tail did not announce a window change")
	}
	assertIndexAgrees(t, tr, "after G")

	tr.resize(70, 16)
	assertIndexAgrees(t, tr, "after resize")
}

// A live frame re-renders the open message but must not announce a window change.
func TestMergedFollowFrameLeavesTheWindowAlone(t *testing.T) {
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	applyTail(client, readBefore(transcriptHistory(40), recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(50, 12), 50, 12, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()

	rev := tr.windowRev
	for i := range 20 {
		client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(41), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{ID: 0, Set: map[string]any{
			"type": "prose", "markdown": "streaming token " + itoa(i)}}}}}}}}, aria.Notify)
		tr.tick++
		tr.render()
	}
	if tr.windowRev != rev {
		t.Fatalf("live frames announced %d window changes", tr.windowRev-rev)
	}
	assertIndexAgrees(t, tr, "while following")

	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 41, Sealed: true,
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "committed"}},
	}}}}, aria.Notify)
	tr.render()
	if tr.windowRev == rev {
		t.Fatal("a committed message did not announce a window change")
	}
	assertIndexAgrees(t, tr, "after a commit")
}

// Rows outlive the window inside the payload LRU, so the budget must read
// the line index; the fixture makes the two answers differ.
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
	tr.key('G')
	tr.render()
	tr.follow = false

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

	// The row-cache average must really differ, or this compares a number to
	// itself.
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

// The open message is excluded from the budget: a long streaming reply must
// not mask a committed window that is under budget and should grow.
func TestMergedOpenMessageIsExcludedFromTheBudget(t *testing.T) {
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	applyTail(client, readBefore(tallHistory(120, 12), recentCursor, transcriptPageSize))

	huge := ""
	for l := range 500 {
		huge += "a very long line of streaming output number " + itoa(l) + "\n\n"
	}
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(121), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{ID: 0, Set: map[string]any{"type": "prose", "markdown": huge}}}}}}}}, aria.Notify)

	tr := newTranscript(ldrender.NewFakeTerminal(60, 16), 60, 16, ldrender.NodeText{}, client, "", time.Time{})
	tr.enter()
	for range 4 {
		tr.render()
	}

	held, msgs := tr.heldWindow()
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

	before, beforeMsgs := held, msgs
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: uint64(121), Live: &aria.Live{From: 0, V: 0, Nodes: []aria.NodeDelta{{ID: 0, Set: map[string]any{"type": "prose", "markdown": huge + huge}}}}}}}}, aria.Notify)
	tr.render()
	if gotRows, gotMsgs := tr.heldWindow(); gotRows != before || gotMsgs != beforeMsgs {
		t.Fatalf("a growing open message moved the budget: %d rows/%d msgs -> %d/%d",
			before, beforeMsgs, gotRows, gotMsgs)
	}
}
