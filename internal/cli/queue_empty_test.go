package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// AN EMPTY QUEUE IS A SENTENCE, NOT A PIT.
//
// These assert the bar, which is the one thing the frozen keymap oracles
// cannot see: their signature has no column for the notice, so `Q` on an
// empty queue reads as inert there and is measured here instead.

// queuePager is a pager with a status bar, which is where the notice lands.
func queuePager(t *testing.T, queued ...queuedItem) *transcript {
	t.Helper()
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Inquiry: "a question", Sealed: true,
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "an answer"}},
	}}}}, aria.Notify)
	tr := newTranscript(ldrender.NewFakeTerminal(72, 20), 72, 20,
		&ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
	tr.setStatus(newSessionStatus("aria1234", time.Unix(0, 0)))
	tr.enter()
	tr.follow = false
	tr.queued = queued
	return tr
}

func TestQueuePit_EmptyQueueIsANoticeNotAPit(t *testing.T) {
	tr := queuePager(t)
	tr.key('Q')
	if tr.showing(pitQueue) {
		t.Fatal("an empty queue opened a pit with nothing in it")
	}
	if tr.pit.open() {
		t.Fatalf("Q opened %q", tr.pit.id)
	}
	if got := tr.status.noticeText(); got != queueEmptyNote {
		t.Fatalf("the bar says %q, want %q", got, queueEmptyNote)
	}
	// It costs the conversation nothing: no drawer, so no rows.
	if rows := tr.pit.lines(tr.w, tr.pitRoom()); len(rows) != 0 {
		t.Fatalf("the refusal drew %d rows:\n%s", len(rows), strings.Join(rows, "\n"))
	}
}

// A queue with something in it still opens, which is the half that must not
// break: without this the test above passes on a pit that never opens at all.
func TestQueuePit_AQueueWithSomethingInItOpens(t *testing.T) {
	tr := queuePager(t, queuedItem{id: 1, text: "a queued prompt"})
	tr.key('Q')
	if !tr.showing(pitQueue) {
		t.Fatal("a queue with one message did not open its pit")
	}
	if got := tr.status.noticeText(); got == queueEmptyNote {
		t.Fatal("a non-empty queue reported itself empty")
	}
	rows := tr.pit.lines(tr.w, tr.pitRoom())
	if len(rows) == 0 || !strings.Contains(strings.Join(rows, "\n"), "a queued prompt") {
		t.Fatalf("the pit does not show the queue:\n%s", strings.Join(rows, "\n"))
	}
}

// A DRAINED QUEUE CLOSES ITS PIT and says why, whether the reader opened it by
// hand or the queue opened it. The old rule refreshed the rows instead, which
// for a queue means an empty list: a pit left open over the conversation,
// showing nothing.
func TestQueuePit_DrainingClosesItAndSaysSo(t *testing.T) {
	for _, byHand := range []bool{true, false} {
		name := "auto-opened"
		if byHand {
			name = "opened by hand"
		}
		t.Run(name, func(t *testing.T) {
			tr := queuePager(t, queuedItem{id: 1, text: "a queued prompt"})
			if byHand {
				tr.key('Q')
			} else {
				tr.showQueuedAuto(true)
			}
			if !tr.showing(pitQueue) {
				t.Fatal("the fixture opened no queue pit")
			}
			// The queue drains: the snapshot empties, then the fold fires.
			tr.queued = nil
			tr.showQueuedAuto(false)
			if tr.showing(pitQueue) {
				t.Fatal("a drained queue left its pit open")
			}
			if got := tr.status.noticeText(); got != queueEmptyNote {
				t.Fatalf("the bar says %q, want %q", got, queueEmptyNote)
			}
		})
	}
}

// An UNREADABLE queue is not an empty one. Reporting a failure as emptiness is
// the worst of the three answers, so queuedErr is part of the test.
func TestQueuePit_AnUnreadableQueueIsNotEmpty(t *testing.T) {
	tr := queuePager(t)
	tr.queuedErr = "queue: dial angelus: connection refused"
	tr.key('Q')
	if got := tr.status.noticeText(); got == queueEmptyNote {
		t.Fatal("a queue that could not be read reported itself empty")
	}
	if !tr.showing(pitQueue) {
		t.Fatal("an unreadable queue must still open the pit that reports it")
	}
}

// Asking for the queue revokes an earlier dismissal, so a queue that arrives
// after the refusal opens the panel by itself rather than being suppressed by
// a no the reader has since taken back.
func TestQueuePit_AskingRevokesTheDismissal(t *testing.T) {
	tr := queuePager(t, queuedItem{id: 1, text: "a queued prompt"})
	tr.key('Q')
	tr.key(0x1b) // Esc: the deliberate dismissal
	if !tr.queueDismissed {
		t.Fatal("Esc did not latch the dismissal")
	}
	tr.queued = nil
	tr.key('Q') // asking again, on an empty queue
	if tr.queueDismissed {
		t.Fatal("asking for the queue left the dismissal latched")
	}
	tr.queued = []queuedItem{{id: 2, text: "a later prompt"}}
	tr.showQueuedAuto(true)
	if !tr.showing(pitQueue) {
		t.Fatal("a later queue did not open the pit")
	}
}
