package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func setRetention(t *testing.T, b *XwalBackend, id, mode string) {
	t.Helper()
	raw, _ := json.Marshal(mode)
	if _, err := b.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{KeyRetentionMode: raw}, nil)); err != nil {
		t.Fatal(err)
	}
}

func newBoardForm(t *testing.T, b *XwalBackend) string {
	t.Helper()
	id, _, err := b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"seed": json.RawMessage(`0`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Retranslating an old turn reads the bound form's records at that turn, so a
// conversation-bound form refuses retention outright.
func TestAConversationBoundFormRefusesRetention(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := mustNewAriaTB(t, b)
	raw, _ := json.Marshal(RetentionMarks)
	_, err = b.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{KeyRetentionMode: raw}, nil))
	if !errors.Is(err, ErrRetentionOnConversation) {
		t.Fatalf("system.retention on a conversation-bound form: %v", err)
	}
}

// Under "marks" the sweep drops whole segments below the lowest live mark and
// nothing at or above it; the state is untouched and a read below the floor
// is refused rather than answered short.
func TestRetentionByMarksDropsOnlyBelowTheLowestMark(t *testing.T) {
	checkpointEvery(t, 64, 2)
	b, err := NewXwalBackend(t.TempDir(), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := newBoardForm(t, b)
	setRetention(t, b, id, RetentionMarks)
	patchBoard(t, b, id, 0, 400)
	settleCheckpoints(t, b, id)
	f, _ := b.form(id)
	want, head := f.Snapshot()
	low := LowestMark(MarksOf(want))
	if low == 0 {
		t.Fatal("no marks")
	}
	out, err := b.RetainForms(context.Background(), false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Dropped == 0 {
		t.Fatalf("nothing dropped below mark %d: %+v", low, out)
	}
	xw, _ := b.store.openNode(id)
	floor := xw.ChannelFloor(chanForm)
	xw.Close()
	if floor == 0 || floor >= low {
		t.Fatalf("floor %d against lowest mark %d", floor, low)
	}
	b.dropForm(id)
	f, _ = b.form(id)
	got, v := f.Snapshot()
	if v != head || snapJSON(t, got) != snapJSON(t, want) {
		t.Fatal("retention changed the state")
	}
	if _, err := f.PatchesBetween(0, head); !errors.Is(err, ErrBelowFloor) {
		t.Fatalf("a read from 0 under floor %d: %v", floor, err)
	}
	if _, err := f.StateAt(low); err != nil {
		t.Fatalf("the lowest mark %d no longer reads: %v", low, err)
	}
}

// "none" keeps every record even where the store-wide sweep would drop.
func TestRetentionNoneKeepsEverything(t *testing.T) {
	checkpointEvery(t, 64, 2)
	b, err := NewXwalBackend(t.TempDir(), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := newBoardForm(t, b)
	setRetention(t, b, id, RetentionNone)
	patchBoard(t, b, id, 0, 400)
	out, err := b.RetainForms(context.Background(), false, 0)
	if err != nil || len(out) != 0 {
		t.Fatalf("retention none dropped: %+v %v", out, err)
	}
}

// A fork tree four deep, written at every level, under retention by marks
// with room for two marks a form: the fork marks are evicted, and still no
// sweep drops a record a live fork needs. Every fork reads its birth state
// from its parent and its own head as before.
func TestAForkTreeFourDeepSurvivesRetentionByMarks(t *testing.T) {
	checkpointEvery(t, 64, 2)
	b, err := NewXwalBackend(t.TempDir(), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	root := newBoardForm(t, b)
	setRetention(t, b, root, RetentionMarks)
	patchBoard(t, b, root, 0, 150)
	kids := map[string]bornAt{}
	parent := root
	for depth := 1; depth <= 4; depth++ {
		child, born := bear(t, b, parent)
		kids[child] = born
		patchBoard(t, b, parent, 1000*depth, 150)
		patchBoard(t, b, child, 1000*depth+500, 150)
		parent = child
	}
	for id := range kids {
		settleCheckpoints(t, b, id)
	}
	settleCheckpoints(t, b, root)
	for id, k := range kids {
		head, err := b.FormState(id)
		if err != nil {
			t.Fatal(err)
		}
		k.head = snapJSON(t, head)
		kids[id] = k
	}
	dropped := 0
	for pass := 0; pass < 2; pass++ {
		out, err := b.RetainForms(context.Background(), false, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range out {
			dropped += r.Dropped
		}
		checkChildren(t, b, kids, fmt.Sprintf("after pass %d", pass))
	}
	if dropped == 0 {
		t.Fatal("the sweep dropped nothing; the tree proves nothing")
	}
	for id := range kids {
		b.dropForm(id)
	}
	b.dropForm(root)
	checkChildren(t, b, kids, "after a cold open")
}
