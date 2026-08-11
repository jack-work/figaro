package term

import (
	"bufio"
	"io"
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

// A prompt must end on \r as surely as on \n. On a Windows console left in raw
// mode by a figaro that died without unwinding (console mode belongs to the
// console, not the process), Enter delivers a bare \r, and the old
// ReadString('\n') waited for a byte that would never arrive. MEASURED:
// `figaro login copilot` hung at its first prompt, echoing nothing, and took
// two Ctrl-C to leave.
func TestReadLineTerminators(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		rest string // what the NEXT prompt must see
	}{
		{"lf", "github.com\nnext", "github.com", "next"},
		{"crlf", "github.com\r\nnext", "github.com", "next"},
		{"bare cr (raw console)", "github.com\rnext", "github.com", "next"},
		{"eof without terminator", "github.com", "github.com", ""},
		{"empty line accepts the default", "\nnext", "", "next"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := bufio.NewReader(strings.NewReader(tc.in))
			got, err := readLine(r)
			if err != nil && err != io.EOF {
				t.Fatalf("readLine: %v", err)
			}
			if got != tc.want {
				t.Fatalf("line = %q, want %q", got, tc.want)
			}
			// The paired \n of a CRLF must not surface as a phantom
			// empty line at the next prompt.
			rest, _ := io.ReadAll(r)
			if string(rest) != tc.rest {
				t.Fatalf("next prompt would see %q, want %q", rest, tc.rest)
			}
		})
	}
}

// present/absent exist so a tree can mark what is there and what is not. They
// are required to be the diff renderer's own green and red: the claim lives
// here, where the palette is defined, rather than in each caller that paints.
func TestPresentAndAbsentShareTheDiffForegrounds(t *testing.T) {
	restore := SetColorMode(ColorAlways)
	defer restore()

	for _, tc := range []struct{ name, role, diff string }{
		{"present", active.present, active.diffAdd},
		{"absent", active.absent, active.diffDel},
	} {
		fg, _, _ := strings.Cut(tc.diff, ";48;")
		if tc.role != fg+"m" {
			t.Errorf("%s = %q, want the diff foreground %q (from %q)",
				tc.name, tc.role, fg+"m", tc.diff)
		}
	}
}
