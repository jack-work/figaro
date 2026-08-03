package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
)

// The selection bar's contract, stated as properties rather than as a second
// copy of the implementation.
//
// It used to be pinned against decorateNodeRowReference — a transcription of
// the pre-optimization code, which was fine while the bar OWNED a column and
// the reference could be written in one line. The bar now stands inside the
// row's own left margin so the pager can render at the same width as the
// incipit (see barOverMargin), and a reference for that is the function again.
// So the test asserts what a reader can check on screen instead:
//
//  1. an unselected row is the stored row, untouched;
//  2. a selected row never leaves the pane, and never costs more than the one
//     column the bar occupies — a row that grew past the viewport would
//     soft-wrap and desync the painter's one-row-per-line cursor math;
//  3. the bar is always drawn — selection is never silent;
//  4. everything from column two rightwards is unchanged, so selecting a row
//     does not move its text.
func TestDecorateNodeRowContract(t *testing.T) {
	marks := []selectionMark{
		{selected: true},
		{selected: true, active: true},
		{active: true},
	}
	rows := append([]string{
		"\x1b[2m  │ \x1b[0m   12  internal/cli/transcript.go:7: captured output",
		"✓ bash rg --line-number transcript internal/cli [12ms]",
		"  a glamour-wrapped paragraph row, with its two-column margin",
		"\x1b[38;5;252m  styled from the first byte, margin and all\x1b[0m",
		"a row that is quite a lot longer than the width it will be clipped to",
	}, clipCorpus...)
	for _, row := range rows {
		if !closedEscapes(row) {
			// A row that ENDS inside an escape sequence paints nothing and eats
			// whatever follows it, bar included. Node rows cannot carry one —
			// render.Prose strips escapes on the way in and sanitizes on the way
			// out — and clipToWidth's own corpus (nodes_clip_test.go) is where
			// that robustness is pinned.
			continue
		}
		for _, w := range []int{-3, 0, 1, 2, 3, 10, 40, 100} {
			plain := plainNodeRow(row, w)
			if got := decorateNodeRow(plain, selectionMark{}, w); got != plain {
				t.Errorf("unselected decorate(%q, %d) = %q, want it untouched", row, w, got)
			}
			for _, mark := range marks {
				got := decorateNodeRow(plain, mark, w)
				// The bar stands in the margin where there is one (same width) and
				// displaces the row by one where there is not — never more, and
				// never past the pane.
				if n, pane := displayWidth(got), max(w, 1); n > pane || n > displayWidth(plain)+1 {
					t.Errorf("selected decorate(%q, %d) is %d cells; pane is %d and the row rests at %d",
						row, w, n, pane, displayWidth(plain))
				}
				vis := []rune(render.StripEscapes(got))
				if len(vis) == 0 || vis[0] != '▎' {
					t.Errorf("selected decorate(%q, %d) = %q: no bar in column one", row, w, got)
					continue
				}
				// Column two rightwards: unchanged. The bar either replaced a
				// margin blank or displaced the row by one, so the tail of the
				// selected row is the tail of the plain row, up to the clip.
				plainVis := []rune(render.StripEscapes(plain))
				tail, want := string(vis[1:]), string(plainVis)
				if strings.HasPrefix(want, " ") {
					want = want[1:] // the margin blank the bar stood in
				}
				if !strings.HasPrefix(want, tail) {
					t.Errorf("selected decorate(%q, %d) moved the text: tail %q is not the head of %q",
						row, w, tail, want)
				}
			}
		}
	}
}

// closedEscapes reports whether every escape sequence in s is terminated.
func closedEscapes(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			continue
		}
		j, _ := escapeEnd(s, i)
		if j >= len(s) && !isEscapeFinal(s[len(s)-1]) {
			return false
		}
		i = j - 1
	}
	return true
}

func isEscapeFinal(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// TestDecorateNodeRowBarIsVisibleWithoutColour: with colour off the wash is
// gone, so the bar is the ONLY selection cue there is. It must survive.
func TestDecorateNodeRowBarIsVisibleWithoutColour(t *testing.T) {
	if term.Enabled() {
		t.Skip("colour is on in this environment; the plain branch is what this pins")
	}
	for _, row := range []string{"  margin row", "✓ bash [1ms]", ""} {
		got := decorateNodeRow(plainNodeRow(row, 40), selectionMark{selected: true}, 40)
		if !strings.HasPrefix(render.StripEscapes(got), "▎") {
			t.Errorf("decorate(%q) without colour = %q, want a leading bar", row, got)
		}
	}
}

// TestDecorateNodeRowNoAllocUnmarked is the point: an undecorated row is
// returned as-is, so a frame with no selection allocates nothing per row.
func TestDecorateNodeRowNoAllocUnmarked(t *testing.T) {
	plain := plainNodeRow("\x1b[2m  │ \x1b[0m a perfectly ordinary tool output row", 100)
	if got := testing.AllocsPerRun(100, func() {
		_ = decorateNodeRow(plain, selectionMark{}, 100)
	}); got != 0 {
		t.Errorf("decorateNodeRow on an unmarked row allocated %v times, want 0", got)
	}
}

// TestTranscriptRowSearchText pins that history search sees the row as stored:
// node rows no longer carry a gutter column to strip.
func TestTranscriptRowSearchText(t *testing.T) {
	node := transcriptRow{text: plainNodeRow("hello world", 40), ref: nodeRef{turn: 3, index: 0}}
	if got := node.searchText(); got != "hello world" {
		t.Errorf("searchText() = %q, want %q", got, "hello world")
	}
	head := transcriptRow{text: "─── header ───"}
	if got := head.searchText(); got != head.text {
		t.Errorf("searchText() on a ref-less row = %q, want %q", got, head.text)
	}
}
