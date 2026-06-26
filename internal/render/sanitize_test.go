package render

import (
	"strings"
	"testing"
)

func TestSanitize_StripsAltScreen(t *testing.T) {
	in := "before\x1b[?1049hduring\x1b[?1049lafter"
	got := SanitizeForTerminal(in)
	want := "beforeduringafter"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestSanitize_StripsCursorVisibility(t *testing.T) {
	in := "x\x1b[?25ly\x1b[?25hz"
	if got := SanitizeForTerminal(in); got != "xyz" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitize_StripsLineWrap(t *testing.T) {
	in := "x\x1b[?7ly\x1b[?7hz"
	if got := SanitizeForTerminal(in); got != "xyz" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitize_StripsMouseModes(t *testing.T) {
	in := "x\x1b[?1000hy\x1b[?1006;1015lz"
	if got := SanitizeForTerminal(in); got != "xyz" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitize_StripsOSCWithBEL(t *testing.T) {
	in := "before\x1b]0;evil title\x07after"
	if got := SanitizeForTerminal(in); got != "beforeafter" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitize_StripsOSCWithST(t *testing.T) {
	in := "before\x1b]0;evil title\x1b\\after"
	if got := SanitizeForTerminal(in); got != "beforeafter" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitize_StripsRIS(t *testing.T) {
	in := "before\x1bcafter"
	if got := SanitizeForTerminal(in); got != "beforeafter" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitize_StripsCursorSaveRestore(t *testing.T) {
	in := "x\x1b7y\x1b8z"
	if got := SanitizeForTerminal(in); got != "xyz" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitize_StripsKeypadModes(t *testing.T) {
	in := "x\x1b=y\x1b>z"
	if got := SanitizeForTerminal(in); got != "xyz" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitize_KeepsSGR(t *testing.T) {
	in := "\x1b[31mred\x1b[0m \x1b[1;32mgreen-bold\x1b[m end"
	got := SanitizeForTerminal(in)
	if got != in {
		t.Fatalf("SGR should be preserved verbatim; got %q want %q", got, in)
	}
}

func TestSanitize_KeepsCursorMoves(t *testing.T) {
	// Painter uses these — leave them alone.
	in := "row1\x1b[Krow2\x1b[1Brow3"
	got := SanitizeForTerminal(in)
	if got != in {
		t.Fatalf("cursor primitives should be preserved; got %q want %q", got, in)
	}
}

func TestSanitize_DropsScrollRegion(t *testing.T) {
	in := "x\x1b[5;10ry"
	if got := SanitizeForTerminal(in); got != "xy" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitize_EmptyAndNoEscapes(t *testing.T) {
	if got := SanitizeForTerminal(""); got != "" {
		t.Fatalf("empty: got %q", got)
	}
	if got := SanitizeForTerminal("plain text 123"); got != "plain text 123" {
		t.Fatalf("plain: got %q", got)
	}
}

func TestSanitize_TruncatedEscape(t *testing.T) {
	// Defensively drop trailing partial escape rather than embed it.
	got := SanitizeForTerminal("hello\x1b[")
	if got != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitize_HuhAltScreenEnterExitRoundTrip(t *testing.T) {
	// Concrete: a captured huh smoke-test output. Should produce
	// only the visible text it would have shown, minus all state
	// changes.
	in := "\x1b[?1049h\x1b[?25l\x1b[?7l" +
		"\x1b[1;1H\x1b[2J" +
		"Welcome to figaro" +
		"\x1b[?25h\x1b[?7h\x1b[?1049l"
	got := SanitizeForTerminal(in)
	// Keep cursor-positioning + erase-display (painter-class), drop
	// the dangerous private modes.
	if strings.Contains(got, "\x1b[?") {
		t.Fatalf("got contains private mode: %q", got)
	}
	if !strings.Contains(got, "Welcome to figaro") {
		t.Fatalf("got drops visible text: %q", got)
	}
}
