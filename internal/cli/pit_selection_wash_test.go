package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/term"
)

const pitWash = "\x1b[48;5;240m"

// The selected row's wash ended at a nested row's guide: the dim guide closed
// with a full reset, which took the background with it, so only the prefix
// read as selected. The wash must run from the glyph to the pane's edge.
func TestTheSelectedRowIsWashedAcrossTheWholeLine(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	rows := []pitRow{
		{text: "notes (3)", id: "notes"},
		{text: "├─ alpha (2)  body · title", id: "notes.alpha", dimLead: len("├─ "), marks: []int{len("├─ ")}},
		{text: "└─ beta: value", id: "notes.beta", dimLead: len("└─ "), tone: toneWarn},
	}
	const w = 60
	for _, id := range []string{"notes", "notes.alpha", "notes.beta"} {
		p := newPicker(rows)
		p.focus(id)
		var line string
		for _, l := range p.lines(pitForm, w, 10) {
			if strings.Contains(l, pitWash) {
				line = l
			}
		}
		if line == "" {
			t.Fatalf("%s: no washed row drawn", id)
		}
		body := line[strings.Index(line, pitWash)+len(pitWash):]
		end := strings.LastIndex(body, "\x1b[49m")
		if end < 0 {
			t.Fatalf("%s: the wash is never closed: %q", id, line)
		}
		inside := body[:end]
		for _, cut := range []string{"\x1b[0m", "\x1b[49m", "\x1b[m"} {
			if strings.Contains(inside, cut) {
				t.Errorf("%s: the wash is cut by %q inside the row: %q", id, cut, line)
			}
		}
		if got := displayWidth(line); got != w {
			t.Errorf("%s: the washed row is %d columns, want the pane's %d: %q", id, got, w, line)
		}
	}
}
