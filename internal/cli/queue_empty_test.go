package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// An empty queue is a sentence on the bar, not a pit; the keymap oracles have
// no column for the notice, so it is measured here.
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
	if rows := tr.pit.lines(tr.w, tr.pitRoom()); len(rows) != 0 {
		t.Fatalf("the refusal drew %d rows:\n%s", len(rows), strings.Join(rows, "\n"))
	}
}

// Without this the test above passes on a pit that never opens at all.
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

// A drained queue closes its pit and says why, however it was opened.
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

// An unreadable queue is not an empty one.
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

// Asking for the queue revokes an earlier dismissal.
func TestQueuePit_AskingRevokesTheDismissal(t *testing.T) {
	tr := queuePager(t, queuedItem{id: 1, text: "a queued prompt"})
	tr.key('Q')
	tr.key(0x1b)
	if !tr.queueDismissed {
		t.Fatal("Esc did not latch the dismissal")
	}
	tr.queued = nil
	tr.key('Q')
	if tr.queueDismissed {
		t.Fatal("asking for the queue left the dismissal latched")
	}
	tr.queued = []queuedItem{{id: 2, text: "a later prompt"}}
	tr.showQueuedAuto(true)
	if !tr.showing(pitQueue) {
		t.Fatal("a later queue did not open the pit")
	}
}
