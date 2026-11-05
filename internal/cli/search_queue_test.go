package cli

import (
	"strings"
	"testing"
)

// Gluck, 2026-11-05: searching with a prompt in the queue went wrong. The queue
// pit opens by itself when the queue fills; '/' then aimed at its rows, so a
// search for words in the conversation answered "no row matching" and n/N
// walked the queue. A queue the user did not open with Q is not a search target.
func TestASearchWithAnAutoShownQueueSearchesTheConversation(t *testing.T) {
	tr := queuePager(t, queuedItem{id: 1, text: "a queued prompt"})
	tr.showQueuedAuto(true)
	if !tr.showing(pitQueue) {
		t.Fatal("the queue pit did not open by itself; the fixture cannot show the bug")
	}
	for _, b := range []byte("/answer") {
		tr.key(b)
	}
	if strings.HasPrefix(tr.searchTarget(), "𝄚") || tr.searchTarget() != "" {
		t.Fatalf("the search line aims at %q, want the conversation", tr.searchTarget())
	}
	tr.key('\r')
	if note := tr.status.noticeText(); strings.Contains(note, "no row matching") {
		t.Fatalf("the search walked the queue: %q", note)
	}
	if tr.matchQuery != "answer" {
		t.Fatalf("matchQuery %q", tr.matchQuery)
	}
	if !tr.showing(pitQueue) {
		t.Fatal("searching the conversation closed the queue")
	}
	tr.key('n')
	if note := tr.status.noticeText(); strings.Contains(note, "no row matching") {
		t.Fatalf("n walked the queue: %q", note)
	}
	if !tr.visual.on {
		t.Fatal("n did not walk the conversation's matches")
	}
}

// Without this the test above passes on a pager that never searches a list: a
// queue opened by hand with Q is still searched by '/'.
func TestASearchWithAQueueOpenedByQSearchesItsRows(t *testing.T) {
	tr := queuePager(t, queuedItem{id: 1, text: "a queued prompt"})
	tr.key('Q')
	if !tr.showing(pitQueue) || !tr.queuedByKey {
		t.Fatal("Q did not open the queue; the fixture cannot show the control")
	}
	for _, b := range []byte("/answer") {
		tr.key(b)
	}
	if !strings.HasPrefix(tr.searchTarget(), "𝄚") {
		t.Fatalf("the search line aims at %q, want the queue", tr.searchTarget())
	}
	tr.key('\r')
	if note := tr.status.noticeText(); !strings.Contains(note, "no row matching answer") {
		t.Fatalf("a search of the queue for a word not in it said %q", note)
	}
}

// In the same session the bar went on saying "message queue is empty" for ten
// seconds after a new prompt filled the queue, beside the pit that showed it.
func TestAQueueThatRefillsWithdrawsItsEmptyNotice(t *testing.T) {
	tr := queuePager(t, queuedItem{id: 1, text: "a queued prompt"})
	tr.showQueuedAuto(true)
	tr.queued = nil
	tr.showQueuedAuto(false)
	if got := tr.status.noticeText(); got != queueEmptyNote {
		t.Fatalf("a drained queue said %q, want %q; the fixture cannot show the bug", got, queueEmptyNote)
	}
	tr.queued = []queuedItem{{id: 2, text: "another queued prompt"}}
	tr.showQueuedAuto(true)
	if got := tr.status.noticeText(); got == queueEmptyNote {
		t.Fatalf("the bar still says %q beside a queue with a prompt in it", got)
	}
}
