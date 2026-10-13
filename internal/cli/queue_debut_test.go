package cli

// A prompt sent into an idle figaro is queued and lifted within milliseconds;
// the grace keeps the drawer from opening for that.

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

	// Still there after the grace: drawn, nothing further owed.
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
	// Committing is still live and keeps its deadline.
	committing := []queueRow{{ID: 1, Text: "hello", State: rpc.QueueStateCommitting}}
	if items, _ := queueDebut(committing, false, now.Add(10*time.Millisecond), debut); len(items) != 0 {
		t.Fatalf("committing restarted nothing but was drawn: %+v", items)
	}
	// Committed: no live row, no frame, the id is forgotten.
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

// A row keeps its grace even if the aria gets busy inside it.
func TestTheDeadlineSurvivesTheAriaGettingBusy(t *testing.T) {
	debut := map[uint64]time.Time{}
	now := time.Now()
	rows := []queueRow{queuedRow(1, "hello")}
	queueDebut(rows, false, now, debut)
	if items, _ := queueDebut(rows, true, now.Add(20*time.Millisecond), debut); len(items) != 0 {
		t.Fatalf("the grace was cut short by the turn starting: %+v", items)
	}
}

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
