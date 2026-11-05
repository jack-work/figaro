package cli

import (
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
