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

func TestVisibleLen(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"hello", 5},
		{"\033[2mhello\033[0m", 5},
		{"\033[31m\033[2mab\033[0mcd", 4},
		{"", 0},
		{"───", 3},
	}
	for _, tc := range cases {
		got := VisibleLen(tc.in)
		if got != tc.want {
			t.Errorf("VisibleLen(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestTruncateVisible(t *testing.T) {
	got := TruncateVisible("hello world", 6)
	vl := VisibleLen(got)
	if vl > 6 {
		t.Errorf("TruncateVisible visible len = %d, want <= 6; got %q", vl, got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("expected ellipsis in %q", got)
	}
}

func TestWrapCount(t *testing.T) {
	cases := []struct {
		visLen, w, want int
	}{
		{80, 80, 1},
		{81, 80, 2},
		{160, 80, 2},
		{161, 80, 3},
		{0, 80, 1},
	}
	for _, tc := range cases {
		got := WrapCount(tc.visLen, tc.w)
		if got != tc.want {
			t.Errorf("WrapCount(%d, %d) = %d, want %d", tc.visLen, tc.w, got, tc.want)
		}
	}
}
