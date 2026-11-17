package cli

import (
	"context"
	"errors"
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

// v0.71.0: `:fork` is an overlay verb, so no pit fence was armed, and the
// 150 ms progress repaint of the figaro being left answered every key. The
// histogram clamped at 151 ms while the fork took 250 ms to reach the screen.
func TestKeysTypedForASwitchAreAnsweredByTheNewFigaro(t *testing.T) {
	for _, line := range []string{"fork aria0001:20", "attend aria0002", "listen aria0002", "new"} {
		in, lt := navInput(t, &countingWriter{}, true)
		lt.tr.command = func(string) {}
		in.consume([]byte(":" + line + "\r"))
		in.mu.Lock()
		owed := len(lt.tr.keys.at)
		if owed == 0 {
			in.mu.Unlock()
			t.Fatalf("%s: no keys owed after typing it", line)
		}
		lt.tr.setProgressNote("working on " + line)
		if n := len(lt.tr.keys.at); n != owed {
			in.mu.Unlock()
			t.Fatalf("%s: the old figaro's progress frame answered %d of %d keys", line, owed-n, owed)
		}
		lt.retarget("aria0002", newSessionStatus("aria0002", time.Unix(0, 0)), 0)
		lt.tr.render()
		if n := len(lt.tr.keys.at); n != 0 {
			in.mu.Unlock()
			t.Fatalf("%s: %d keys still owed after the new figaro painted", line, n)
		}
		in.mu.Unlock()
	}
}

func TestASwitchThatFailsStillAnswersItsKeys(t *testing.T) {
	in, lt := navInput(t, &countingWriter{}, true)
	lt.tr.command = func(string) {}
	in.consume([]byte(":fork aria0001:20\r"))
	in.mu.Lock()
	fence := lt.tr.switching
	in.mu.Unlock()
	in.switchAsync(fence, func(context.Context) (string, error) { return "", errors.New("no such turn") })
	settling.wait()
	in.mu.Lock()
	defer in.mu.Unlock()
	if n := len(lt.tr.keys.at); n != 0 {
		t.Fatalf("%d keys still owed after the failed switch painted its error", n)
	}
}

// Review of batch 1: any command settling lifted the fence, so a :send typed
// behind a :fork let the old figaro's repaint answer the fork's keys.
func TestOnlyTheSwitchItselfLiftsItsFence(t *testing.T) {
	in, lt := navInput(t, &countingWriter{}, true)
	lt.tr.command = func(string) {}
	in.consume([]byte(":fork aria0001:20\r"))
	in.mu.Lock()
	older := lt.tr.switching
	in.mu.Unlock()
	in.consume([]byte(":attend aria0002\r"))
	in.commandAsync(func(context.Context) (string, error) { return "sent", nil })
	in.switchAsync(older, func(context.Context) (string, error) { return "", errors.New("no such turn") })
	settling.wait()
	in.mu.Lock()
	defer in.mu.Unlock()
	if lt.tr.switching == 0 {
		t.Fatal("a :send and an older switch settling lifted the newer switch's fence")
	}
	if len(lt.tr.keys.at) == 0 {
		t.Fatal("the attend's keys were answered before its figaro painted")
	}
}

// Review of batch 1: a replay refuses a switch on the tape fence and never
// reached the code that lifts it, so key_to_paint stopped recording for the
// rest of the session.
func TestASwitchRefusedByTheReplayFenceAnswersItsKeys(t *testing.T) {
	in, lt := navInput(t, &countingWriter{}, true)
	var line string
	lt.tr.command = func(l string) { line = l }
	in.replaying = true
	in.consume([]byte(":fork aria0001:20\r"))
	in.mu.Lock()
	fence := lt.tr.switching
	in.mu.Unlock()
	if fence == 0 || line == "" {
		t.Fatalf("fixture: no switch armed (fence %d, line %q)", fence, line)
	}
	in.execCommandAs(line, fence)
	settling.wait()
	in.mu.Lock()
	defer in.mu.Unlock()
	if lt.tr.switching != 0 {
		t.Fatal("the replay fence refused the switch and left its fence up")
	}
	if n := len(lt.tr.keys.at); n != 0 {
		t.Fatalf("%d keys still owed after the refusal painted", n)
	}
}
