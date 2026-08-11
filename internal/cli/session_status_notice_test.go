package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// The status row is the pager's only voice for trouble, and it is also the
// row a reader parses as a sentence. Two properties, both asserted here:
//
//   - a notice sits at the LEFT, in red, and is the last thing shed
//   - the row ends in an ellipsis when it does not fit, never a bare cut
//
// Why the notice lives in the frame buffer at all: writing it to the terminal
// while the pager is up scrolls the alt grid out from under the painter and
// smears a frozen status line across the transcript. See
// transcript_screenmoved_test.go for that measurement.

func statusFixture(mantra string) *sessionStatus {
	s := newSessionStatus("abc12345", time.Date(2026, 6, 1, 12, 34, 56, 0, time.UTC))
	s.update(aria.Metrics{Mantra: mantra, ContextTokens: 11800, ContextLimit: 1000000, TokensIn: 4000, TokensOut: 500})
	return s
}

func TestStatusNoticeIsRedAndLeftmost(t *testing.T) {
	s := statusFixture("a perfectly ordinary mantra")
	s.setNotice("error: overloaded")
	line := s.statusLine(200, true)

	if !strings.HasPrefix(line, "\x1b[22;31merror: overloaded\x1b[39;2m") {
		t.Fatalf("notice must open the row, re-lit red against the dim wrapper: %q", line)
	}
	if i, j := strings.Index(line, "overloaded"), strings.Index(line, "ordinary"); i > j {
		t.Errorf("notice must precede the mantra: %q", line)
	}
	// And it is only lit for its own span: what follows is dim again.
	if strings.Count(line, "\x1b[39;2m") != 1 {
		t.Errorf("notice must hand the row back to the dim default exactly once: %q", line)
	}
}

// The shed order exists because narrow panes cannot hold everything. Trouble
// is the one token that must never be what makes room.
func TestStatusNoticeShedsLast(t *testing.T) {
	s := statusFixture("a perfectly ordinary mantra that is quite long indeed")
	s.setNotice("error: overloaded")
	for _, w := range []int{120, 90, 70, 50, 40, 30} {
		line := s.statusLine(w, true)
		if !strings.Contains(line, "error") {
			t.Errorf("width %d shed the notice: %q", w, line)
		}
		if got := displayWidth(line); got > w {
			t.Errorf("width %d: row is %d columns wide: %q", w, got, line)
		}
	}
}

// Clearing is how a notice stops being true.
func TestStatusNoticeClears(t *testing.T) {
	s := statusFixture("m")
	s.setNotice("error: overloaded")
	s.setNotice("")
	if line := s.statusLine(200, true); strings.Contains(line, "overloaded") {
		t.Fatalf("notice survived being cleared: %q", line)
	}
}

// Multi-line hints (providerSetupHint is several lines) must not break the
// one-row contract; the full text is reprinted on leaving the pager.
func TestStatusNoticeIsOneLine(t *testing.T) {
	s := statusFixture("m")
	s.setNotice("first line\nsecond line\n\tthird")
	line := s.statusLine(200, true)
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
		line := s.statusLine(w, true)
		if got := displayWidth(line); got > w {
			t.Fatalf("width %d: %d columns: %q", w, got, line)
		}
		if !strings.Contains(line, "…") {
			t.Errorf("width %d: overflow was cut with no ellipsis: %q", w, line)
		}
	}
}

// A row that fits must be left exactly alone: no ellipsis, no rewrite.
func TestStatusLineNoEllipsisWhenItFits(t *testing.T) {
	s := statusFixture("m")
	line := s.statusLine(400, true)
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

// displayWidth exists because runewidth.StringWidth counts the BYTES of an SGR
// run as columns, a dim wrapper alone measures eight columns of nothing, and
// the footer would shed tokens that fit.
func TestDisplayWidthIgnoresEscapes(t *testing.T) {
	if got := displayWidth("\x1b[2mfive!\x1b[0m"); got != 5 {
		t.Errorf("displayWidth = %d, want 5", got)
	}
	if got := displayWidth("\x1b[22;31mred\x1b[39;2m"); got != 3 {
		t.Errorf("displayWidth = %d, want 3", got)
	}
	if got := displayWidth("日本"); got != 4 {
		t.Errorf("displayWidth = %d, want 4", got)
	}
}
