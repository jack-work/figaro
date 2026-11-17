package cli

import (
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

// Gluck, 11-16: the queue pit opens itself when a prompt queues behind a
// running turn. It used to take the keyboard, so j/k and ^N/^P stopped moving
// the transcript he was reading. A passive pit leaves the focus where it was;
// only T (or opening it by its own key) hands it over. Bytes go through the
// input decoder, one per read.

type passiveRig struct {
	t  *testing.T
	in *interactiveInput
	tr *transcript
}

func newPassiveRig(t *testing.T) *passiveRig {
	in, lt := navInput(t, &countingWriter{}, true)
	r := &passiveRig{t: t, in: in, tr: lt.tr}
	in.mu.Lock()
	pagerTop(r.tr)
	r.tr.queued = []queuedItem{{id: 1, text: "first queued"}, {id: 2, text: "second queued"}}
	in.mu.Unlock()
	return r
}

func (r *passiveRig) press(keys string) {
	r.t.Helper()
	for i := 0; i < len(keys); i++ {
		feed(r.t, r.in, keys[i:i+1])
	}
}

type passiveState struct {
	offset   int
	pitRow   string
	selected bool
	focused  pitFocus
	open     pitID
}

func (r *passiveRig) state() passiveState {
	r.in.mu.Lock()
	defer r.in.mu.Unlock()
	row, _ := r.tr.pit.selected()
	return passiveState{r.tr.offset, row.id, r.tr.selection.active, r.tr.focused, r.tr.openPit()}
}

func (r *passiveRig) queueArrives() {
	r.in.mu.Lock()
	r.tr.showQueuedAuto(true)
	r.in.mu.Unlock()
}

func TestAQueueThatOpensItselfLeavesTheTranscriptFocused(t *testing.T) {
	r := newPassiveRig(t)
	r.queueArrives()
	at := r.state()
	if at.open != pitQueue || at.focused != focusTranscript {
		t.Fatalf("the queue arrived as %q with focus %v, want it open and the transcript focused", at.open, at.focused)
	}
	r.press("jj")
	moved := r.state()
	if moved.offset <= at.offset || moved.pitRow != at.pitRow {
		t.Fatalf("j moved offset %d->%d and the queue row %q->%q; want the transcript to scroll",
			at.offset, moved.offset, at.pitRow, moved.pitRow)
	}
	r.press("k")
	if back := r.state(); back.offset >= moved.offset || back.pitRow != at.pitRow {
		t.Fatalf("k left offset %d (was %d) and the queue row on %q", back.offset, moved.offset, back.pitRow)
	}
	r.press("\x0e")
	if sel := r.state(); !sel.selected || sel.pitRow != at.pitRow {
		t.Fatalf("^N selected a node %v and moved the queue row to %q; want the transcript's node selection", sel.selected, sel.pitRow)
	}
	r.press("\x10")
	if sel := r.state(); sel.pitRow != at.pitRow {
		t.Fatalf("^P moved the queue row to %q", sel.pitRow)
	}
	r.press(">")
	if open := r.state().open; open != pitComposer {
		t.Fatalf("'>' with the queue up opened %q, want the composer", open)
	}
}

func TestTHandsAPassivePitTheKeyboard(t *testing.T) {
	r := newPassiveRig(t)
	r.queueArrives()
	r.press("T")
	at := r.state()
	if at.focused != focusPit {
		t.Fatalf("T left focus %v", at.focused)
	}
	r.press("j")
	if moved := r.state(); moved.pitRow == at.pitRow || moved.offset != at.offset {
		t.Fatalf("j in the focused queue moved the row %q->%q and the offset %d->%d", at.pitRow, moved.pitRow, at.offset, moved.offset)
	}
	r.press("T")
	if f := r.state().focused; f != focusTranscript {
		t.Fatalf("a second T left focus %v", f)
	}
}

func TestOpeningTheQueueByItsKeyFocusesIt(t *testing.T) {
	r := newPassiveRig(t)
	r.press("Q")
	if s := r.state(); s.open != pitQueue || s.focused != focusPit {
		t.Fatalf("Q opened %q with focus %v, want the queue focused", s.open, s.focused)
	}
}

// Review of batch 1: with the queue receded, its binds still answered first, so
// x (the transcript's delete-comment) deleted a queued prompt with no question
// and no copy, and Enter opened a queue row. Keys belong to the focused side;
// only Esc still reaches the pit.
func TestARecededQueueAnswersNoneOfItsOwnKeys(t *testing.T) {
	for _, key := range []string{"x", "X", "\r", "a", "l", "h"} {
		t.Run(fmt.Sprintf("%q", key), func(t *testing.T) {
			r := newPassiveRig(t)
			var dropped []string
			r.in.mu.Lock()
			r.tr.dropRow = func(pit, id, next string) { dropped = append(dropped, pit+":"+id) }
			r.in.mu.Unlock()
			r.queueArrives()
			at := r.state()
			r.press(key)
			r.in.mu.Lock()
			opened := len(r.tr.queueOpen)
			queue := r.tr.showing(pitQueue)
			r.in.mu.Unlock()
			if len(dropped) != 0 {
				t.Fatalf("%q with the transcript focused dropped %v", key, dropped)
			}
			if opened != 0 {
				t.Fatalf("%q with the transcript focused opened a queue row", key)
			}
			if now := r.state(); queue && now.pitRow != at.pitRow {
				t.Fatalf("%q moved the receded queue's row %q->%q", key, at.pitRow, now.pitRow)
			}
		})
	}
}

func TestEscStillClosesARecededQueue(t *testing.T) {
	r := newPassiveRig(t)
	r.queueArrives()
	r.press("\x1b")
	if open := r.state().open; open == pitQueue {
		t.Fatal("Esc left the receded queue open")
	}
}

func TestQOnARecededQueueHandsItTheKeyboard(t *testing.T) {
	r := newPassiveRig(t)
	r.queueArrives()
	r.press("Q")
	if s := r.state(); s.open != pitQueue || s.focused != focusPit {
		t.Fatalf("Q over the receded queue left %q with focus %v, want the queue focused", s.open, s.focused)
	}
}

// Review of batch 1: an interrupt replaces the receded queue with the dropped
// pit, the only copy of the discarded text, and it inherited the transcript's
// focus, so j/k and y never reached it.
func TestTheDroppedPitTakesTheKeyboardFromARecededQueue(t *testing.T) {
	r := newPassiveRig(t)
	r.queueArrives()
	r.in.mu.Lock()
	r.in.lt.showDropped([]rpc.QueuedPrompt{{ID: 1, Text: "first queued"}, {ID: 2, Text: "second queued"}})
	r.in.mu.Unlock()
	if s := r.state(); s.open != pitDropped || s.focused != focusPit {
		t.Fatalf("the dropped pit opened as %q with focus %v, want it focused", s.open, s.focused)
	}
}
