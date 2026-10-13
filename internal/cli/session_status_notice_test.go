package cli

import (
	"strings"

	"github.com/jack-work/figaro/internal/term"
	"testing"
	"time"

	"github.com/jack-work/figaro/sdk/aria"
)

// The notice lives in the frame buffer: writing it to the terminal while the pager
// is up scrolls the alt grid.

func statusFixture(mantra string) *sessionStatus {
	s := newSessionStatus("abc12345", time.Date(2026, 6, 1, 12, 34, 56, 0, time.UTC))
	s.update(aria.Metrics{Mantra: mantra, ContextTokens: 11800, ContextLimit: 1000000, TokensIn: 4000, TokensOut: 500})
	return s
}

func row(s *sessionStatus, w int) string {
	return strings.Join(s.viewOf(pitNothing, s.barVerbose(), time.Now()).render(w), "\n")
}

// The bar is one row or three: measure rows, not the joined string.
func rowWidth(s *sessionStatus, w int) int {
	widest := 0
	for _, r := range s.viewOf(pitNothing, s.barVerbose(), time.Now()).render(w) {
		widest = max(widest, displayWidth(r))
	}
	return widest
}

func TestStatusNoticeIsRedAndLeftmost(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()

	s := statusFixture("a perfectly ordinary mantra")
	s.setNoticeAt("error: overloaded", alertError)
	line := row(s, 200)

	if !strings.HasPrefix(line, term.NoticeInDim("error: overloaded")) {
		t.Fatalf("notice must open the row, re-lit against the dim wrapper: %q", line)
	}
	if i, j := strings.Index(line, "overloaded"), strings.Index(line, "ordinary"); i > j {
		t.Errorf("notice must precede the mantra: %q", line)
	}
	if strings.Count(line, "\x1b[39;2m") != 1 {
		t.Errorf("notice must hand the row back to the dim default exactly once: %q", line)
	}
}

func TestStatusNoticeShedsLast(t *testing.T) {
	s := statusFixture("a perfectly ordinary mantra that is quite long indeed")
	s.setNotice("error: overloaded")
	for _, w := range []int{120, 90, 70, 50, 40, 30} {
		line := row(s, w)
		if !strings.Contains(line, "error") {
			t.Errorf("width %d shed the notice: %q", w, line)
		}
		if got := rowWidth(s, w); got > w {
			t.Errorf("width %d: widest row is %d columns: %q", w, got, line)
		}
	}
}

func TestStatusNoticeClears(t *testing.T) {
	s := statusFixture("m")
	s.setNotice("error: overloaded")
	s.setNotice("")
	if line := row(s, 200); strings.Contains(line, "overloaded") {
		t.Fatalf("notice survived being cleared: %q", line)
	}
}

func TestStatusNoticeIsOneLine(t *testing.T) {
	s := statusFixture("m")
	s.setNotice("first line\nsecond line\n\tthird")
	line := row(s, 200)
	if strings.ContainsAny(line, "\n\t") {
		t.Fatalf("notice carried a line break into the row: %q", line)
	}
	if !strings.Contains(line, "first line second line third") {
		t.Fatalf("notice lost its words to the fold: %q", line)
	}
}

func TestStatusLineEllipsisOnOverflow(t *testing.T) {
	s := statusFixture("a mantra long enough to force the issue at narrow widths")
	s.setNotice("error: something went quite wrong in a long-winded way")
	for _, w := range []int{60, 40, 24, 12, 5, 2, 1} {
		line := row(s, w)
		if got := rowWidth(s, w); got > w {
			t.Fatalf("width %d: %d columns: %q", w, got, line)
		}
		if !strings.Contains(line, "…") {
			t.Errorf("width %d: overflow was cut with no ellipsis: %q", w, line)
		}
	}
}

func TestStatusLineNoEllipsisWhenItFits(t *testing.T) {
	s := statusFixture("m")
	line := row(s, 400)
	if strings.Contains(line, "…") {
		t.Fatalf("a row with room to spare was ellipsised: %q", line)
	}
}

func TestClipToWidthEllipsis(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  string
	}{
		{"short", 10, "short"},
		{"exactly-10", 10, "exactly-10"},
		{"eleven chars", 10, "eleven ch…\x1b[0m"},
		{"日本語のテキスト", 5, "日本…\x1b[0m"}, // wide runes: two columns each
		{"anything", 1, "…\x1b[0m"},
		{"anything", 0, ""},
	}
	for _, tc := range cases {
		got := clipToWidthEllipsis(tc.in, tc.width)
		if got != tc.want {
			t.Errorf("clipToWidthEllipsis(%q, %d) = %q, want %q", tc.in, tc.width, got, tc.want)
		}
		if w := displayWidth(got); w > tc.width {
			t.Errorf("clipToWidthEllipsis(%q, %d) is %d columns wide", tc.in, tc.width, w)
		}
	}
}

// runewidth.StringWidth counts the bytes of an SGR run as columns.
func TestDisplayWidthIgnoresEscapes(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()

	if got := displayWidth("\x1b[2mfive!\x1b[0m"); got != 5 {
		t.Errorf("displayWidth = %d, want 5", got)
	}
	if got := displayWidth(term.NoticeInDim("red")); got != 3 {
		t.Errorf("displayWidth = %d, want 3", got)
	}
	if got := displayWidth("日本"); got != 4 {
		t.Errorf("displayWidth = %d, want 4", got)
	}
}
