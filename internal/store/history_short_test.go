package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func setHistoryShort(t *testing.T, b *XwalBackend, id string) {
	t.Helper()
	raw, _ := json.Marshal(HistoryShort)
	if _, err := b.ApplyFormPrivileged(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{KeyHistory: raw}, nil)); err != nil {
		t.Fatal(err)
	}
}

func TestShortHistoryAloneCheckpointsAndDropsBelowTheOlderMark(t *testing.T) {
	checkpointEvery(t, 0, 5)
	b, err := NewXwalBackend(t.TempDir(), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := newBoardForm(t, b)
	setHistoryShort(t, b, id)
	patchBoard(t, b, id, 0, 400)
	settleCheckpoints(t, b, id)
	f, _ := b.form(id)
	want, head := f.Snapshot()
	low := LowestMark(MarksOf(want))
	if n := len(MarksOf(want)); n == 0 || n > minCheckpointMarks {
		t.Fatalf("a short-history form keeps %d marks, want 1 to %d", n, minCheckpointMarks)
	}
	out, err := b.RetainForms(context.Background(), false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Dropped == 0 {
		t.Fatalf("nothing dropped: %+v", out)
	}
	b.dropForm(id)
	f, _ = b.form(id)
	got, v := f.Snapshot()
	if v != head || snapJSON(t, got) != snapJSON(t, want) {
		t.Fatal("dropping history changed the state")
	}
	if _, err := f.PatchesBetween(0, head); !errors.Is(err, ErrBelowFloor) {
		t.Fatalf("a read from 0: %v", err)
	}
	if _, err := f.StateAt(low); err != nil {
		t.Fatalf("the older mark %d no longer reads: %v", low, err)
	}
}

func TestAFormWithoutShortHistoryTakesTheStoreCheckpointDefaults(t *testing.T) {
	checkpointEvery(t, 0, 5)
	b, err := NewXwalBackend(t.TempDir(), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := newBoardForm(t, b)
	patchBoard(t, b, id, 0, 400)
	settleCheckpoints(t, b, id)
	f, _ := b.form(id)
	snap, _ := f.Snapshot()
	if n := len(MarksOf(snap)); n != 0 {
		t.Fatalf("checkpoints off in the store, yet %d marks", n)
	}
}

func TestAnExplicitRetentionNoneBeatsShortHistory(t *testing.T) {
	checkpointEvery(t, 0, 5)
	b, err := NewXwalBackend(t.TempDir(), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := newBoardForm(t, b)
	setHistoryShort(t, b, id)
	setRetention(t, b, id, RetentionNone)
	patchBoard(t, b, id, 0, 400)
	out, err := b.RetainForms(context.Background(), false, 0)
	if err != nil || len(out) != 0 {
		t.Fatalf("dropped under retention none: %+v %v", out, err)
	}
}

func TestAConversationBoundFormRefusesShortHistory(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := mustNewAriaTB(t, b)
	raw, _ := json.Marshal(HistoryShort)
	_, err = b.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{KeyHistory: raw}, nil))
	if !errors.Is(err, ErrRetentionOnConversation) {
		t.Fatalf("system.history on a conversation-bound form: %v", err)
	}
}

func TestRetainFormsCountedCountsEveryFormItVisitedNotOnlyTheOnesItTrimmed(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	newBoardForm(t, b)
	newBoardForm(t, b)
	out, scanned, err := b.RetainFormsCounted(context.Background(), false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if scanned != 2 || len(out) != 0 {
		t.Fatalf("scanned %d, trimmed %d, want 2 and 0", scanned, len(out))
	}
}
