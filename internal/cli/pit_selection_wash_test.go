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

// Matched letters were bold and underlined in the row's own colour, which on a
// long @arias list did not stand out. They are drawn in the palette's notice
// red, still bold, and in its lighter red on the selected row's wash; the
// row's colour comes back after each match.
func TestMatchedLettersAreBoldInThePalettesRed(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	g := len("├─ ")
	king := []int{g, g + 1, g + 2, g + 3}
	rows := []pitRow{
		{text: "├─ king of spain", id: "a", dimLead: g, marks: king},
		{text: "├─ kingdom", id: "b", dimLead: g, marks: king},
	}
	p := newPicker(rows)
	p.focus("a")
	lines := p.lines(pitForm, 60, 10)
	var selected, plain string
	for _, l := range lines {
		switch {
		case strings.Contains(l, pitWash):
			selected = l
		case strings.Contains(stripANSIAll([]string{l})[0], "kingdom"):
			plain = l
		}
	}
	for _, c := range []struct {
		name, line, red string
	}{
		{"plain", plain, term.NoticeBody()},
		{"selected", selected, term.NoticeOnWash()},
	} {
		at := strings.Index(c.line, "k")
		if at < 0 {
			t.Fatalf("%s: no match drawn: %q", c.name, c.line)
		}
		lead := c.line[:at]
		red := strings.LastIndex(lead, c.red)
		bold := strings.LastIndex(lead, "\x1b[1")
		if red < 0 || bold < red {
			t.Errorf("%s: the match is not red then bold (red at %d, bold at %d): %q", c.name, red, bold, c.line)
		}
		if strings.Contains(c.line[at:], "\x1b[0m") {
			t.Errorf("%s: a full reset inside the row: %q", c.name, c.line)
		}
	}
	if strings.Contains(plain+selected, "38;5;196") {
		t.Error("a raw 196 red, not the palette's")
	}
}
