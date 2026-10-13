package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
)

// The wash is background only: a selected row paints the same cells, never
// grows past the pane (which would soft-wrap and desync the cursor math),
// and the focused block is washed apart from the range.
func TestDecorateNodeRowContract(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
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
			// A row ending inside an escape eats what follows; node
			// rows cannot carry one.
			continue
		}
		for _, w := range []int{-3, 0, 1, 2, 3, 10, 40, 100} {
			plain := plainNodeRow(row, w)
			if got := decorateNodeRow(plain, selectionMark{}, w); got != plain {
				t.Errorf("unselected decorate(%q, %d) = %q, want it untouched", row, w, got)
			}
			for _, mark := range marks {
				got := decorateNodeRow(plain, mark, w)
				if n, pane := displayWidth(got), max(w, 1); n > pane {
					t.Errorf("selected decorate(%q, %d) is %d cells; the pane is %d", row, w, n, pane)
				}
				if vis, want := render.StripEscapes(got), render.StripEscapes(plain); vis != want {
					t.Errorf("selected decorate(%q, %d) moved the text: %q, want %q", row, w, vis, want)
				}
				if !strings.Contains(got, term.NodeWash(mark.active)) {
					t.Errorf("selected decorate(%q, %d) = %q: no wash", row, w, got)
				}
			}
		}
	}
	if term.NodeWash(true) == term.NodeWash(false) {
		t.Error("the focused block must be washed apart from the range it is in")
	}
}

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

// The wash is the whole selection UI; without colour there is no cue.
func TestDecorateNodeRowIsWashOnly(t *testing.T) {
	defer term.SetColorMode(term.ColorNever)()
	for _, row := range []string{"  margin row", "✓ bash [1ms]", ""} {
		plain := plainNodeRow(row, 40)
		if got := decorateNodeRow(plain, selectionMark{selected: true}, 40); got != plain {
			t.Errorf("decorate(%q) without colour = %q, want the row untouched", row, got)
		}
	}
}

func TestDecorateNodeRowNoAllocUnmarked(t *testing.T) {
	plain := plainNodeRow("\x1b[2m  │ \x1b[0m a perfectly ordinary tool output row", 100)
	if got := testing.AllocsPerRun(100, func() {
		_ = decorateNodeRow(plain, selectionMark{}, 100)
	}); got != 0 {
		t.Errorf("decorateNodeRow on an unmarked row allocated %v times, want 0", got)
	}
}

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
