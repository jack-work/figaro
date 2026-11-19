package cli

import (
	"context"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

// Issue #126: with a pit open but receded, mode() still answered panel, so the
// input layer's transcript binds (X) never fired. Transcript binds follow the
// keyboard's focus, not the pit's presence. Bytes go through the decoder.

type hangupRecorder chan rpc.QueueDisposition

func (h hangupRecorder) Hangup(_ context.Context, d rpc.QueueDisposition) (*rpc.InterruptResponse, error) {
	h <- d
	return &rpc.InterruptResponse{Stopped: true}, nil
}

func TestModeFollowsFocusOverAnOpenPit(t *testing.T) {
	r := newPassiveRig(t)
	r.queueArrives()
	r.in.mu.Lock()
	receded := r.tr.mode()
	r.in.mu.Unlock()
	r.press("T")
	r.in.mu.Lock()
	focused := r.tr.mode()
	r.in.mu.Unlock()
	if receded != modeTranscript || focused == modeTranscript {
		t.Fatalf("mode with the queue receded %v, focused %v; want transcript, then the pit's", receded, focused)
	}
}

func TestXHangsUpAndDropsOverARecededPit(t *testing.T) {
	r := newPassiveRig(t)
	calls := make(hangupRecorder, 1)
	r.in.hangup = calls
	r.queueArrives()
	r.press("X")
	select {
	case d := <-calls:
		if d != rpc.QueueClear {
			t.Fatalf("X hung up with %v, want the queue cleared", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("X over a receded queue never hung up")
	}
}

func TestQuestionTravelOverARecededPit(t *testing.T) {
	for _, key := range []string{"\x1bn", "\x1bp"} {
		t.Run(key[1:], func(t *testing.T) {
			r := newPassiveRig(t)
			r.queueArrives()
			if key == "\x1bp" {
				r.in.mu.Lock()
				pagerTail(r.tr)
				r.in.mu.Unlock()
			}
			at := r.state()
			feed(t, r.in, key)
			now := r.state()
			if now.offset == at.offset || now.pitRow != at.pitRow || now.open != pitQueue {
				t.Fatalf("M-%s left offset %d->%d, queue row %q->%q, open %q", key[1:], at.offset, now.offset, at.pitRow, now.pitRow, now.open)
			}
		})
	}
}

func TestAFocusedPitKeepsItsBinds(t *testing.T) {
	r := newPassiveRig(t)
	calls := make(hangupRecorder, 1)
	r.in.hangup = calls
	var dropped []string
	r.in.mu.Lock()
	r.tr.dropRow = func(pit, id, next string) { dropped = append(dropped, pit+":"+id) }
	r.in.mu.Unlock()
	r.queueArrives()
	r.press("T")
	at := r.state()
	r.press("j")
	if now := r.state(); now.pitRow == at.pitRow || now.offset != at.offset {
		t.Fatalf("j in the focused queue moved row %q->%q, offset %d->%d", at.pitRow, now.pitRow, at.offset, now.offset)
	}
	r.press("x")
	if len(dropped) == 0 {
		t.Fatal("x in the focused queue dropped nothing")
	}
	r.press("X")
	select {
	case <-calls:
		t.Fatal("X in the focused queue hung up")
	case <-time.After(200 * time.Millisecond):
	}
}
