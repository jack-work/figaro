package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func longListRows(n int) []pitRow {
	rows := make([]pitRow, n)
	for i := range rows {
		rows[i] = pitRow{text: fmt.Sprintf("  row %02d", i), id: fmt.Sprintf("r%02d", i)}
	}
	return rows
}

func twoRowForm(tr *transcript) {
	tr.showLivePit("form", &formView{src: &fakeFormSource{snap: form.FromMap(formKeys(
		"alpha", `"one"`, "beta", `"two"`,
	))}, open: map[string]bool{}}, false)
}

// The search line took its row from the pit, so a pit lost a row of what it
// was searching the moment / was pressed. The pit keeps every row it drew and
// the search line is a row of its own, at every keystroke.
func TestASearchLineDoesNotTakeARowFromThePit(t *testing.T) {
	cases := []struct {
		name  string
		open  func(*transcript)
		query string
	}{
		{"a list", func(tr *transcript) { tr.setCmdOut("ls", longListRows(40)) }, "row 0"},
		{"a form", twoRowForm, "alp"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out countingWriter
			in, lt := navInput(t, &out, true)
			c.open(lt.tr)
			before := stripANSIAll(lt.tr.drawerLines())
			feed(t, in, "/")
			for _, b := range c.query {
				feed(t, in, string(b))
				during := stripANSIAll(lt.tr.drawerLines())
				if len(during) != len(before)+1 {
					t.Fatalf("after %q the drawer is %d rows, want the pit's %d and the search line:\n%s",
						lt.tr.query, len(during), len(before), strings.Join(during, "\n"))
				}
				if line := lt.tr.inputDrawerLines(); len(line) != 1 {
					t.Fatalf("after %q the search line is %d rows, want 1", lt.tr.query, len(line))
				}
			}
		})
	}
}
