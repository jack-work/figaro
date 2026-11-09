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
