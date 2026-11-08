package cli

import (
	"fmt"
	"strings"
	"testing"
)

func spanRows() []pitRow {
	var rows []pitRow
	for i := range 8 {
		rows = append(rows, idRow(fmt.Sprintf("row %d", i), fmt.Sprintf("r%d", i)))
	}
	return append(rows,
		idRow("me", "me"), idRow("me.forks", "me.forks"),
		idRow("me.forks.a", "me.forks.a"), idRow("me.forks.b", "me.forks.b"),
		idRow("after", "zz"),
	)
}

// A seek lands on a row whose children were opened for the reader; the
// cursor alone kept in view would sit on the last line with every child
// below the fold. focusSubtree keeps the row's subtree on screen.
func TestASeekKeepsTheRowsSubtreeInView(t *testing.T) {
	p := newPicker(spanRows())
	if !p.focusSubtree("me") {
		t.Fatal("no row me")
	}
	shown := strings.Join(p.lines(pitTroupe, 60, 6), "\n")
	for _, want := range []string{"me", "me.forks.a", "me.forks.b"} {
		if !strings.Contains(shown, want) {
			t.Errorf("a six-row pit after the seek does not show %q:\n%s", want, shown)
		}
	}
}
