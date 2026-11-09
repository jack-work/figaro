package cli

import (
	"testing"
	"time"
)

// Key-to-paint is measured from the key to the frame that shows it. A key
// still waiting after its frame would be charged to some later, unrelated
// frame, and the histogram would report the reader's pauses as latency.
func TestAFrameAnswersEveryKeyWaitingOnIt(t *testing.T) {
	var w countingWriter
	tr := coalesceTranscript(t, &w)
	at := time.Now()

	tr.beginBatch()
	for range 3 {
		tr.keys.arrived(at)
		tr.scrollBy(-1)
	}
	tr.endBatch()
	if n := len(tr.keys.at); n != 0 {
		t.Fatalf("%d keys still wait after the frame that showed them", n)
	}

	tr.beginBatch()
	tr.keys.arrived(at)
	tr.scrollBy(0)
	tr.endBatch()
	tr.render()
	if n := len(tr.keys.at); n != 0 {
		t.Fatalf("%d keys still wait after a frame that had nothing new to show", n)
	}
}

func TestKeysWaitingForPaintAreBounded(t *testing.T) {
	var k keysAwaitingPaint
	for range 2 * keysAwaitingMax {
		k.arrived(time.Now())
	}
	if len(k.at) != keysAwaitingMax {
		t.Fatalf("%d keys held, want at most %d", len(k.at), keysAwaitingMax)
	}
}

// A key typed while a pit opens was answered by whatever frame came next,
// usually one without the pit, or dropped at the end of its read because no
// frame was owed. The slowest case, a pit that takes seconds to open, never
// reached the histogram. Those keys are owed until the pit is on screen.
func TestKeysTypedWhileAPitOpensAreAnsweredByThePit(t *testing.T) {
	in, lt := navInput(t, &countingWriter{}, true)
	lt.tr.command = func(string) {}
	in.consume([]byte("lj"))
	in.mu.Lock()
	defer in.mu.Unlock()
	if n := len(lt.tr.keys.at); n != 2 {
		t.Fatalf("%d keys owed while the pit opens, want 2", n)
	}
	lt.tr.setCmdOut("ls", awaitRows())
	lt.tr.render()
	if n := len(lt.tr.keys.at); n != 0 {
		t.Fatalf("%d keys still owed after the pit painted", n)
	}
}

func TestEachPitIsItsOwnView(t *testing.T) {
	in, lt := navInput(t, &countingWriter{}, true)
	in.mu.Lock()
	defer in.mu.Unlock()
	for _, c := range []struct {
		open func()
		want string
	}{
		{func() {}, "transcript"},
		{func() { lt.tr.setCmdOut("ls", awaitRows()) }, "pit"},
		{func() { lt.tr.inSearch = true }, "search"},
		{func() { lt.tr.inSearch = false; lt.tr.box = boxCommand }, "command"},
		{func() { lt.tr.box = boxComposer }, "composer"},
	} {
		c.open()
		if got := lt.tr.paintView().String(); got != c.want {
			t.Errorf("paintView = %q, want %q", got, c.want)
		}
	}
}
