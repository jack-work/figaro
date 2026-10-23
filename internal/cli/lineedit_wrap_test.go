package cli

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

// The composer wrapped a draft at whatever rune met the margin, so a word
// could end one row and finish the next, and a draft copied off the screen
// carried "m\n  iss" into the message it was pasted into.
func TestComposerWrapsAtWords(t *testing.T) {
	draft := "okay can you please give one final readthrough of my report having just seen my repo as a smoke test to make sure i didnt miss anything?"
	for _, avail := range []int{12, 23, 31, 40, 57} {
		e := ed(draft, -1)
		rows, _ := e.layout(avail)
		var joined []rune
		for i, r := range rows {
			if r.start != len(joined) {
				t.Fatalf("avail %d: row %d starts at %d, want %d", avail, i, r.start, len(joined))
			}
			joined = append(joined, r.runes...)
			if w := runewidth.StringWidth(strings.TrimRight(string(r.runes), " ")); w > avail-1 {
				t.Errorf("avail %d: row %q is %d cells", avail, string(r.runes), w)
			}
			if i+1 < len(rows) {
				next := rows[i+1].runes
				if len(r.runes) > 0 && len(next) > 0 && r.runes[len(r.runes)-1] != ' ' && next[0] != ' ' {
					t.Errorf("avail %d: %q | %q splits a word", avail, string(r.runes), string(next))
				}
			}
		}
		if string(joined) != draft {
			t.Fatalf("avail %d: the rows hold %q", avail, string(joined))
		}
	}
}

func TestComposerHardBreaksOnlyAWordWiderThanTheRow(t *testing.T) {
	e := ed("see https://keel.kelliher.info/_keel/jack-cs7641-notes/blob ok", -1)
	rows, _ := e.layout(20)
	if got := string(rows[0].runes); got != "see " {
		t.Fatalf("the long word must start its own row: first row %q", got)
	}
	for _, r := range rows {
		if w := runewidth.StringWidth(string(r.runes)); w > 19 {
			t.Errorf("row %q is %d cells", string(r.runes), w)
		}
	}
}

func TestComposerCursorFollowsAWrappedWord(t *testing.T) {
	draft := "aaaa bbbb cccc"
	for cursor := 0; cursor <= len(draft); cursor++ {
		e := ed(draft, cursor)
		rows, at := e.layout(11)
		r := rows[at]
		if cursor < r.start || cursor > r.start+len(r.runes) {
			t.Fatalf("cursor %d is drawn on row %d %q starting at %d", cursor, at, string(r.runes), r.start)
		}
		if at+1 < len(rows) && cursor >= rows[at+1].start {
			t.Fatalf("cursor %d belongs to row %d, drawn on %d", cursor, at+1, at)
		}
	}
}
