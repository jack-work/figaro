package cli

import (
	"github.com/jack-work/figaro/api/form"
	"strings"
	"testing"
)

// The search line replaced the drawer it was searching, so the rows being
// searched vanished the moment `/` was pressed.
func TestTheSearchedPitStaysOnScreen(t *testing.T) {
	var out strings.Builder
	tr := newTestTranscript(&out)
	tr.enter()
	tr.setCmdOut("ls", []pitRow{staticRow("  alpha row"), staticRow("  beta row"), staticRow("  gamma row")})
	before := stripANSIAll(tr.footLines())

	tr.key('/')
	for _, b := range []byte("bet") {
		tr.key(b)
	}
	if !tr.inSearch {
		t.Fatal("fixture: / did not open the search line")
	}
	during := stripANSIAll(tr.footLines())
	if last := during[len(during)-1]; !strings.HasSuffix(last, "/bet") {
		t.Fatalf("the search line is not the last drawer row: %q", during)
	}
	for _, row := range before[1:] {
		if !containsRow(during, row) {
			t.Errorf("the pit row %q left the screen while it was being searched:\n%s", row, strings.Join(during, "\n"))
		}
	}
	if body, _ := tr.layoutNow(); body+len(during)+1+tr.barRows() > tr.h {
		t.Errorf("the layout reserved fewer rows than the drawer draws: body %d, drawer %d, pane %d", body, len(during), tr.h)
	}
}

// ^N and ^P while typing a search in a plain list (the :ls pit) went to the
// conversation's node selection, so the rows being searched never moved.
func TestCtrlNAndCtrlPStepAListPitsMatchesWhileSearching(t *testing.T) {
	var out countingWriter
	in, lt := navInput(t, &out, true)
	lt.tr.setCmdOut("ls", []pitRow{
		{text: "  alpha row", id: "a"},
		{text: "  beta match", id: "b"},
		{text: "  gamma row", id: "c"},
		{text: "  delta match", id: "d"},
	})
	at := func() string {
		row, _ := lt.tr.pit.selected()
		return row.id
	}
	feed(t, in, "/match")
	steps := []struct {
		key, want string
	}{
		{"\x0e", "b"},
		{"\x0e", "d"},
		{"\x0e", "b"},
		{"\x10", "d"},
		{"\x10", "b"},
	}
	for i, s := range steps {
		feed(t, in, s.key)
		if got := at(); got != s.want {
			t.Fatalf("step %d (%q): on %q, want %q", i, s.key, got, s.want)
		}
	}
	if !lt.tr.inSearch {
		t.Error("stepping closed the search line")
	}
}

// The pit sized itself to its rows, so it opened short on a small list and
// shrank under the reader's eyes as a search narrowed it. It opens at the
// partial height whatever it holds, and keeps it while the search line is up.
func TestThePitKeepsItsHeightThroughASearch(t *testing.T) {
	open := map[string]func(tr *transcript){
		"list": func(tr *transcript) {
			tr.setCmdOut("ls", []pitRow{
				{text: "  alpha row", id: "a"},
				{text: "  beta row", id: "b"},
				{text: "  gamma row", id: "c"},
			})
		},
		"form": func(tr *transcript) {
			v := &formView{src: &fakeFormSource{snap: form.FromMap(formKeys(
				"alpha", `"one"`, "beta", `"two"`, "gamma", `"three"`,
			))}, open: map[string]bool{}}
			tr.showLivePit("form", v, false)
		},
	}
	for name, show := range open {
		t.Run(name, func(t *testing.T) {
			var out strings.Builder
			tr := newTestTranscript(&out)
			tr.enter()
			show(tr)
			want := pickerRows
			if got := len(tr.drawerLines()); got != want {
				t.Fatalf("the pit opened %d rows tall, want the partial height %d", got, want)
			}
			tr.key('/')
			for _, b := range []byte("beta") {
				tr.key(b)
				if got := len(tr.drawerLines()); got != want {
					t.Fatalf("after %q the drawer is %d rows, want %d", tr.query, got, want)
				}
			}
			tr.key(0x1b)
			if got := len(tr.drawerLines()); got != want {
				t.Errorf("after the search the pit is %d rows, want %d", got, want)
			}
		})
	}
}

// A note on the bar's behalf is a glance, not a pit to browse: it stays one line.
func TestAGlanceNoteStaysItsOwnHeight(t *testing.T) {
	var out strings.Builder
	tr := newTestTranscript(&out)
	tr.enter()
	tr.pit.showNote("saved")
	if got := len(tr.drawerLines()); got != 1 {
		t.Errorf("a note drew %d rows, want 1", got)
	}
}

// The queue opens itself when a message is queued; padded to the partial
// height it took twelve rows of conversation for one line. Asked for with Q,
// it is a pit like any other.
func TestTheUnbiddenQueueKeepsItsOwnHeight(t *testing.T) {
	tr := queuePager(t, queuedItem{id: 3, text: "one queued prompt"})
	tr.showQueuedAuto(true)
	if !tr.showing(pitQueue) || tr.queuedByKey {
		t.Fatal("fixture: the queue did not open on its own")
	}
	if got, want := len(tr.drawerLines()), len(tr.pit.list().rows); got != want {
		t.Errorf("the unbidden queue drew %d rows, want its own %d", got, want)
	}
	tr.closePanels()
	tr.key('Q')
	if !tr.queuedByKey {
		t.Fatal("fixture: Q did not open the queue")
	}
	if got := len(tr.drawerLines()); got != pickerRows {
		t.Errorf("the queue asked for with Q drew %d rows, want the partial height %d", got, pickerRows)
	}
}
