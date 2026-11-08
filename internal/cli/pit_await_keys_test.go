package cli

import (
	"strings"
	"testing"
)

// jiffy-wakeup, recording v0.56.2: '/' typed 1.4 s after 'l' never reached the
// tree's search, and the Enter after it attended the row under the cursor. A
// key that arrives while a pit is still opening belongs to that pit; it must
// wait for it, never go to whatever is underneath.

const awaitAria = "aaaaaaaa11111111aaaaaaaa11111111"

func awaitRows() []pitRow {
	return []pitRow{
		{text: "  alpha-one", id: awaitAria, aria: awaitAria},
		{text: "  bravo-two", id: "bbbbbbbb22222222bbbbbbbb22222222", aria: "bbbbbbbb22222222bbbbbbbb22222222"},
		{text: "  charlie-three", id: "cccccccc33333333cccccccc33333333", aria: "cccccccc33333333cccccccc33333333"},
	}
}

func TestKeysTypedWhileAPitOpensWaitForIt(t *testing.T) {
	p := pitFramesPager(t, 100, 30)
	var ran []string
	p.tr.command = func(line string) { ran = append(ran, line) }
	var attended []string
	p.tr.attendAria = func(id string) { attended = append(attended, id) }

	pressKeys(p.tr, "l")
	if len(ran) != 1 || ran[0] != "ls" {
		t.Fatalf("fixture: l ran %q", ran)
	}
	pressKeys(p.tr, "/bravo\r")
	if p.tr.inSearch || p.tr.query != "" {
		t.Fatalf("a key for the opening pit searched the transcript underneath (query %q)", p.tr.query)
	}
	if len(attended) != 0 {
		t.Fatalf("a key for the opening pit attended %q before the pit was there", attended)
	}

	p.tr.setCmdOut("ls", awaitRows())
	row, ok := p.tr.pit.selected()
	if !ok || !strings.Contains(row.str(), "bravo") {
		t.Fatalf("the keys typed while the pit opened did not search it: selected %q", row.str())
	}
	if len(attended) != 0 {
		t.Fatalf("the Enter that closed the search attended %q", attended)
	}
	pressKeys(p.tr, "\r")
	if len(attended) != 1 || attended[0] != "bbbbbbbb22222222bbbbbbbb22222222" {
		t.Fatalf("Enter on the row the search found attended %q", attended)
	}
}

func TestAPitThatNeverOpensDropsTheKeysWaitingForIt(t *testing.T) {
	p := pitFramesPager(t, 100, 30)
	p.tr.command = func(string) {}
	pressKeys(p.tr, "l")
	pressKeys(p.tr, "/x")
	if len(p.later) == 0 {
		t.Fatal("waiting for the pit armed no limit")
	}
	p.fire()
	if p.tr.inSearch {
		t.Fatal("keys that waited for a pit that never came were handed to the transcript")
	}
	pressKeys(p.tr, "/")
	if !p.tr.inSearch {
		t.Fatal("after the wait gave up, keys did not reach the transcript again")
	}
}

// The keyboard loop answers some keys itself before the pager sees them: in
// the recording-shaped burst "l/arias", the s toggled the sticky header
// underneath and the tree's search got "aria".
func TestABurstTypedWhileAPitOpensReachesItWhole(t *testing.T) {
	in, lt := navInput(t, &countingWriter{}, true)
	lt.tr.command = func(string) {}
	sticky := lt.view.settings.sticky
	in.consume([]byte("l/arias"))
	in.mu.Lock()
	defer in.mu.Unlock()
	if lt.view.settings.sticky != sticky {
		t.Fatal("a key typed for the opening pit toggled the sticky header underneath")
	}
	lt.tr.setCmdOut("ls", awaitRows())
	if !lt.tr.inSearch || lt.tr.query != "arias" {
		t.Fatalf("the pit's search got %q (searching %v), want \"arias\"", lt.tr.query, lt.tr.inSearch)
	}
}
