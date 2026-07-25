package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/term"
)

// decorateNodeRowReference is the pre-optimization implementation: it took the
// raw node row plus the frame width and did the clip + gutter on every call.
// The split version must be byte-identical to it for every input.
func decorateNodeRowReference(row string, mark selectionMark, width int) string {
	if width < 2 {
		width = 2
	}
	body := clipToWidthReference(row, width-1)
	if !mark.selected && !mark.active {
		return " " + body
	}
	const (
		reset     = "\x1b[0m"
		bgSelect  = "\x1b[48;5;238m"
		gutterSel = "\x1b[36m▎"
		gutterFoc = "\x1b[1;96m▎"
	)
	if !term.Enabled() {
		return "▎" + body
	}
	gutter := gutterSel
	if mark.active {
		gutter = gutterFoc
	}
	body = strings.ReplaceAll(body, reset, reset+bgSelect)
	return bgSelect + gutter + reset + bgSelect + body + "\x1b[K" + reset
}

// (Run under FORCE_COLOR=1 as well to cover the styled branch; the reference
// mirrors the same term.Enabled() split, so both modes are checked.)
func TestDecorateNodeRowMatchesReference(t *testing.T) {
	marks := []selectionMark{
		{},
		{selected: true},
		{selected: true, active: true},
		{active: true},
	}
	rows := append([]string{
		"\x1b[2m  │ \x1b[0m   12  internal/cli/transcript.go:7: captured output",
		"✓ bash rg --line-number transcript internal/cli [12ms]",
		"a row that is quite a lot longer than the width it will be clipped to",
	}, clipCorpus...)
	for _, row := range rows {
		for _, w := range []int{-3, 0, 1, 2, 3, 10, 40, 100} {
			for _, mark := range marks {
				got := decorateNodeRow(plainNodeRow(row, w), mark)
				want := decorateNodeRowReference(row, mark, w)
				if got != want {
					t.Errorf("decorate(%q, %+v, %d) = %q, want %q", row, mark, w, got, want)
				}
			}
		}
	}
}

// TestDecorateNodeRowNoAllocUnmarked is the point: an undecorated row is
// returned as-is, so a frame with no selection allocates nothing per row.
func TestDecorateNodeRowNoAllocUnmarked(t *testing.T) {
	plain := plainNodeRow("\x1b[2m  │ \x1b[0m a perfectly ordinary tool output row", 100)
	if got := testing.AllocsPerRun(100, func() {
		_ = decorateNodeRow(plain, selectionMark{})
	}); got != 0 {
		t.Errorf("decorateNodeRow on an unmarked row allocated %v times, want 0", got)
	}
}

// TestTranscriptRowSearchText pins that history search still sees the row
// without the gutter column that plainNodeRow prepends.
func TestTranscriptRowSearchText(t *testing.T) {
	node := transcriptRow{text: plainNodeRow("hello world", 40), ref: nodeRef{lt: 3, index: 0}}
	if got := node.searchText(); got != "hello world" {
		t.Errorf("searchText() = %q, want %q", got, "hello world")
	}
	head := transcriptRow{text: "─── header ───"}
	if got := head.searchText(); got != head.text {
		t.Errorf("searchText() on a ref-less row = %q, want %q", got, head.text)
	}
}
