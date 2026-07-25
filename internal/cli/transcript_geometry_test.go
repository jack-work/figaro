package cli

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// tallHistory builds committed messages whose rendered height is large, so the
// retained window is bounded by rows rather than by message count.
func tallHistory(n, lines int) []aria.TurnPart {
	out := make([]aria.TurnPart, n)
	for i := range out {
		md := ""
		for l := range lines {
			md += "message-" + itoa(i+1) + " line-" + itoa(l) + "\n\n"
		}
		out[i] = aria.TurnPart{Turn: aria.Turn{ID: uint64(i + 1), Sealed: true, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: md}}}}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func newTallTranscript(t *testing.T, budget int) *transcript {
	t.Helper()
	prev := transcriptWindowRows
	transcriptWindowRows = budget
	t.Cleanup(func() { transcriptWindowRows = prev })
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	client.Apply(readBefore(tallHistory(120, 12), recentCursor, transcriptPageSize))
	tr := newTranscript(ldrender.NewFakeTerminal(60, 20), 60, 20, ldrender.NodeText{}, client, "aria", time.Time{})
	tr.enter()
	return tr
}

// TestTranscriptRowBudgetShowsIdenticalFrame pins the correctness half of the
// rows-based geometry: bounding the retained window changes what is HELD, never
// what is SHOWN. The visible rows at the live tail must be byte-identical to
// those of a pager holding the whole 30-message page.
func TestTranscriptRowBudgetShowsIdenticalFrame(t *testing.T) {
	small := newTallTranscript(t, 600)
	big := newTallTranscript(t, 1_000_000)

	smallRows, bigRows := small.lines(), big.lines()
	body := small.h - 3
	if len(smallRows) < body || len(bigRows) < body {
		t.Fatalf("windows too short: %d / %d rows", len(smallRows), len(bigRows))
	}
	got := smallRows[len(smallRows)-body:]
	want := bigRows[len(bigRows)-body:]
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("visible row %d differs:\n small: %q\n   big: %q", i, got[i], want[i])
		}
	}
	if len(smallRows) >= len(bigRows) {
		t.Fatalf("row budget did not shrink the window: %d vs %d rows", len(smallRows), len(bigRows))
	}
}

// TestTranscriptRowBudgetBoundsTheWindow pins that the window converges to the
// budget within one message, and that a light aria (where the budget is never
// reached) keeps the full message-count page — i.e. the old geometry.
func TestTranscriptRowBudgetBoundsTheWindow(t *testing.T) {
	tr := newTallTranscript(t, 600)
	tr.render()
	rows := len(tr.lines())
	if rows > transcriptWindowRows {
		t.Fatalf("retained window is %d rows, budget is %d", rows, transcriptWindowRows)
	}
	if rows < tr.h-3 {
		t.Fatalf("window of %d rows does not fill the %d-row viewport", rows, tr.h-3)
	}
	if n := len(tr.messages()); n < transcriptMinPageSize {
		t.Fatalf("window shrank below the floor: %d messages", n)
	}

	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	client.Apply(readBefore(transcriptHistory(120), recentCursor, transcriptPageSize))
	light := newTranscript(ldrender.NewFakeTerminal(60, 20), 60, 20, ldrender.NodeText{}, client, "aria", time.Time{})
	transcriptWindowRows = 1200 // the shipped budget; restored by the Cleanup above
	light.enter()
	light.render()
	if got := len(light.messages()); got != transcriptPageSize {
		t.Fatalf("light aria retained %d messages, want the full page of %d", got, transcriptPageSize)
	}
	if got := light.pageMessages(); got != transcriptPageSize {
		t.Fatalf("light aria page size = %d, want %d", got, transcriptPageSize)
	}
}

// TestTranscriptColdStartRendersOnePage pins the cold-start policy: opening the
// pager on an aria of very tall messages must not render thirty of them to fill
// one screen. The window starts at the floor and grows into the row budget.
func TestTranscriptColdStartRendersOnePage(t *testing.T) {
	prev := transcriptWindowRows
	transcriptWindowRows = 1200
	t.Cleanup(func() { transcriptWindowRows = prev })
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	client.Apply(readBefore(tallHistory(120, 40), recentCursor, transcriptPageSize))
	view := &countingNodeView{}
	tr := newTranscript(ldrender.NewFakeTerminal(60, 20), 60, 20, view, client, "aria", time.Time{})
	tr.enter()
	if view.calls > transcriptPageSize/2 {
		t.Fatalf("cold enter rendered %d nodes; the whole 30-message page is %d",
			view.calls, transcriptPageSize)
	}
	if rows := len(tr.lines()); rows < tr.h-3 {
		t.Fatalf("cold window of %d rows does not fill the %d-row viewport", rows, tr.h-3)
	}
}

type countingNodeView struct {
	inner ldrender.NodeText
	calls int
}

func (v *countingNodeView) Render(n livedoc.Node, width, tick int) []string {
	v.calls++
	return v.inner.Render(n, width, tick)
}

// TestTranscriptRetuneIsIdempotent pins that the one-shot retune settles: it
// must not keep re-cutting (and thus re-rendering) the window frame after frame.
func TestTranscriptRetuneIsIdempotent(t *testing.T) {
	tr := newTallTranscript(t, 600)
	tr.render()
	rev, n := tr.tailRev, len(tr.messages())
	for range 10 {
		tr.render()
	}
	if tr.tailRev != rev || len(tr.messages()) != n {
		t.Fatalf("retune kept firing: rev %d -> %d, messages %d -> %d",
			rev, tr.tailRev, n, len(tr.messages()))
	}
}
