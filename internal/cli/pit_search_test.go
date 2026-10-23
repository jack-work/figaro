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
