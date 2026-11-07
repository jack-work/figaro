package render

import (
	"strings"
	"testing"
)

func textRows(md string, width int) []string {
	var out []string
	for _, r := range plainRows(md, width) {
		if s := strings.TrimSpace(r); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// A CommonMark hard line break (two trailing spaces, or a backslash, before
// the newline) was joined into the next line, as a soft break is: only a
// blank line broke. A hard break starts a new row; a soft break still joins.
func TestAHardLineBreakStartsANewRow(t *testing.T) {
	for name, md := range map[string]string{
		"two spaces": "Largo al factotum  \ndella città",
		"backslash":  "Largo al factotum\\\ndella città",
	} {
		got := textRows(md, 80)
		if len(got) != 2 || got[0] != "Largo al factotum" || got[1] != "della città" {
			t.Errorf("%s: rows %q, want the two lines apart", name, got)
		}
	}
	for _, md := range []string{"Largo al factotum\ndella città", "Largo al factotum \ndella città"} {
		if got := textRows(md, 80); len(got) != 1 || strings.Join(strings.Fields(got[0]), " ") != "Largo al factotum della città" {
			t.Errorf("soft break %q: rows %q, want one joined row", md, got)
		}
	}
	quoted := textRows("> first line  \n> second line", 80)
	if len(quoted) != 2 || !strings.HasSuffix(quoted[1], "second line") || !strings.HasPrefix(quoted[1], strings.Fields(quoted[0])[0]) {
		t.Errorf("quoted hard break: rows %q, want two rows under the quote bar", quoted)
	}
	item := textRows("- first line  \n  second line", 80)
	if len(item) != 2 || item[1] != "second line" {
		t.Errorf("list item hard break: rows %q, want the second line on its own row", item)
	}
}
