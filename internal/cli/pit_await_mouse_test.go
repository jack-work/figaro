package cli

import (
	"fmt"
	"testing"
)

// Keys typed while a pit opens wait for it (32caf3ba); the wheel and a click
// went round that queue, straight to the transcript underneath.

func awaitingPager(t *testing.T) (*interactiveInput, *livelogTurn, int, int) {
	t.Helper()
	in, lt := navInput(t, &countingWriter{}, true)
	lt.tr.command = func(string) {}
	in.mu.Lock()
	lt.tr.follow = false
	lt.tr.render()
	row := -1
	for r, ref := range lt.tr.frameRefs {
		if ref.valid() {
			row = r
			break
		}
	}
	in.mu.Unlock()
	if row < 0 {
		t.Fatal("fixture: nothing on screen to click")
	}
	in.consume([]byte("l"))
	in.mu.Lock()
	defer in.mu.Unlock()
	if !lt.tr.awaiting {
		t.Fatal("fixture: l is not waiting for its pit")
	}
	return in, lt, row, lt.tr.offset
}

func TestTheWheelWhileAPitOpensLeavesTheTranscriptAlone(t *testing.T) {
	for name, report := range map[string]string{"up": "\x1b[<64;10;5M", "down": "\x1b[<65;10;5M"} {
		in, lt, _, before := awaitingPager(t)
		in.consume([]byte(report))
		in.mu.Lock()
		off := lt.tr.offset
		in.mu.Unlock()
		if off != before {
			t.Errorf("wheel %s while the pit opened scrolled the transcript to %d", name, off)
		}
	}
}

func TestAClickWhileAPitOpensLeavesTheTranscriptAlone(t *testing.T) {
	in, lt, row, _ := awaitingPager(t)
	in.consume([]byte(fmt.Sprintf("\x1b[<0;10;%dM\x1b[<0;10;%dm", row+1, row+1)))
	in.mu.Lock()
	defer in.mu.Unlock()
	if lt.tr.selection.active {
		t.Fatal("a click while the pit opened selected a node in the transcript underneath")
	}
}

func TestTheWheelReachesThePitOnceItIsUp(t *testing.T) {
	in, lt, _, _ := awaitingPager(t)
	in.mu.Lock()
	lt.tr.setCmdOut("ls", awaitRows())
	before := lt.tr.offset
	in.mu.Unlock()
	in.consume([]byte("\x1b[<64;10;5M"))
	in.mu.Lock()
	defer in.mu.Unlock()
	if lt.tr.offset != before-3 {
		t.Fatalf("the wheel with the pit up moved the transcript %d -> %d, want a notch", before, lt.tr.offset)
	}
}
