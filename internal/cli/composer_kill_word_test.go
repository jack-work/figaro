package cli

import (
	"strings"
	"testing"
)

func composerDraft(t *testing.T, lines ...string) (*interactiveInput, *livelogTurn) {
	t.Helper()
	in, lt := navInput(t, &countingWriter{}, true)
	feed(t, in, ">")
	if lt.tr.box != boxComposer {
		t.Fatalf("'>' did not open the composer (box %v)", lt.tr.box)
	}
	feed(t, in, strings.Join(lines, "\r"))
	return in, lt
}

// Gluck, 2026-11-03: ^W at the start of a line took the last word of the line
// above with it. A backward word kill stays on its own line; at column 0 it
// takes only the newline that joins the two.
func TestBackwardWordKillStaysOnItsLine(t *testing.T) {
	const ctrlW, metaDel, home = "\x17", "\x1b\x7f", "\x01"
	for _, tc := range []struct {
		name   string
		lines  []string
		keys   []string
		draft  string
		cursor int
	}{
		{"start of line 2", []string{"foo bar", "baz qux"}, []string{home, ctrlW}, "foo barbaz qux", 7},
		{"start of line 2, M-DEL", []string{"foo bar", "baz qux"}, []string{home, metaDel}, "foo barbaz qux", 7},
		{"start of the buffer", []string{"foo bar"}, []string{home, ctrlW}, "foo bar", 0},
		{"mid-word on line 2", []string{"foo bar", "baz qu"}, []string{ctrlW}, "foo bar\nbaz ", 12},
		{"whole line 2", []string{"foo bar", "baz"}, []string{ctrlW}, "foo bar\n", 8},
		{"only spaces on line 2", []string{"foo bar", "   "}, []string{ctrlW}, "foo bar\n", 8},
		{"twice from line 2 takes the newline", []string{"foo bar", "baz"}, []string{ctrlW, ctrlW}, "foo bar", 7},
		{"trailing spaces above stay", []string{"foo bar  ", "baz"}, []string{home, ctrlW}, "foo bar  baz", 9},
		{"multibyte at line start", []string{"héllo wörld", "çà"}, []string{home, ctrlW}, "héllo wörldçà", 11},
		{"multibyte word", []string{"héllo wörld", "çà ñú"}, []string{ctrlW}, "héllo wörld\nçà ", 15},
		{"one line, unchanged", []string{"send --id abc12345"}, []string{ctrlW}, "send --id ", 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, lt := composerDraft(t, tc.lines...)
			for _, k := range tc.keys {
				feed(t, in, k)
			}
			if got := lt.tr.draft.String(); got != tc.draft {
				t.Fatalf("draft %q, want %q", got, tc.draft)
			}
			if got := lt.tr.draft.cursor; got != tc.cursor {
				t.Fatalf("cursor %d, want %d", got, tc.cursor)
			}
		})
	}
}
