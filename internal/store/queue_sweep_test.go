package store

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
)

func queueEntry(state string, at time.Time) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"state": state, "state_at": at.UTC().Format(time.RFC3339)})
	return raw
}

func queueBoard(t *testing.T, b *XwalBackend, now time.Time, short bool) string {
	t.Helper()
	id := newBoardForm(t, b)
	if short {
		setHistoryShort(t, b, id)
	}
	old, recent := now.Add(-8*24*time.Hour), now.Add(-24*time.Hour)
	set := map[string]json.RawMessage{
		"queue.old-done":      queueEntry("done", old),
		"queue.old-cancelled": queueEntry("cancelled", old),
		"queue.recent-done":   queueEntry("done", recent),
		"queue.old-posted":    queueEntry("posted", old),
		"queue.old-claimed":   queueEntry("claimed", old),
	}
	if _, err := b.ApplyForm(id, form.Build(form.Snapshot{}, set, nil)); err != nil {
		t.Fatal(err)
	}
	return id
}

func queueIDs(t *testing.T, b *XwalBackend, id string) map[string]bool {
	t.Helper()
	snap, err := b.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := snap.Get("queue")
	var entries map[string]json.RawMessage
	_ = json.Unmarshal(raw, &entries)
	out := map[string]bool{}
	for k := range entries {
		out[k] = true
	}
	return out
}

func TestTheSweepDeletesOnlyFinishedEntriesOlderThanTheGraceOnShortHistoryForms(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	now := time.Now()
	short := queueBoard(t, b, now, true)
	plain := queueBoard(t, b, now, false)
	n, err := b.SweepQueueEntries(now, 7*24*time.Hour)
	if err != nil || n != 2 {
		t.Fatalf("removed %d, %v", n, err)
	}
	got := queueIDs(t, b, short)
	want := map[string]bool{"recent-done": true, "old-posted": true, "old-claimed": true}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("short-history queue after the sweep: %v", got)
	}
	if len(queueIDs(t, b, plain)) != 5 {
		t.Fatalf("a form without system.history lost entries: %v", queueIDs(t, b, plain))
	}
}

func TestAZeroGraceSweepsNothing(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	now := time.Now()
	id := queueBoard(t, b, now, true)
	if n, err := b.SweepQueueEntries(now, 0); err != nil || n != 0 || len(queueIDs(t, b, id)) != 5 {
		t.Fatalf("zero grace swept %d: %v", n, err)
	}
}
