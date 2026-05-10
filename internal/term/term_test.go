package term

import (
	"os"
	"strings"
	"testing"
)

func TestDimNoColor(t *testing.T) {
	os.Setenv("NO_COLOR", "1")
	defer os.Unsetenv("NO_COLOR")
	// Re-detect after env change.
	mode = ColorNever

	got := Dim("hello")
	if strings.Contains(got, "\033[") {
		t.Errorf("Dim should not contain ANSI when NO_COLOR is set: %q", got)
	}
	if got != "hello" {
		t.Errorf("Dim = %q, want %q", got, "hello")
	}
}

func TestDimForceColor(t *testing.T) {
	os.Setenv("FORCE_COLOR", "1")
	defer os.Unsetenv("FORCE_COLOR")
	mode = ColorAlways

	got := Dim("hello")
	if !strings.Contains(got, "\033[2m") {
		t.Errorf("Dim should contain dim ANSI with FORCE_COLOR: %q", got)
	}
}

func TestCursorUp(t *testing.T) {
	got := CursorUp(3)
	if got != "\033[3A" {
		t.Errorf("CursorUp(3) = %q, want %q", got, "\033[3A")
	}
}

func TestCursorDown(t *testing.T) {
	got := CursorDown(5)
	if got != "\033[5B" {
		t.Errorf("CursorDown(5) = %q, want %q", got, "\033[5B")
	}
}

func TestWidth(t *testing.T) {
	// In CI/test environment, stdout is probably not a TTY.
	w := Width()
	if w < 20 {
		t.Errorf("Width() = %d, expected >= 20", w)
	}
}
