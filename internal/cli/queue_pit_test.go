package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/render"
)

const longQueued = "first line of a long queued prompt\n" +
	"second line, which is long enough that a pane seventy columns wide has to wrap it onto a row of its own\n" +
	"\n" +
	"third paragraph\nfourth\nfifth\nsixth\nseventh\neighth\nninth\ntenth\neleventh\ntwelfth line, the last"

func queuePitText(tr *transcript) string {
	return render.StripEscapes(strings.Join(tr.pit.lines(tr.w, tr.pitRoom()), "\n"))
}

// A queued message's id is a number, and a number is a valid aria id: Enter
// used to hand it to attend. It must open the message instead.
func TestQueuePit_EnterOpensTheMessageAndNeverAttends(t *testing.T) {
	tr := queuePager(t, queuedItem{id: 3, text: longQueued})
	attended := ""
	tr.attendAria = func(id string) { attended = id }
	tr.key('Q')
	tr.key(0x0d)
	if attended != "" {
		t.Fatalf("Enter on a queued message attended aria %q", attended)
	}
	tr.key('F')
	got := queuePitText(tr)
	for _, want := range []string{"second line, which is long enough", "a row of its own", "third paragraph", "twelfth line, the last"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the open message is missing %q:\n%s", want, got)
		}
	}
}

// j walks into the body a row at a time, and the pit scrolls to follow it, so
// a message taller than the pit can be read to its end.
func TestQueuePit_JWalksAndScrollsTheBody(t *testing.T) {
	tr := queuePager(t, queuedItem{id: 3, text: longQueued}, queuedItem{id: 4, text: "the next prompt"})
	tr.key('Q')
	tr.key(0x0d)
	if strings.Contains(queuePitText(tr), "twelfth line, the last") {
		t.Skip("the pit is tall enough to show the whole message; the test needs a short pit")
	}
	for range 40 {
		tr.key('j')
		if row, _ := tr.pit.selected(); strings.Contains(row.text, "the next prompt") {
			break
		}
	}
	if row, _ := tr.pit.selected(); !strings.Contains(row.text, "the next prompt") {
		t.Fatalf("j never reached the next message; stopped on %q", row.text)
	}
	tr.key('k')
	if row, _ := tr.pit.selected(); !strings.Contains(row.text, "twelfth line, the last") {
		t.Fatalf("k from the next message landed on %q, want the body's last row", row.text)
	}
	if !strings.Contains(queuePitText(tr), "twelfth line, the last") {
		t.Fatalf("the selected last row is not on screen:\n%s", queuePitText(tr))
	}
}

// Enter on any row of an open message folds it and puts the cursor back on its
// header; y anywhere in it yanks the whole message.
func TestQueuePit_EnterOnTheBodyFoldsIt(t *testing.T) {
	tr := queuePager(t, queuedItem{id: 3, text: longQueued})
	tr.key('Q')
	tr.key(0x0d)
	tr.key('j')
	tr.key('j')
	if row, _ := tr.pit.selected(); row.yank != longQueued {
		t.Fatalf("a body row yanks %q, want the whole message", row.yank)
	}
	tr.key(0x0d)
	row, _ := tr.pit.selected()
	if row.id != "3" {
		t.Fatalf("folding left the cursor on %q", row.id)
	}
	if strings.Contains(queuePitText(tr), "third paragraph") {
		t.Fatal("Enter on the body did not fold the message")
	}
}

// x on a body row drops the message, not the row, and every row of it goes.
func TestQueuePit_XOnTheBodyDropsTheMessage(t *testing.T) {
	tr := queuePager(t, queuedItem{id: 3, text: longQueued}, queuedItem{id: 4, text: "the next prompt"})
	var dropped []string
	tr.dropRow = func(pit, id, next string) { dropped = append(dropped, pit+":"+id+">"+next) }
	tr.key('Q')
	tr.key(0x0d)
	tr.key('j')
	tr.key('j')
	tr.key('x')
	if len(dropped) != 1 || dropped[0] != "queue:3>4" {
		t.Fatalf("dropped %q, want the message 3, landing on 4", dropped)
	}
	got := queuePitText(tr)
	if strings.Contains(got, "line") || !strings.Contains(got, "the next prompt") {
		t.Fatalf("the dropped message left rows behind:\n%s", got)
	}
	if row, _ := tr.pit.selected(); row.id != "4" {
		t.Fatalf("the cursor landed on %q", row.id)
	}
}

func TestQueuePit_AResizeRewrapsTheOpenMessage(t *testing.T) {
	tr := queuePager(t, queuedItem{id: 3, text: longQueued})
	tr.key('Q')
	tr.key(0x0d)
	tr.key('F')
	tr.resize(160, 40)
	for _, line := range strings.Split(queuePitText(tr), "\n") {
		if strings.Contains(line, "second line") && !strings.Contains(line, "row of its own") {
			t.Fatalf("at 160 columns the second line still wraps: %q", line)
		}
	}
}

// An open message is its text once: the header carries the first line and the
// body continues from the second.
func TestQueuePit_TheFirstLineIsNotDrawnTwice(t *testing.T) {
	tr := queuePager(t, queuedItem{id: 3, text: longQueued})
	tr.key('Q')
	tr.key(0x0d)
	tr.key('F')
	if n := strings.Count(queuePitText(tr), "first line of a long queued prompt"); n != 1 {
		t.Fatalf("the first line is drawn %d times:\n%s", n, queuePitText(tr))
	}
}
