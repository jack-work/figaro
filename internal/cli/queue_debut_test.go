package cli

// THE FLICKER THIS SUPPRESSES, stated as a law: a prompt sent into an idle
// figaro is queued and lifted again in the same breath, and a drawer that
// opens for those few milliseconds shows the reader nothing they can read.
// The bench (scripts/jitterbench.sh) measured the window at 8 to 66 ms.

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

func queuedRow(id uint64, text string) queueRow {
	return queueRow{ID: id, Text: text, State: rpc.QueueStateQueued}
}

func TestAnIdleAriasRowWaitsForItsDebut(t *testing.T) {
	debut := map[uint64]time.Time{}
	now := time.Now()
	rows := []queueRow{queuedRow(1, "hello")}

	items, next := queueDebut(rows, false, now, debut)
	if len(items) != 0 {
		t.Fatalf("an idle aria's fresh row was drawn at once: %+v", items)
	}
	if want := now.Add(queueDebutGrace); !next.Equal(want) {
		t.Fatalf("wake-up at %v, wanted %v", next, want)
	}

	// It is still there after the grace: it will really sit there, so it is
	// drawn, and nothing further is owed.
	items, next = queueDebut(rows, false, now.Add(queueDebutGrace), debut)
	if len(items) != 1 || items[0].id != 1 {
		t.Fatalf("the row that survived the grace was not drawn: %+v", items)
	}
	if !next.IsZero() {
		t.Fatalf("a drawn row still asked for a wake-up at %v", next)
	}
}

func TestALiftedRowIsNeverDrawnAtAll(t *testing.T) {
	debut := map[uint64]time.Time{}
	now := time.Now()
	if items, _ := queueDebut([]queueRow{queuedRow(1, "hello")}, false, now, debut); len(items) != 0 {
		t.Fatalf("drawn on arrival: %+v", items)
	}
	// The drain loop lifted it: committing is still live, and the row keeps
	// the deadline it was given rather than starting a new grace.
	committing := []queueRow{{ID: 1, Text: "hello", State: rpc.QueueStateCommitting}}
	if items, _ := queueDebut(committing, false, now.Add(10*time.Millisecond), debut); len(items) != 0 {
		t.Fatalf("committing restarted nothing but was drawn: %+v", items)
	}
	// Then it became a message. By the time the wake-up fires there is no live
	// row, so no frame is spent and the id is forgotten.
	gone := []queueRow{{ID: 1, Text: "hello", State: rpc.QueueStateCommitted}}
	items, next := queueDebut(gone, false, now.Add(queueDebutGrace), debut)
	if len(items) != 0 || !next.IsZero() {
		t.Fatalf("a departed row left something behind: %+v %v", items, next)
	}
	if len(debut) != 0 {
		t.Fatalf("the deadline of a departed row was kept: %+v", debut)
	}
}

func TestABusyAriasRowIsDrawnImmediately(t *testing.T) {
	debut := map[uint64]time.Time{}
	now := time.Now()
	items, next := queueDebut([]queueRow{queuedRow(1, "hello"), queuedRow(2, "again")}, true, now, debut)
	if len(items) != 2 {
		t.Fatalf("a working aria's queue was delayed: %+v", items)
	}
	if !next.IsZero() {
		t.Fatalf("nothing was deferred, yet a wake-up was asked for: %v", next)
	}
}

// A row that arrived against an idle aria keeps its grace even if the aria
// starts working inside it: promoting it would put the row on screen for the
// last few milliseconds of its life, which is the flicker again.
func TestTheDeadlineSurvivesTheAriaGettingBusy(t *testing.T) {
	debut := map[uint64]time.Time{}
	now := time.Now()
	rows := []queueRow{queuedRow(1, "hello")}
	queueDebut(rows, false, now, debut)
	if items, _ := queueDebut(rows, true, now.Add(20*time.Millisecond), debut); len(items) != 0 {
		t.Fatalf("the grace was cut short by the turn starting: %+v", items)
	}
}

// The second prompt of a burst is its own row with its own deadline, and the
// earliest one is what the caller is told to wake for.
func TestTheWakeUpIsTheEarliestDeadline(t *testing.T) {
	debut := map[uint64]time.Time{}
	now := time.Now()
	queueDebut([]queueRow{queuedRow(1, "one")}, false, now, debut)
	_, next := queueDebut([]queueRow{queuedRow(1, "one"), queuedRow(2, "two")},
		false, now.Add(30*time.Millisecond), debut)
	if want := now.Add(queueDebutGrace); !next.Equal(want) {
		t.Fatalf("wake-up at %v, wanted the older row's %v", next, want)
	}
}
